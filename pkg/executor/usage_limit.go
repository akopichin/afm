package executor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Источники, по которым распознан usage-limit (UsageLimitError.Source).
const (
	// UsageLimitSourceStructured — лимит и момент сброса взяты из полей потока (rate_limit_event, api_error_params).
	UsageLimitSourceStructured = "structured"
	// UsageLimitSourceText — лимит или момент сброса разобраны из текста ошибки или stderr.
	UsageLimitSourceText = "text"
)

// UsageLimitError — агент упёрся в лимит подписки или квоты (а не во временный 429/529): повтор до сброса
// бессмыслен. Provider executor сам не определяет — он приходит из Config.Provider, который задаёт фабрика
// по resolved-команде.
type UsageLimitError struct {
	Provider string
	// ResetAt — момент сброса лимита; нулевое значение — неизвестен.
	ResetAt time.Time
	// Window — окно лимита (five_hour, seven_day, session, weekly…); может быть пустым.
	Window  string
	Message string
	// NoReset — квота без сброса (insufficient_quota): ждать бессмысленно, пока баланс не пополнят.
	NoReset bool
	Source  string
	// Progress — до отказа ход успел сделать работу (ответ или действие инструмента): это не повторный отказ подряд.
	Progress bool

	cause error
}

func (e *UsageLimitError) Error() string { return "usage limit reached: " + e.Message }

// TerminalResultError — CLI завершился с кодом 0, но его финальный корневой result помечен is_error: ход
// не удался, хотя процесс отработал штатно. Оркестратор классифицирует её как ошибку CLI, а не как успех.
type TerminalResultError struct {
	// Subtype — result.subtype (error_during_execution, error_max_turns…); может быть пустым.
	Subtype string
	// Message — result.result: текст, который CLI показал как причину отказа.
	Message        string
	APIErrorStatus int
}

func (e *TerminalResultError) Error() string {
	msg := "agent finished with an error result"
	if e.Subtype != "" {
		msg += " (" + e.Subtype + ")"
	}
	if e.Message != "" {
		msg += ": " + e.Message
	}
	return msg
}

// LimitWarning — провайдер сообщил, что лимит близок (rate_limit_event со status:"allowed_warning"): ход
// продолжается, ничего не останавливается. Это не действие агента, поэтому у него свой узкий callback
// Config.OnLimitWarning, а не OnAction.
type LimitWarning struct {
	Provider string
	// Window — окно лимита (five_hour, seven_day…); может быть пустым.
	Window string
	// ResetAt — момент сброса окна; нулевое значение — неизвестен.
	ResetAt time.Time
	// Utilization — доля израсходованного окна (0.92 = 92%); 0 — провайдер её не прислал.
	Utilization float64
}

// Unwrap отдаёт исходную ошибку запуска (например, exit status 1); она может быть nil.
func (e *UsageLimitError) Unwrap() error { return e.cause }

var (
	// «You've hit your session limit», «hit your usage limit», «hit your limit».
	hitLimitRE = regexp.MustCompile(`(?i)\bhit your(?:\s+(\w+))?\s+limit\b`)
	// Явный код временного троттлинга (HTTP 429 у OpenAI): это не лимит подписки, что бы ни говорил текст рядом.
	transientRateLimitRE = regexp.MustCompile(`(?i)rate_limit_exceeded`)
	// «Claude AI usage limit reached|<epoch>» (старый формат), «usage_limit_reached».
	usageLimitReachedRE = regexp.MustCompile(`(?i)usage[ _]limit[ _]reached`)
	legacyEpochRE       = regexp.MustCompile(`(?i)usage limit reached\|(\d{9,13})`)
	// Квота по балансу: сброса не будет, пока баланс не пополнят.
	noResetQuotaRE = regexp.MustCompile(`(?i)insufficient_quota|exceeded your current quota`)

	// «resets 11:50pm (UTC)», «resets Mon 12:00am», «Try again at 3:45 PM», «Try again at Oct 24th, 2026 3:45 PM».
	resetWhenRE = regexp.MustCompile(`(?i)(?:\bresets?(?:\s+at)?|\btry again at)\s+` +
		`(?:(?P<weekday>(?:mon|tue|wed|thu|fri|sat|sun)[a-z]*)\.?,?\s+)?` +
		`(?:(?P<month>(?:jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*)\.?\s+(?P<day>\d{1,2})(?:st|nd|rd|th)?,?\s*` +
		`(?:(?P<year>\d{4}),?\s*)?(?:at\s+)?)?` +
		`(?P<hour>\d{1,2})(?::(?P<minute>\d{2}))?\s*(?P<ampm>[ap]\.?m\.?)?` +
		`(?:\s*\((?P<tz>[^)]+)\))?`)
	resetWhenGroup = func() map[string]int {
		groups := make(map[string]int)
		for i, name := range resetWhenRE.SubexpNames() {
			groups[name] = i
		}
		return groups
	}()
)

// throttleWords — «hit your <word> limit» про временный троттлинг, а не про лимит подписки или квоты.
var throttleWords = map[string]bool{
	"rate": true, "request": true, "requests": true, "token": true, "tokens": true, "concurrency": true,
}

var weekdays = map[string]time.Weekday{
	"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
	"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday,
}

var months = map[string]time.Month{
	"jan": time.January, "feb": time.February, "mar": time.March, "apr": time.April, "may": time.May, "jun": time.June,
	"jul": time.July, "aug": time.August, "sep": time.September, "oct": time.October, "nov": time.November, "dec": time.December,
}

// parseUsageLimitText распознаёт usage-limit в тексте ошибки любого провайдера. Нераспознанный или невалидный
// момент сброса оставляет ResetAt нулевым — это не ошибка. Время без зоны трактуется в loc.
func parseUsageLimitText(msg string, now time.Time, loc *time.Location) (UsageLimitError, bool) {
	limit := UsageLimitError{Message: strings.TrimSpace(msg), Source: UsageLimitSourceText}
	if m := legacyEpochRE.FindStringSubmatch(msg); m != nil {
		epoch, _ := strconv.ParseInt(m[1], 10, 64)
		limit.ResetAt = epochToTime(float64(epoch))
		return limit, true
	}
	if transientRateLimitRE.MatchString(msg) {
		return UsageLimitError{}, false
	}
	if m := findHitLimit(msg); m != nil {
		if word := strings.ToLower(m[1]); word != "usage" {
			limit.Window = word
		}
	} else if !usageLimitReachedRE.MatchString(msg) {
		if noResetQuotaRE.MatchString(msg) {
			limit.NoReset = true
			return limit, true
		}
		return UsageLimitError{}, false
	}
	limit.ResetAt = parseResetTime(msg, now, loc)
	return limit, true
}

// findHitLimit возвращает первое «hit your <окно> limit», которое не про троттлинг (см. throttleWords).
func findHitLimit(msg string) []string {
	for _, m := range hitLimitRE.FindAllStringSubmatch(msg, -1) {
		if !throttleWords[strings.ToLower(m[1])] {
			return m
		}
	}
	return nil
}

// epochToTime читает эпоху в секундах или миллисекундах (значения > 1e12 — миллисекунды).
func epochToTime(epoch float64) time.Time {
	if epoch <= 0 {
		return time.Time{}
	}
	if epoch > 1e12 {
		return time.UnixMilli(int64(epoch)).UTC()
	}
	return time.Unix(int64(epoch), 0).UTC()
}

// parseResetTime достаёт момент сброса из текста; нулевое время — не нашли или невалидно.
func parseResetTime(msg string, now time.Time, loc *time.Location) time.Time {
	m := resetWhenRE.FindStringSubmatch(msg)
	if m == nil {
		return time.Time{}
	}
	group := func(name string) string { return m[resetWhenGroup[name]] }

	if tz := strings.TrimSpace(group("tz")); tz != "" {
		explicit, err := time.LoadLocation(tz)
		if err != nil {
			return time.Time{}
		}
		loc = explicit
	}
	hour, minute, ok := parseClock(group("hour"), group("minute"), group("ampm"))
	if !ok {
		return time.Time{}
	}
	local := now.In(loc)

	switch {
	case group("month") != "":
		return resolveDate(group("month"), group("day"), group("year"), hour, minute, local)
	case group("weekday") != "":
		return resolveWeekday(group("weekday"), hour, minute, local)
	default:
		return nextClock(hour, minute, local)
	}
}

// parseClock переводит часы и минуты в 24-часовой формат. Час без минут и am/pm («resets 3») — не время.
func parseClock(hourText, minuteText, ampm string) (hour, minute int, ok bool) {
	if minuteText == "" && ampm == "" {
		return 0, 0, false
	}
	hour, _ = strconv.Atoi(hourText)
	if minuteText != "" {
		minute, _ = strconv.Atoi(minuteText)
	}
	if minute > 59 {
		return 0, 0, false
	}
	if ampm == "" {
		return hour, minute, hour <= 23
	}
	if hour < 1 || hour > 12 {
		return 0, 0, false
	}
	hour %= 12
	if strings.ToLower(ampm)[0] == 'p' {
		hour += 12
	}
	return hour, minute, true
}

// resolveDate строит момент по явной дате; без года берёт ближайшее будущее вхождение. Несуществующая дата
// («Feb 30») даёт нулевое время.
func resolveDate(monthText, dayText, yearText string, hour, minute int, local time.Time) time.Time {
	month := months[strings.ToLower(monthText)[:3]]
	day, _ := strconv.Atoi(dayText)
	build := func(year int) time.Time {
		t := time.Date(year, month, day, hour, minute, 0, 0, local.Location())
		if t.Month() != month || t.Day() != day {
			return time.Time{}
		}
		return t
	}
	if yearText != "" {
		year, _ := strconv.Atoi(yearText)
		return build(year)
	}
	for year := local.Year(); year <= local.Year()+1; year++ {
		if t := build(year); !t.IsZero() && t.After(local) {
			return t
		}
	}
	return time.Time{}
}

// resolveWeekday возвращает ближайший будущий день недели в указанное время.
func resolveWeekday(weekdayText string, hour, minute int, local time.Time) time.Time {
	want := weekdays[strings.ToLower(weekdayText)[:3]]
	for add := 0; add <= 7; add++ {
		t := time.Date(local.Year(), local.Month(), local.Day()+add, hour, minute, 0, 0, local.Location())
		if t.Weekday() == want && t.After(local) {
			return t
		}
	}
	return time.Time{}
}

// nextClock возвращает ближайшее будущее вхождение времени суток: сегодня или завтра.
func nextClock(hour, minute int, local time.Time) time.Time {
	t := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, local.Location())
	if !t.After(local) {
		t = time.Date(local.Year(), local.Month(), local.Day()+1, hour, minute, 0, 0, local.Location())
	}
	return t
}

// agentLocation — часовой пояс процесса агента: время без зоны в его сообщениях относится к нему.
func agentLocation() *time.Location {
	tz := strings.TrimPrefix(os.Getenv("TZ"), ":")
	if tz == "" {
		return time.Local
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.Local
	}
	return loc
}

// limitInfo — rate_limit_info из rate_limit_event и api_error_params.
type limitInfo struct {
	Status        string  `json:"status"`
	ResetsAt      float64 `json:"resetsAt"`
	RateLimitType string  `json:"rateLimitType"`
	Utilization   float64 `json:"utilization"`
}

// limitStreamLine — поля строки stream-json, нужные распознавателю лимита. Полиморфные поля читаются лениво.
type limitStreamLine struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	IsError bool   `json:"is_error"`
	// ParentToolUseID непуст у событий субагента: корневой result его не имеет.
	ParentToolUseID string           `json:"parent_tool_use_id"`
	Result          json.RawMessage  `json:"result"`
	Message         json.RawMessage  `json:"message"`
	Error           json.RawMessage  `json:"error"`
	APIError        string           `json:"api_error"`
	APIErrorStatus  int              `json:"api_error_status"`
	APIErrorParams  limitErrorParams `json:"api_error_params"`
	RateLimitInfo   *limitInfo       `json:"rate_limit_info"`
}

// limitErrorParams — api_error_params ассистента и result: структурные детали отказа.
type limitErrorParams struct {
	RateLimitInfo *limitInfo `json:"rate_limit_info"`
}

const (
	apiErrorUsageLimit        = "usage_limit_reached"
	apiErrorRateLimit         = "rate_limit"
	eventTypeError            = "error"
	rateLimitStatusReject     = "rejected"
	rateLimitStatusWarning    = "allowed_warning"
	httpStatusTooManyRequests = 429
)

// usageLimitScanner собирает признаки usage-limit по строкам потока одного хода агента.
type usageLimitScanner struct {
	now func() time.Time
	loc *time.Location
	// provider и onWarning — из Config: предупреждение allowed_warning уходит в onWarning сразу, по мере чтения потока.
	provider  string
	onWarning func(LimitWarning)

	rejected   *limitInfo // последний rate_limit_event со status:"rejected"
	info       *limitInfo // rate_limit_info, приложенный к самой ошибке (приоритетнее rejected)
	hit        bool       // структурный признак лимита
	hitMessage string
	textHit    *UsageLimitError     // лимит, распознанный по тексту error-строки
	rawHit     *UsageLimitError     // лимит в не-JSON строке stdout; засчитывается только при сбое процесса
	terminal   *TerminalResultError // последний корневой result с is_error; успешный result его снимает
	progress   bool
}

func newUsageLimitScanner(now func() time.Time, cfg Config) *usageLimitScanner {
	return &usageLimitScanner{now: now, loc: agentLocation(), provider: cfg.Provider, onWarning: cfg.OnLimitWarning}
}

// limitLineMarkers отсекают строки, которые заведомо не несут признаков лимита, до разбора JSON.
var limitLineMarkers = []string{"rate_limit_event", "is_error", "api_error", eventTypeError, "assistant"}

func (s *usageLimitScanner) observe(line string) {
	if !strings.HasPrefix(line, "{") {
		if s.rawHit == nil {
			if hit, ok := parseUsageLimitText(line, s.now(), s.loc); ok {
				s.rawHit = &hit
			}
		}
		return
	}
	if !containsAny(line, limitLineMarkers) {
		return
	}
	var ev limitStreamLine
	if json.Unmarshal([]byte(line), &ev) != nil {
		return
	}
	switch ev.Type {
	case "rate_limit_event":
		s.observeRateLimitEvent(ev.RateLimitInfo)
	case eventTypeAssistant:
		s.observeAssistant(&ev)
	case streamEventResult:
		s.observeTerminal(&ev)
		s.observeResult(&ev)
	case eventTypeError:
		s.observeErrorText(rawErrorText(ev.Message, ev.Error))
	default:
		// Прочие события (system, user, api_retry…) о лимите не говорят.
	}
}

func (s *usageLimitScanner) observeRateLimitEvent(info *limitInfo) {
	if info == nil {
		return
	}
	switch info.Status {
	case rateLimitStatusReject:
		s.rejected = info
	case rateLimitStatusWarning:
		if s.onWarning != nil {
			s.onWarning(LimitWarning{
				Provider:    s.provider,
				Window:      info.RateLimitType,
				ResetAt:     epochToTime(info.ResetsAt),
				Utilization: info.Utilization,
			})
		}
	default:
		// allowed и прочие статусы о лимите не говорят.
	}
}

func (s *usageLimitScanner) observeAssistant(ev *limitStreamLine) {
	errKind := rawString(ev.Error)
	if ev.APIError == apiErrorUsageLimit || (errKind == apiErrorRateLimit && s.rejected != nil) {
		s.markHit(ev, assistantText(ev.Message))
		return
	}
	if errKind == "" && assistantHasWork(ev.Message) {
		s.progress = true
	}
}

// observeTerminal запоминает ошибочный итог корневого хода. Ошибки инструментов и субагентов приходят не как
// корневой result, а успешный result после ошибочного означает, что ход всё же завершился.
func (s *usageLimitScanner) observeTerminal(ev *limitStreamLine) {
	switch {
	case ev.ParentToolUseID != "":
		// Result субагента не описывает исход корневого хода.
	case ev.IsError:
		s.terminal = &TerminalResultError{
			Subtype:        ev.Subtype,
			Message:        strings.TrimSpace(rawString(ev.Result)),
			APIErrorStatus: ev.APIErrorStatus,
		}
	default:
		s.terminal = nil
	}
}

func (s *usageLimitScanner) observeResult(ev *limitStreamLine) {
	if !ev.IsError {
		// Ход завершился штатно: прежние признаки были промежуточными.
		s.hit, s.textHit, s.rawHit = false, nil, nil
		return
	}
	text := rawString(ev.Result)
	switch {
	case ev.APIError == apiErrorUsageLimit:
		s.markHit(ev, text)
	case s.rejected != nil && ev.APIErrorStatus == httpStatusTooManyRequests:
		s.markHit(ev, text)
	default:
		s.observeErrorText(text)
	}
}

func (s *usageLimitScanner) markHit(ev *limitStreamLine, message string) {
	s.hit = true
	if message != "" {
		s.hitMessage = message
	}
	if ev.APIErrorParams.RateLimitInfo != nil {
		s.info = ev.APIErrorParams.RateLimitInfo
	}
}

func (s *usageLimitScanner) observeErrorText(text string) {
	if text == "" || s.textHit != nil {
		return
	}
	if hit, ok := parseUsageLimitText(text, s.now(), s.loc); ok {
		s.textHit = &hit
	}
}

// finish возвращает usage-limit, которым закончился ход, либо nil. stderrTail учитывается только при сбое
// процесса (runErr != nil).
func (s *usageLimitScanner) finish(runErr error, stderrTail string) *UsageLimitError {
	var limit *UsageLimitError
	switch {
	case s.hit:
		limit = s.structuredLimit()
	case s.textHit != nil:
		limit = s.textHit
	case runErr != nil && s.rawHit != nil:
		limit = s.rawHit
	case runErr != nil:
		limit = s.stderrLimit(stderrTail)
	default:
		// Ход завершился без ошибки и без признаков лимита.
	}
	if limit == nil {
		return nil
	}
	limit.Progress = s.progress
	limit.cause = runErr
	return limit
}

// structuredLimit строит ошибку по структурным полям; момент сброса, которого в них нет, берётся из текста.
func (s *usageLimitScanner) structuredLimit() *UsageLimitError {
	limit := &UsageLimitError{Message: s.hitMessage, Source: UsageLimitSourceStructured}
	info := s.info
	if info == nil {
		info = s.rejected
	}
	if info != nil {
		limit.Window = info.RateLimitType
		limit.ResetAt = epochToTime(info.ResetsAt)
	}
	if limit.Message == "" {
		limit.Message = apiErrorUsageLimit
	}
	if limit.ResetAt.IsZero() {
		if text, ok := parseUsageLimitText(limit.Message, s.now(), s.loc); ok {
			limit.ResetAt = text.ResetAt
			limit.NoReset = text.NoReset
			if limit.Window == "" {
				limit.Window = text.Window
			}
			if !limit.ResetAt.IsZero() || limit.NoReset {
				limit.Source = UsageLimitSourceText
			}
		}
	}
	return limit
}

// stderrLimit разбирает stderr хода тем же парсером, построчно.
func (s *usageLimitScanner) stderrLimit(tail string) *UsageLimitError {
	for _, line := range strings.Split(tail, "\n") {
		if hit, ok := parseUsageLimitText(line, s.now(), s.loc); ok {
			return &hit
		}
	}
	return nil
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// rawString читает JSON-строку; значение другого типа даёт пустую строку.
func rawString(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

// rawErrorText читает текст из type:"error": строка в message либо объект error{message}.
func rawErrorText(message, errField json.RawMessage) string {
	if s := rawString(message); s != "" {
		return s
	}
	var obj struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(errField, &obj) == nil {
		return obj.Message
	}
	return ""
}

// assistantText склеивает текстовые блоки сообщения ассистента.
func assistantText(message json.RawMessage) string {
	var msg streamMessage
	if json.Unmarshal(message, &msg) != nil {
		return ""
	}
	var texts []string
	for _, c := range msg.Content {
		if c.Type == contentTypeText && c.Text != "" {
			texts = append(texts, c.Text)
		}
	}
	return strings.Join(texts, "\n")
}

// assistantHasWork сообщает, что сообщение ассистента содержит текст или вызов инструмента.
func assistantHasWork(message json.RawMessage) bool {
	var msg streamMessage
	if json.Unmarshal(message, &msg) != nil {
		return false
	}
	for _, c := range msg.Content {
		if c.Type == contentTypeToolUse || (c.Type == contentTypeText && c.Text != "") {
			return true
		}
	}
	return false
}

// usageLimitError возвращает usage-limit, которым закончился ход, или nil. Остановка пользователем, отмена
// контекста, idle timeout и сбой старта — не лимит и остаются как есть.
func (e *Executor) usageLimitError(sc *usageLimitScanner, runErr error, logFile string, stderrFrom int64) *UsageLimitError {
	if errors.Is(runErr, ErrUserInterrupted) || errors.Is(runErr, ErrIdleTimeout) || errors.Is(runErr, ErrAgentStart) ||
		errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
		return nil
	}
	tail := ""
	if runErr != nil {
		tail = ReadStderrTailFrom(logFile, stderrFrom, stderrTailLines, stderrTailBytes)
	}
	limit := sc.finish(runErr, tail)
	if limit != nil {
		limit.Provider = e.cfg.Provider
	}
	return limit
}

// turnOutcome — единый исход хода для лога, accounting и вызывающего: usage-limit приоритетнее прочего, остановка
// и ошибка запуска остаются как есть, а ошибочный корневой result при exit 0 становится *TerminalResultError.
func (e *Executor) turnOutcome(sc *usageLimitScanner, runErr error, logFile string, stderrFrom int64) error {
	if limit := e.usageLimitError(sc, runErr, logFile, stderrFrom); limit != nil {
		return limit
	}
	if runErr == nil && sc.terminal != nil {
		return sc.terminal
	}
	return runErr
}

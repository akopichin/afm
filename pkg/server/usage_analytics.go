package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/akopichin/afm/contracts/accounting"
)

// UsageAnalyticsSource is the read model behind GET /api/usage/analytics; *accounting.Analytics satisfies it.
type UsageAnalyticsSource interface {
	Facts() *accounting.Facts
}

// UsageModeFor maps the accounting display switches to the analytics mode. Hiding accounting leaves the
// agent time, which is measured regardless of accounting.
func UsageModeFor(accountingEnabled, showMoney bool) UsageAnalyticsMode {
	switch {
	case !accountingEnabled:
		return UsageModeTimeOnly
	case showMoney:
		return UsageModeFull
	default:
		return UsageModeNoMoney
	}
}

// usageModeOrClosed keeps an unset or unknown mode from exposing more than the least permissive one.
func usageModeOrClosed(mode UsageAnalyticsMode) UsageAnalyticsMode {
	switch mode {
	case UsageModeFull, UsageModeNoMoney, UsageModeTimeOnly:
		return mode
	default:
		return UsageModeTimeOnly
	}
}

const (
	usageDefaultLimit = 10
	usageMaxLimit     = 100
	usageTimelineMax  = 2000
	usageMaxTextLen   = 256
)

// usageParams is a validated request.
type usageParams struct {
	view     UsageView
	group    UsageGroup
	scope    UsageScope
	stageID  string
	agent    string
	model    string
	sort     UsageSort
	dir      UsageDir
	limit    int // jobs view only
	cursor   string
	focusJob string
}

// usageRequestError is a rejected request: invalid_param (400) or stale_cursor (409).
type usageRequestError struct {
	code    UsageErrorCode
	message string
}

func (e *usageRequestError) Error() string { return e.message }

func invalidParam(format string, args ...any) error {
	return &usageRequestError{code: UsageErrorInvalidParam, message: fmt.Sprintf(format, args...)}
}

func staleCursor() error {
	return &usageRequestError{code: UsageErrorStaleCursor, message: "cursor was issued for another revision, filter, sort or mode"}
}

var usageParamNames = []string{
	"view", "group", "scope", "stage_id", "agent", "model", "sort", "dir", "limit", "cursor", "focus_job",
}

func (s *Server) handleUsageAnalytics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	resp, err := s.usageResponse(r.URL.Query())
	if err != nil {
		writeUsageError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func writeUsageError(w http.ResponseWriter, err error) {
	reqErr, ok := err.(*usageRequestError) //nolint:errorlint // only this file creates the type, never wrapped
	if !ok {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(reqErr.code.HTTPStatus())
	_ = json.NewEncoder(w).Encode(UsageAnalyticsError{Code: reqErr.code, Message: reqErr.message})
}

func (s *Server) usageResponse(values url.Values) (UsageAnalytics, error) {
	p, err := parseUsageParams(values, s.usageMode)
	if err != nil {
		return UsageAnalytics{}, err
	}
	facts := s.usageAnalytics.Facts()
	revision := facts.Revision()

	// A cursor pins as_of: pages of one walk are ordered by durations measured at the same instant.
	asOf := s.usageNow().UTC().Truncate(time.Millisecond)
	var offset int
	if p.cursor != "" {
		c, err := p.openCursor(revision, s.usageMode)
		if err != nil {
			return UsageAnalytics{}, err
		}
		asOf, offset = c.asOf, c.offset
	}

	sel := facts.Select(accounting.Filter{
		Scope: accounting.FilterScope(p.scope), StageID: p.stageID, Agent: p.agent, Model: p.model,
	}, asOf)

	resp := UsageAnalytics{
		View:         p.view,
		AsOf:         asOf,
		Revision:     revision,
		Health:       usageHealth(facts.Health()),
		Completeness: usageCompleteness(sel.Completeness()),
		Query:        p.echo(),
		Totals:       usageMetrics(sel.Totals()),
		Catalog:      usageCatalog(facts.Catalog()),
	}
	if outside := sel.Outside(); outside != nil {
		m := usageMetrics(*outside)
		resp.OutsideRunTotal = &m
	}

	sortKey, desc := accounting.SortKey(p.sort), p.dir == UsageDirDesc
	switch p.view {
	case UsageViewBreakdown:
		resp.Breakdown = &UsageBreakdown{Group: p.group, Rows: usageRows(sel.Rows(accounting.Group(p.group), sortKey, desc))}
	case UsageViewCharts:
		resp.Charts = &UsageCharts{
			Group:      p.group,
			Comparison: usageRows(sel.Rows(accounting.Group(p.group), sortKey, desc)),
			Timeline:   usageTimeline(sel.Timeline(usageTimelineMax)),
		}
	default: // UsageViewJobs: the parser admits no other view
		page, err := s.usageJobsPage(facts, sel, p, revision, asOf, offset)
		if err != nil {
			return UsageAnalytics{}, err
		}
		resp.Jobs = page
	}
	return resp.ForMode(s.usageMode), nil
}

func (s *Server) usageJobsPage(facts *accounting.Facts, sel *accounting.Selection, p usageParams, revision string, asOf time.Time, offset int) (*UsageJobsPage, error) {
	items := sel.Jobs(accounting.SortKey(p.sort), p.dir == UsageDirDesc)
	total := len(items)

	var focus *UsageFocus
	if p.focusJob != "" {
		focus = &UsageFocus{Status: UsageFocusNotFound, JobID: p.focusJob}
		if i := slices.IndexFunc(items, func(j accounting.JobItem) bool { return j.ID == p.focusJob }); i >= 0 {
			focus.Status, focus.Index = UsageFocusFound, &i
			offset = i / p.limit * p.limit
		} else if everything := facts.Select(accounting.Filter{}, asOf).Jobs(accounting.SortStarted, true); slices.ContainsFunc(everything, func(j accounting.JobItem) bool { return j.ID == p.focusJob }) {
			focus.Status = UsageFocusFilteredOut
		}
	}
	if offset > total {
		return nil, staleCursor()
	}

	end := min(offset+p.limit, total)
	page := &UsageJobsPage{Items: make([]UsageJob, 0, end-offset), Total: total, Limit: p.limit, Focus: focus}
	for _, item := range items[offset:end] {
		page.Items = append(page.Items, usageJob(item))
	}
	if end < total {
		page.NextCursor = p.sealCursor(revision, s.usageMode, usageCursor{offset: end, asOf: asOf})
	}
	if offset > 0 {
		page.PrevCursor = p.sealCursor(revision, s.usageMode, usageCursor{offset: max(offset-p.limit, 0), asOf: asOf})
	}
	return page, nil
}

// echo is the normalized request the response answers.
func (p usageParams) echo() UsageQuery {
	q := UsageQuery{
		View: p.view, Group: p.group, Scope: p.scope, StageID: p.stageID, Agent: p.agent, Model: p.model,
		Sort: p.sort, Dir: p.dir,
	}
	if p.view == UsageViewJobs {
		q.Limit = p.limit
	}
	return q
}

func parseUsageParams(values url.Values, mode UsageAnalyticsMode) (usageParams, error) {
	for name, vals := range values {
		switch {
		case !slices.Contains(usageParamNames, name):
			return usageParams{}, invalidParam("unknown parameter %.40q", name)
		case len(vals) != 1:
			return usageParams{}, invalidParam("parameter %s is repeated", name)
		case vals[0] == "":
			return usageParams{}, invalidParam("parameter %s is empty; omit it instead", name)
		}
	}

	var p usageParams
	var err error
	if p.view, err = enumParam(values, "view", UsageViewBreakdown, UsageViewJobs, UsageViewCharts); err != nil {
		return p, err
	}
	if p.group, err = enumParam(values, "group", UsageGroupModel, UsageGroupAgent); err != nil {
		return p, err
	}
	if p.scope, err = enumParam(values, "scope", UsageScopeAll, UsageScopeStage, UsageScopeRunWide, UsageScopeUnattributed); err != nil {
		return p, err
	}
	for name, dst := range map[string]*string{"stage_id": &p.stageID, "agent": &p.agent, "model": &p.model, "focus_job": &p.focusJob} {
		if *dst, err = textParam(values, name, usageMaxTextLen); err != nil {
			return p, err
		}
	}
	if p.cursor, err = textParam(values, "cursor", usageMaxTextLen); err != nil {
		return p, err
	}
	if err = p.checkScope(); err != nil {
		return p, err
	}
	if err = p.checkPaging(values); err != nil {
		return p, err
	}
	return p, p.resolveOrder(values, mode)
}

func (p *usageParams) checkScope() error {
	switch {
	case p.scope == UsageScopeStage && p.stageID == "":
		return invalidParam("scope=stage needs stage_id")
	case p.scope != UsageScopeStage && p.stageID != "":
		return invalidParam("stage_id is only valid with scope=stage")
	}
	return nil
}

func (p *usageParams) checkPaging(values url.Values) error {
	if p.view != UsageViewJobs {
		for _, name := range []string{"limit", "cursor", "focus_job"} {
			if values.Has(name) {
				return invalidParam("%s is only valid for view=jobs", name)
			}
		}
		return nil
	}
	if p.cursor != "" && p.focusJob != "" {
		return invalidParam("cursor and focus_job are mutually exclusive")
	}
	p.limit = usageDefaultLimit
	if values.Has("limit") {
		n, err := strconv.Atoi(values.Get("limit"))
		if err != nil || n < 1 || n > usageMaxLimit {
			return invalidParam("limit must be an integer from 1 to %d", usageMaxLimit)
		}
		p.limit = n
	}
	return nil
}

// resolveOrder applies the sort and direction defaults and rejects a sort the view or mode cannot serve.
func (p *usageParams) resolveOrder(values url.Values, mode UsageAnalyticsMode) error {
	var allowed []UsageSort
	p.sort = defaultUsageSort(mode)
	if p.view == UsageViewJobs {
		allowed = []UsageSort{UsageSortCost, UsageSortTokens, UsageSortTime, UsageSortName, UsageSortModel, UsageSortStarted}
		p.sort = UsageSortStarted
	} else {
		allowed = []UsageSort{UsageSortCost, UsageSortTokens, UsageSortTime, UsageSortJobs, UsageSortName}
	}
	var err error
	if p.sort, err = enumParam(values, "sort", p.sort, allowed...); err != nil {
		return err
	}
	if !mode.AllowsSort(p.sort) {
		return invalidParam("sort=%s is not available in mode %s", p.sort, mode)
	}
	defaultDir := UsageDirDesc
	if p.sort == UsageSortName || p.sort == UsageSortModel {
		defaultDir = UsageDirAsc
	}
	p.dir, err = enumParam(values, "dir", defaultDir, UsageDirAsc, UsageDirDesc)
	return err
}

// defaultUsageSort orders rows by the most telling metric the mode has.
func defaultUsageSort(mode UsageAnalyticsMode) UsageSort {
	switch {
	case mode.HasMoney():
		return UsageSortCost
	case mode.HasTokens():
		return UsageSortTokens
	default:
		return UsageSortTime
	}
}

// enumParam reads a parameter that must be def or one of others; def is used when it is absent.
func enumParam[T ~string](values url.Values, name string, def T, others ...T) (T, error) {
	raw := values.Get(name)
	if raw == "" {
		return def, nil
	}
	if v := T(raw); v == def || slices.Contains(others, v) {
		return v, nil
	}
	return def, invalidParam("invalid %s %.40q", name, raw)
}

// textParam reads a value that is an ID or key. It is only ever compared, never used as a path.
func textParam(values url.Values, name string, maxLen int) (string, error) {
	v := values.Get(name)
	if len(v) > maxLen || !utf8.ValidString(v) || slices.ContainsFunc([]rune(v), unicode.IsControl) {
		return "", invalidParam("invalid %s", name)
	}
	return v, nil
}

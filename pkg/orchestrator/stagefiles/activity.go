package stagefiles

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/akopichin/afm/pkg/orchestrator/bus"
)

// Размер страницы истории активности: по умолчанию и максимум.
const (
	DefaultActivityLimit = 100
	MaxActivityLimit     = 200
)

// Пределы текста одной записи: publisher обрезает до них, а читатель рассчитывает на них размер строки.
const (
	MaxActivityDetailBytes = 16 * 1024
	MaxActivityToolBytes   = 256
)

// activityLineBuffer — самая длинная читаемая строка: каждый байт detail и tool после JSON-экранирования до шести
// байт, плюс запас на остальные поля. Более длинная строка не может быть записью активности и пропускается.
const activityLineBuffer = 6*(MaxActivityDetailBytes+MaxActivityToolBytes) + (4 << 10)

// ErrInvalidCursor — курсор не лежит на границе строки notices.jsonl либо заданы оба направления.
var ErrInvalidCursor = errors.New("stagefiles: invalid activity cursor")

// ActivityData — payload одного notice assistant_activity; его пишет orchestrator и читает история.
type ActivityData struct {
	EventID       string `json:"event_id"`
	TaskID        string `json:"task_id"`
	ParticipantID string `json:"participant_id"`
	Turn          uint64 `json:"turn"`
	OccurredAt    string `json:"occurred_at"`
	Tool          string `json:"tool"`
	Detail        string `json:"detail"`
	Truncated     bool   `json:"truncated,omitempty"`
}

// ActivityEntry — запись истории. Cursor — смещение конца её строки: after=Cursor продолжает чтение сразу за
// записью.
type ActivityEntry struct {
	ActivityData
	StageID string `json:"stage_id,omitempty"`
	Cursor  uint64 `json:"cursor,string"`
}

// ActivityQuery выбирает страницу: без курсора — хвост, After — вперёд, Before — назад. Оба сразу недопустимы.
type ActivityQuery struct {
	Limit  int
	After  *uint64
	Before *uint64
}

// ActivityPage — записи по возрастанию позиции в файле. After — откуда продолжать чтение вперёд, Before — где
// начинается первая запись (0 для пустой страницы). HasOlder считается для хвоста и Before-страницы, HasMore —
// только для After-страницы; противоположное поле остаётся false.
type ActivityPage struct {
	Entries  []ActivityEntry `json:"entries"`
	After    uint64          `json:"after,string"`
	Before   uint64          `json:"before,string"`
	HasMore  bool            `json:"has_more"`
	HasOlder bool            `json:"has_older"`
}

// activityTypeMarker — как тип assistant_activity выглядит в строке notices.jsonl; дешёвая проверка байтов
// избавляет остальные строки от разбора JSON.
var activityTypeMarker = []byte(`"` + string(bus.EventAssistantActivity) + `"`)

// IsActivityLine сообщает, что строка notices.jsonl — notice assistant_activity любой задачи. Это единственное
// место правила «строка — активность помощника»: им пользуются и история, и replay событий в сервере.
func IsActivityLine(line []byte) bool {
	if !bytes.Contains(line, activityTypeMarker) {
		return false
	}
	var head struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(line, &head) == nil && head.Type == string(bus.EventAssistantActivity)
}

// activityMatcher выбирает из notices.jsonl записи одной задачи.
type activityMatcher struct {
	taskID string
	// needle — task_id в том виде, в каком его пишет json.Marshal: по нему чужие задачи отсекаются без разбора
	// JSON. Кавычки внутри detail в строке экранированы, поэтому needle не найдётся в тексте записи.
	needle []byte
}

func newActivityMatcher(taskID string) activityMatcher {
	quoted, _ := json.Marshal(taskID) // строка всегда кодируется
	return activityMatcher{taskID: taskID, needle: append([]byte(`"task_id":`), quoted...)}
}

// ReadTaskActivity читает историю активности одной задачи из <runDir>/notices.jsonl. Учитываются только строки,
// законченные переводом строки, поэтому чтение безопасно рядом с AppendNotice, а курсор не уходит за недописанный
// хвост. Битые и слишком длинные строки пропускаются. Файла нет — это пустая история.
func ReadTaskActivity(runDir, taskID string, q ActivityQuery) (ActivityPage, error) {
	if q.After != nil && q.Before != nil {
		return ActivityPage{}, ErrInvalidCursor
	}
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultActivityLimit
	}
	limit = min(limit, MaxActivityLimit)

	var src io.ReaderAt = bytes.NewReader(nil)
	var size int64
	if f, err := os.Open(filepath.Join(runDir, "notices.jsonl")); err == nil {
		defer f.Close()
		fi, err := f.Stat()
		if err != nil {
			return ActivityPage{}, err
		}
		src, size = f, fi.Size()
	} else if !errors.Is(err, os.ErrNotExist) {
		return ActivityPage{}, err
	}

	match := newActivityMatcher(taskID)
	switch {
	case q.After != nil:
		if err := checkCursor(src, size, *q.After); err != nil {
			return ActivityPage{}, err
		}
		return readForward(src, size, match, int64(*q.After), limit)
	case q.Before != nil:
		if err := checkCursor(src, size, *q.Before); err != nil {
			return ActivityPage{}, err
		}
		page, _, err := readNewest(src, match, int64(*q.Before), limit)
		page.After = *q.Before
		return page, err
	default:
		page, lastEnd, err := readNewest(src, match, size, limit)
		page.After = uint64(lastEnd)
		return page, err
	}
}

// checkCursor принимает только 0 или смещение сразу после перевода строки внутри файла.
func checkCursor(src io.ReaderAt, size int64, cursor uint64) error {
	if cursor > uint64(size) {
		return ErrInvalidCursor
	}
	if cursor == 0 {
		return nil
	}
	var prev [1]byte
	if _, err := src.ReadAt(prev[:], int64(cursor)-1); err != nil || prev[0] != '\n' {
		return ErrInvalidCursor
	}
	return nil
}

// readForward собирает до limit записей после from. HasMore ставится, только если за последней записью есть ещё
// одна запись задачи; тогда After остаётся сразу за последней возвращённой записью.
func readForward(src io.ReaderAt, size int64, match activityMatcher, from int64, limit int) (ActivityPage, error) {
	page := ActivityPage{Entries: []ActivityEntry{}}
	var firstStart int64
	lastEnd, err := scanLines(src, from, size, func(start, end int64, line []byte) bool {
		entry, ok := match.decode(line, end)
		if !ok {
			return true
		}
		if len(page.Entries) == limit {
			page.HasMore = true
			return false
		}
		if len(page.Entries) == 0 {
			firstStart = start
		}
		page.Entries = append(page.Entries, entry)
		return true
	})
	if page.HasMore {
		lastEnd = int64(page.Entries[len(page.Entries)-1].Cursor)
	}
	page.After, page.Before = uint64(lastEnd), uint64(firstStart)
	return page, err
}

// readNewest возвращает последние limit записей в [0, to) в порядке файла и конец последней полной строки.
func readNewest(src io.ReaderAt, match activityMatcher, to int64, limit int) (ActivityPage, int64, error) {
	type placed struct {
		entry ActivityEntry
		start int64
	}
	ring := make([]placed, 0, min(limit, 16))
	oldest := 0 // индекс самой старой записи, когда кольцо заполнено
	dropped := false
	lastEnd, err := scanLines(src, 0, to, func(start, end int64, line []byte) bool {
		entry, ok := match.decode(line, end)
		if !ok {
			return true
		}
		switch {
		case len(ring) < limit:
			ring = append(ring, placed{entry, start})
		default:
			ring[oldest] = placed{entry, start}
			oldest = (oldest + 1) % limit
			dropped = true
		}
		return true
	})

	page := ActivityPage{Entries: make([]ActivityEntry, 0, len(ring)), HasOlder: dropped}
	for i := range ring {
		p := ring[(oldest+i)%len(ring)]
		if i == 0 {
			page.Before = uint64(p.start)
		}
		page.Entries = append(page.Entries, p.entry)
	}
	return page, lastEnd, err
}

// decode разбирает строку как запись активности своей задачи; строка без event_id не считается записью.
func (m activityMatcher) decode(line []byte, end int64) (ActivityEntry, bool) {
	if !bytes.Contains(line, activityTypeMarker) || !bytes.Contains(line, m.needle) {
		return ActivityEntry{}, false
	}
	var rec struct {
		Type    string       `json:"type"`
		StageID string       `json:"stage_id"`
		Data    ActivityData `json:"data"`
	}
	if json.Unmarshal(line, &rec) != nil || rec.Type != string(bus.EventAssistantActivity) ||
		rec.Data.TaskID != m.taskID || rec.Data.EventID == "" {
		return ActivityEntry{}, false
	}
	return ActivityEntry{ActivityData: rec.Data, StageID: rec.StageID, Cursor: uint64(end)}, true
}

// scanLines обходит законченные строки src в [from, to). Возвращает конец последней законченной строки (from,
// если таких нет). Недописанный хвост не читается; visit, вернув false, останавливает обход после своей строки.
func scanLines(src io.ReaderAt, from, to int64, visit func(start, end int64, line []byte) bool) (int64, error) {
	r := bufio.NewReaderSize(io.NewSectionReader(src, from, to-from), activityLineBuffer)
	pos := from
	for {
		start := pos
		line, err := r.ReadSlice('\n')
		length, overlong := int64(len(line)), false
		for errors.Is(err, bufio.ErrBufferFull) {
			overlong = true
			line, err = r.ReadSlice('\n')
			length += int64(len(line))
		}
		if errors.Is(err, io.EOF) {
			return pos, nil // строка без перевода строки ещё пишется
		}
		if err != nil {
			return pos, err
		}
		pos = start + length
		if !overlong && !visit(start, pos, line) {
			return pos, nil
		}
	}
}

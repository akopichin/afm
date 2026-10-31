package agentjobs

import (
	"bytes"
	"encoding/json"
	"slices"
	"time"
)

// HealthStatus says how far the journal can be trusted.
type HealthStatus string

const (
	// HealthOK: every line was understood.
	HealthOK HealthStatus = "ok"
	// HealthDegraded: records are readable but some facts were skipped or refused.
	HealthDegraded HealthStatus = "degraded"
	// HealthUnavailable: the journal is missing, unreadable or no longer writable.
	HealthUnavailable HealthStatus = "unavailable"
)

// Machine reasons behind a Health status.
const (
	ReasonNoJournal          = "no_journal"
	ReasonMissingHeader      = "missing_header"
	ReasonCorruptHeader      = "corrupt_header"
	ReasonUnsupportedVersion = "unsupported_version"
	ReasonIOFailed           = "io_failed"
	ReasonCorruptLine        = "corrupt_line"
	ReasonConflictingRecord  = "conflicting_record"
)

var unavailableReasons = map[string]bool{
	ReasonNoJournal: true, ReasonMissingHeader: true, ReasonCorruptHeader: true,
	ReasonUnsupportedVersion: true, ReasonIOFailed: true,
}

// Health is a journal's status with the sorted machine reasons behind it.
type Health struct {
	Status  HealthStatus
	Reasons []string
}

// Snapshot is an immutable view of the journal. Records are in file order, without the header.
type Snapshot struct {
	Header  Header
	Records []Record
	// Revision grows with every accepted record and every new health reason, never otherwise.
	Revision uint64
	Health   Health
}

// journal is the in-memory image of the file. Replay and Append share its rules.
type journal struct {
	header  Header
	records []Record
	// facts holds the stored form of each accepted fact, to tell repeats from conflicts.
	facts   map[factKey]string
	reasons map[string]bool
}

type factKey struct {
	typ   RecordType
	jobID string
}

type verdict int

const (
	verdictNew verdict = iota
	verdictRepeat
	verdictConflict
)

func newJournal() *journal {
	return &journal{facts: map[factKey]string{}, reasons: map[string]bool{}}
}

// classify compares rec (in stored form line) with what is already accepted.
// A job either started or failed to start, never both.
func (j *journal) classify(rec Record, line string) verdict {
	if prev, ok := j.facts[factKey{rec.Type, rec.JobID}]; ok {
		if prev == line {
			return verdictRepeat
		}
		return verdictConflict
	}
	if rec.Type != TypeFinished {
		opposite := TypeStartFailed
		if rec.Type == TypeStartFailed {
			opposite = TypeStarted
		}
		if _, ok := j.facts[factKey{opposite, rec.JobID}]; ok {
			return verdictConflict
		}
	}
	return verdictNew
}

func (j *journal) add(rec Record, line string) {
	j.records = append(j.records, rec)
	j.facts[factKey{rec.Type, rec.JobID}] = line
}

func (j *journal) health() Health {
	reasons := make([]string, 0, len(j.reasons))
	status := HealthOK
	for reason := range j.reasons {
		reasons = append(reasons, reason)
		if unavailableReasons[reason] {
			status = HealthUnavailable
		} else if status == HealthOK {
			status = HealthDegraded
		}
	}
	slices.Sort(reasons)
	return Health{Status: status, Reasons: reasons}
}

// snapshot shares the append-only records without copying: capping the capacity makes a
// later append (here or by the caller) write outside the snapshot's window.
func (j *journal) snapshot() Snapshot {
	n := len(j.records)
	return Snapshot{
		Header:   j.header,
		Records:  j.records[:n:n],
		Revision: uint64(n + len(j.reasons)),
		Health:   j.health(),
	}
}

// replay rebuilds the journal from file content and returns the offset just past the last
// complete line. A final line without "\n" was never committed and is ignored. It never writes.
func replay(data []byte) (*journal, int) {
	j := newJournal()
	pos, sawHeader := 0, false
	for {
		end := bytes.IndexByte(data[pos:], '\n')
		if end < 0 {
			break
		}
		line := data[pos : pos+end]
		pos += end + 1
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if !sawHeader {
			sawHeader = true
			if reason := j.readHeader(line); reason != "" {
				j.reasons[reason] = true
				return j, pos
			}
			continue
		}
		j.replayRecord(line)
	}
	if !sawHeader {
		j.reasons[ReasonMissingHeader] = true
	}
	return j, pos
}

// readHeader parses the first line and returns the reason it is unusable, or "".
func (j *journal) readHeader(line []byte) string {
	var h Header
	if err := json.Unmarshal(line, &h); err != nil {
		return ReasonCorruptHeader
	}
	if h.Type != TypeHeader {
		return ReasonMissingHeader
	}
	if h.RecordVersion <= 0 || h.RecordingStartedAt.IsZero() || h.BaseOffsetMS < 0 {
		return ReasonCorruptHeader
	}
	if h.RecordVersion != RecordVersion {
		return ReasonUnsupportedVersion
	}
	h.RecordingStartedAt = h.RecordingStartedAt.UTC()
	j.header = h
	return ""
}

func (j *journal) replayRecord(line []byte) {
	var rec Record
	if err := json.Unmarshal(line, &rec); err != nil {
		j.reasons[ReasonCorruptLine] = true
		return
	}
	rec = rec.normalized()
	canonical, err := json.Marshal(rec)
	if err != nil || rec.validate() != nil {
		j.reasons[ReasonCorruptLine] = true
		return
	}
	if v := j.classify(rec, string(canonical)); v == verdictNew {
		j.add(rec, string(canonical))
	} else if v == verdictConflict {
		j.reasons[ReasonConflictingRecord] = true
	}
}

func newHeader(recordingStartedAt time.Time, baseOffsetMS int64) Header {
	return Header{
		Type:               TypeHeader,
		RecordVersion:      RecordVersion,
		RecordingStartedAt: recordingStartedAt.UTC(),
		BaseOffsetMS:       baseOffsetMS,
	}
}

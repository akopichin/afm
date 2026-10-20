package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

type RunBinding struct {
	Key              RunKey    `json:"key"`
	DashboardPort    int       `json:"dashboard_port"`
	StartedAt        time.Time `json:"started_at"`
	OptionalAFMRunID string    `json:"afm_run_id,omitempty"`
	LastSeenAt       time.Time `json:"last_seen_at"`
	Label            string    `json:"label"`
	Offline          bool      `json:"offline,omitempty"`
	Invalidated      bool      `json:"invalidated,omitempty"`
	Completed        bool      `json:"completed,omitempty"`
}

type ComposerContext struct {
	Mode           string    `json:"mode"`
	RunKey         RunKey    `json:"run_key"`
	StageID        string    `json:"stage_id,omitempty"`
	QuestionPhase  string    `json:"question_phase,omitempty"`
	QuestionID     string    `json:"question_id,omitempty"`
	QuestionDigest string    `json:"question_digest,omitempty"`
	StageUpdatedAt time.Time `json:"stage_updated_at,omitempty"`
	ThreadID       string    `json:"thread_id,omitempty"`
	InReplyTo      string    `json:"in_reply_to,omitempty"`
	MessageKind    string    `json:"message_kind,omitempty"`
	Interrupt      bool      `json:"interrupt,omitempty"`
	RecipientIDs   []string  `json:"recipient_ids,omitempty"`
	ProfileID      string    `json:"profile_id,omitempty"`
	Scope          string    `json:"scope,omitempty"`
	HelperMode     string    `json:"helper_mode,omitempty"`
	Instruction    string    `json:"instruction,omitempty"`
	FirstMessage   string    `json:"first_message,omitempty"`
	Draft          string    `json:"draft,omitempty"`
}

type UserSession struct {
	Selected   RunKey           `json:"selected,omitempty"`
	Filter     string           `json:"filter,omitempty"`
	Recipients []string         `json:"recipients,omitempty"`
	Composer   *ComposerContext `json:"composer,omitempty"`
}

type CallbackRecord struct {
	UserID    int64     `json:"user_id"`
	Intent    Intent    `json:"intent"`
	Consumed  bool      `json:"consumed,omitempty"`
	Result    string    `json:"result,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type Outbound struct {
	ID              string                   `json:"id"`
	OwnerUserID     int64                    `json:"owner_user_id,omitempty"`
	RunKey          RunKey                   `json:"run_key"`
	Kind            string                   `json:"kind"`
	RequestID       string                   `json:"request_id"`
	Text            string                   `json:"text"`
	StageID         string                   `json:"stage_id,omitempty"`
	ThreadID        string                   `json:"thread_id,omitempty"`
	InReplyTo       string                   `json:"in_reply_to,omitempty"`
	MessageKind     string                   `json:"message_kind,omitempty"`
	Interrupt       bool                     `json:"interrupt,omitempty"`
	RecipientIDs    []string                 `json:"recipient_ids,omitempty"`
	ProfileID       string                   `json:"profile_id,omitempty"`
	Scope           string                   `json:"scope,omitempty"`
	Mode            string                   `json:"mode,omitempty"`
	Instruction     string                   `json:"instruction,omitempty"`
	FirstMessage    string                   `json:"first_message,omitempty"`
	Status          ReceiptStatus            `json:"status"`
	Attempted       bool                     `json:"attempted,omitempty"`
	NotifiedState   string                   `json:"notified_state,omitempty"`
	AFMMessageID    string                   `json:"afm_message_id,omitempty"`
	RecipientStatus map[string]ReceiptStatus `json:"recipient_status,omitempty"`
	FailureReason   string                   `json:"failure_reason,omitempty"`
}

type PendingAction struct {
	ID             string     `json:"id"`
	OwnerUserID    int64      `json:"owner_user_id"`
	RunKey         RunKey     `json:"run_key"`
	StageID        string     `json:"stage_id"`
	Kind           IntentKind `json:"kind"`
	QuestionID     string     `json:"question_id,omitempty"`
	QuestionPhase  string     `json:"question_phase,omitempty"`
	QuestionDigest string     `json:"question_digest,omitempty"`
	Answer         string     `json:"answer,omitempty"`
	ExpectedStatus string     `json:"expected_status,omitempty"`
}

type State struct {
	Runs           map[RunKey]RunBinding      `json:"runs"`
	Users          map[int64]UserSession      `json:"users"`
	Callbacks      map[string]CallbackRecord  `json:"callbacks"`
	Outbox         map[string]Outbound        `json:"outbox"`
	PendingActions map[string]PendingAction   `json:"pending_actions"`
	ProcessedText  map[int64]bool             `json:"processed_text"`
	UpdateOffset   int64                      `json:"update_offset"`
	Cursors        map[string]uint64          `json:"cursors"`
	Seen           map[string]bool            `json:"seen"`
	ThreadCache    map[string][]ThreadMessage `json:"thread_cache"`
	PlanCache      map[string]string          `json:"plan_cache"`
	PlanUpdated    map[string]time.Time       `json:"plan_updated"`
	QuestionCache  map[string]QuestionCard    `json:"question_cache"`
	RosterCache    map[RunKey][]AgentCard     `json:"roster_cache"`
}

func (s *State) init() {
	if s.Runs == nil {
		s.Runs = map[RunKey]RunBinding{}
	}
	if s.Users == nil {
		s.Users = map[int64]UserSession{}
	}
	if s.Callbacks == nil {
		s.Callbacks = map[string]CallbackRecord{}
	}
	if s.Outbox == nil {
		s.Outbox = map[string]Outbound{}
	}
	if s.PendingActions == nil {
		s.PendingActions = map[string]PendingAction{}
	}
	if s.ProcessedText == nil {
		s.ProcessedText = map[int64]bool{}
	}
	if s.Cursors == nil {
		s.Cursors = map[string]uint64{}
	}
	if s.Seen == nil {
		s.Seen = map[string]bool{}
	}
	if s.ThreadCache == nil {
		s.ThreadCache = map[string][]ThreadMessage{}
	}
	if s.PlanCache == nil {
		s.PlanCache = map[string]string{}
	}
	if s.PlanUpdated == nil {
		s.PlanUpdated = map[string]time.Time{}
	}
	if s.QuestionCache == nil {
		s.QuestionCache = map[string]QuestionCard{}
	}
	if s.RosterCache == nil {
		s.RosterCache = map[RunKey][]AgentCard{}
	}
}

type Store struct {
	mu    sync.Mutex
	path  string
	lock  *os.File
	state State
}

func OpenStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, fmt.Errorf("bot store already owned: %w", err)
	}
	s := &Store{path: path, lock: lock}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		s.Close()
		return nil, err
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &s.state); err != nil {
			s.Close()
			return nil, err
		}
	}
	s.state.init()
	return s, nil
}
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lock == nil {
		return nil
	}
	_ = syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
	err := s.lock.Close()
	s.lock = nil
	return err
}
func (s *Store) Snapshot() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, _ := json.Marshal(s.state)
	var out State
	_ = json.Unmarshal(data, &out)
	out.init()
	return out
}
func (s *Store) Run(key RunKey) (RunBinding, bool) {
	st := s.Snapshot()
	r, ok := st.Runs[key]
	return r, ok
}
func (s *Store) Update(fn func(*State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lock == nil {
		return errors.New("bot store is closed")
	}
	before, err := json.Marshal(s.state)
	if err != nil {
		return err
	}
	var next State
	if err := json.Unmarshal(before, &next); err != nil {
		return err
	}
	next.init()
	if err = fn(&next); err != nil {
		return err
	}
	renamed, err := s.save(next)
	if err == nil || renamed {
		// After rename, disk may contain next even if directory fsync fails.
		// Keep memory aligned with the visible file and report the error.
		s.state = next
	}
	return err
}
func (s *Store) save(next State) (bool, error) {
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return false, err
	}
	file, err := os.CreateTemp(filepath.Dir(s.path), ".telegram-bot-*")
	if err != nil {
		return false, err
	}
	tmp := file.Name()
	defer os.Remove(tmp)
	if err = file.Chmod(0600); err != nil {
		file.Close()
		return false, err
	}
	if _, err = file.Write(append(data, '\n')); err != nil {
		file.Close()
		return false, err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return false, err
	}
	if err = file.Close(); err != nil {
		return false, err
	}
	if err = os.Rename(tmp, s.path); err != nil {
		return false, err
	}
	dir, err := os.Open(filepath.Dir(s.path))
	if err != nil {
		return true, err
	}
	defer dir.Close()
	return true, dir.Sync()
}
func randomID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

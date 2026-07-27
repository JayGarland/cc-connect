package core

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// topicSeatBinding remembers which seat created a Telegram Forum Topic in
// direct response to an explicit @mention of that seat's own bot username
// (handleGeneralTopicIntake) — a Topic that was never registered in the
// [DISPATCH] ledger (dispatchStore / DispatchExpectation) because it has no
// associated letter at all.
//
// This is deliberately separate from both dispatchStore
// (dispatch_expectations.json, which requires Letter/Path/ResultPath fields
// that make no sense for an ad hoc, non-letter Topic — see dispatch.go) and
// topicLetterBindingStore (topic_letter_bindings.json, which resolves
// {{LETTER_ID}} workspace-pattern substitution, a different concern — see
// topic_letter_binding.go). Its one job: TopicID -> the single seat that
// created it, so isDirectedAtBot's bare-text fallback (via
// Engine.isTopicBoundToSeat) can recognize ownership of a Topic that was
// never [DISPATCH]-registered (L-0669 follow-up: General-topic-intake
// Topics were falling through to "not directed at me" for every message
// after the one that triggered topic creation, since nothing durable
// recorded which seat that Topic belonged to).
//
// The binding is written exactly once, at Topic-creation time
// (Engine.recordTopicBoundToSeat), from a structured signal — which bot's
// own username matched the @mention that triggered CreateForumTopic — never
// inferred later by re-scraping a message's text.
type topicSeatBinding struct {
	ThreadID string `json:"thread_id"`
	Seat     string `json:"seat"`
}

type topicSeatBindingLedger struct {
	Bindings []topicSeatBinding `json:"bindings"`
}

type topicSeatBindingStore struct {
	mu   sync.Mutex
	path string
}

func newTopicSeatBindingStore(dataDir string) *topicSeatBindingStore {
	if strings.TrimSpace(dataDir) == "" {
		return nil
	}
	return &topicSeatBindingStore{path: filepath.Join(dataDir, "topic_seat_bindings.json")}
}

func (s *topicSeatBindingStore) loadLocked() (topicSeatBindingLedger, error) {
	var ledger topicSeatBindingLedger
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return ledger, nil
		}
		return ledger, err
	}
	if len(data) == 0 {
		return ledger, nil
	}
	if err := json.Unmarshal(data, &ledger); err != nil {
		return ledger, err
	}
	return ledger, nil
}

func (s *topicSeatBindingStore) saveLocked(ledger topicSeatBindingLedger) error {
	data, err := json.MarshalIndent(ledger, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWriteFile(s.path, data, 0o644)
}

// seatFor returns the seat bound to threadID, or "" if no binding exists.
func (s *topicSeatBindingStore) seatFor(threadID string) string {
	if s == nil || strings.TrimSpace(threadID) == "" {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ledger, err := s.loadLocked()
	if err != nil {
		slog.Warn("topic seat binding: failed to load", "thread_id", threadID, "error", err)
		return ""
	}
	for _, b := range ledger.Bindings {
		if b.ThreadID == threadID {
			return b.Seat
		}
	}
	return ""
}

// bind records threadID as owned by seat. First writer wins: a Topic is
// created by exactly one seat's bot, so this does not overwrite an existing
// binding for the same threadID (unlike topicLetterBindingStore.bind, whose
// {{LETTER_ID}} resolution is intentionally allowed to move; a Topic's
// creator never changes).
func (s *topicSeatBindingStore) bind(threadID, seat string) {
	if s == nil || strings.TrimSpace(threadID) == "" || strings.TrimSpace(seat) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ledger, err := s.loadLocked()
	if err != nil {
		slog.Warn("topic seat binding: failed to load before bind", "thread_id", threadID, "seat", seat, "error", err)
		return
	}
	for _, b := range ledger.Bindings {
		if b.ThreadID == threadID {
			return
		}
	}
	ledger.Bindings = append(ledger.Bindings, topicSeatBinding{ThreadID: threadID, Seat: seat})
	if err := s.saveLocked(ledger); err != nil {
		slog.Warn("topic seat binding: failed to save new binding", "thread_id", threadID, "seat", seat, "error", err)
	}
}

func (e *Engine) ensureTopicSeatBindingStore() *topicSeatBindingStore {
	if e.topicSeatBindingStore != nil {
		return e.topicSeatBindingStore
	}
	e.topicSeatBindingStore = newTopicSeatBindingStore(e.dataDir)
	return e.topicSeatBindingStore
}

// recordTopicBoundToSeat implements TopicOwnershipRecorder: it durably binds
// topicID to THIS engine's own seat (e.name). Called by a Platform exactly
// once, immediately after it creates a Topic in direct response to an
// explicit @mention of its own bot username (L-0669 follow-up).
func (e *Engine) recordTopicBoundToSeat(topicID string) {
	e.ensureTopicSeatBindingStore().bind(topicID, e.name)
}

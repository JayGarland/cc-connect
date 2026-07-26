package core

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// topicLetterBinding remembers the last letter ID a Telegram topic (thread)
// resolved to, for a pattern-seat workspace whose letter couldn't be
// determined from the dispatch ledger.
//
// This is deliberately separate from both dispatchStore (dispatch_expectations.json,
// which drives QUERY/RESULT notification bookkeeping in outbox.go/notify.go and
// must not be polluted with workspace-only rows that lack ResultPath/IndexPath)
// and WorkspaceBindingManager (workspace_bindings.json, the explicit /workspace
// bind command for multiWorkspace seats, keyed by channelKey and consulted only
// downstream of workspacePattern resolution). Neither fits: this store's one job
// is remembering a pattern seat's {{LETTER_ID}} resolution per topic.
//
// Why it exists: findLetterIDByTopic reads the dispatch ledger, which is only
// populated when a letter is dispatched through the strict [DISPATCH]
// interception path. Pattern seats (workspace_pattern containing {{LETTER_ID}})
// also accept ad-hoc continuation messages in a topic that was never
// [DISPATCH]-registered, and resolveWorkspacePattern has always fallen back to
// scraping the message body, then to fabricating "L-"+threadID, on every such
// message. Recomputing that fallback fresh per message means the SAME topic
// can resolve to a DIFFERENT letter ID depending on what each message happens
// to say — e.g. "continue 650" doesn't match the L-XXXX text pattern, so a
// resumed topic silently mints an unrelated new worktree instead of reusing
// the one already in progress. That is the Cardinal Invariant violation
// (durable identity from message content, not a stable key) that #54 already
// closed for cooperative-chat (empty-pattern) seats.
//
// This closes the same class for pattern seats WITHOUT taking away the
// existing manual-dispatch feature (L-0320): an explicit, well-formed letter
// mention in the message body still redirects immediately (resolveWorkspacePattern
// tries text extraction before consulting this store) and becomes the new
// remembered default going forward (bind upserts, it does not lock in the
// first resolution forever). This store is consulted only when the CURRENT
// message fails to name a letter at all — that is the case that used to fall
// through to fabricating "L-"+threadID instead of remembering what the topic
// was already working on.
type topicLetterBinding struct {
	Project  string `json:"project"`
	ThreadID string `json:"thread_id"`
	Letter   string `json:"letter"`
}

type topicLetterBindingLedger struct {
	Bindings []topicLetterBinding `json:"bindings"`
}

type topicLetterBindingStore struct {
	mu   sync.Mutex
	path string
}

func newTopicLetterBindingStore(dataDir string) *topicLetterBindingStore {
	if strings.TrimSpace(dataDir) == "" {
		return nil
	}
	return &topicLetterBindingStore{path: filepath.Join(dataDir, "topic_letter_bindings.json")}
}

func (s *topicLetterBindingStore) loadLocked() (topicLetterBindingLedger, error) {
	var ledger topicLetterBindingLedger
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

func (s *topicLetterBindingStore) saveLocked(ledger topicLetterBindingLedger) error {
	data, err := json.MarshalIndent(ledger, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWriteFile(s.path, data, 0o644)
}

// lookup returns the letter ID this (project, threadID) topic last resolved
// to, or "" if no binding exists yet.
func (s *topicLetterBindingStore) lookup(project, threadID string) string {
	if s == nil || strings.TrimSpace(threadID) == "" {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ledger, err := s.loadLocked()
	if err != nil {
		slog.Warn("topic letter binding: failed to load", "project", project, "thread_id", threadID, "error", err)
		return ""
	}
	for _, b := range ledger.Bindings {
		if b.ThreadID == threadID && strings.EqualFold(b.Project, project) {
			return b.Letter
		}
	}
	return ""
}

// bind persists the (project, threadID) -> letter mapping, overwriting any
// prior value for this topic. This is an upsert, not write-once: an explicit,
// well-formed letter mention (a deliberate manual-dispatch redirect, L-0320)
// is allowed to change which letter a topic is remembered as working on, and
// that new value becomes the default a later ambiguous continuation resolves
// to. Only resolveWorkspacePattern's ordering — try ledger, then text
// extraction, and consult this store solely when both come up empty —
// prevents an incidental letter mention in ordinary conversation from
// silently overwriting an established binding.
func (s *topicLetterBindingStore) bind(project, threadID, letter string) {
	if s == nil || strings.TrimSpace(threadID) == "" || strings.TrimSpace(letter) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ledger, err := s.loadLocked()
	if err != nil {
		slog.Warn("topic letter binding: failed to load before bind", "project", project, "thread_id", threadID, "error", err)
		return
	}
	for i := range ledger.Bindings {
		b := &ledger.Bindings[i]
		if b.ThreadID == threadID && strings.EqualFold(b.Project, project) {
			if b.Letter == letter {
				return
			}
			b.Letter = letter
			if err := s.saveLocked(ledger); err != nil {
				slog.Warn("topic letter binding: failed to save update", "project", project, "thread_id", threadID, "letter", letter, "error", err)
			}
			return
		}
	}
	ledger.Bindings = append(ledger.Bindings, topicLetterBinding{
		Project:  project,
		ThreadID: threadID,
		Letter:   letter,
	})
	if err := s.saveLocked(ledger); err != nil {
		slog.Warn("topic letter binding: failed to save new binding", "project", project, "thread_id", threadID, "letter", letter, "error", err)
	}
}

func (e *Engine) ensureTopicLetterBindingStore() *topicLetterBindingStore {
	if e.topicLetterBindingStore != nil {
		return e.topicLetterBindingStore
	}
	e.topicLetterBindingStore = newTopicLetterBindingStore(e.dataDir)
	return e.topicLetterBindingStore
}

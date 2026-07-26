package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// topicLetterBinding pins one Telegram topic (thread) to the letter ID its
// pattern-seat workspace was first resolved to.
//
// This is deliberately separate from both dispatchStore (dispatch_expectations.json,
// which drives QUERY/RESULT notification bookkeeping in outbox.go/notify.go and
// must not be polluted with workspace-only rows that lack ResultPath/IndexPath)
// and WorkspaceBindingManager (workspace_bindings.json, the explicit /workspace
// bind command for multiWorkspace seats, keyed by channelKey and consulted only
// downstream of workspacePattern resolution). Neither fits: this store's one job
// is pinning a pattern seat's {{LETTER_ID}} resolution per topic.
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
// closed for cooperative-chat (empty-pattern) seats; this closes the same
// class for pattern seats, where a letter mention is legitimately allowed to
// decide workspace routing (L-0320) but must only do so once per topic.
//
// The stable key is the Telegram thread ID. Message content (or the
// threadID-fallback) may decide the FIRST resolution for a topic; after that,
// the topic is pinned to whatever it first resolved to, exactly like a
// ledger entry would be.
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
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// lookup returns the letter ID this (project, threadID) topic was already
// bound to, or "" if no binding exists yet.
func (s *topicLetterBindingStore) lookup(project, threadID string) string {
	if s == nil || strings.TrimSpace(threadID) == "" {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ledger, err := s.loadLocked()
	if err != nil {
		return ""
	}
	for _, b := range ledger.Bindings {
		if b.ThreadID == threadID && strings.EqualFold(b.Project, project) {
			return b.Letter
		}
	}
	return ""
}

// bind persists the (project, threadID) -> letter mapping the first time it
// is resolved. It is a no-op if a binding already exists: a later, possibly
// worse, resolution (a message that fails to name the letter, or a
// threadID-based fabrication) must never override an established binding.
func (s *topicLetterBindingStore) bind(project, threadID, letter string) {
	if s == nil || strings.TrimSpace(threadID) == "" || strings.TrimSpace(letter) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ledger, err := s.loadLocked()
	if err != nil {
		return
	}
	for _, b := range ledger.Bindings {
		if b.ThreadID == threadID && strings.EqualFold(b.Project, project) {
			return
		}
	}
	ledger.Bindings = append(ledger.Bindings, topicLetterBinding{
		Project:  project,
		ThreadID: threadID,
		Letter:   letter,
	})
	_ = s.saveLocked(ledger)
}

func (e *Engine) ensureTopicLetterBindingStore() *topicLetterBindingStore {
	if e.topicLetterBindingStore != nil {
		return e.topicLetterBindingStore
	}
	e.topicLetterBindingStore = newTopicLetterBindingStore(e.dataDir)
	return e.topicLetterBindingStore
}

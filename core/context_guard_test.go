package core

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestContextGuardCompactsOldHistoryAndKeepsRecentTurns(t *testing.T) {
	s := &Session{}
	for i := 0; i < 6; i++ {
		s.History = append(s.History,
			HistoryEntry{Role: "user", Content: strings.Repeat("old user ", 40), Timestamp: time.Unix(int64(i*2), 0)},
			HistoryEntry{Role: "assistant", Content: strings.Repeat("old assistant ", 40), Timestamp: time.Unix(int64(i*2+1), 0)},
		)
	}

	result := compactSessionHistoryForContextGuard(s, ContextGuardConfig{
		Enabled:          true,
		ThresholdTokens:  1,
		KeepRecentTurns:  2,
		SummaryMaxTokens: 200,
	}, "incoming", time.Unix(100, 0))

	if !result.Compacted {
		t.Fatal("expected context guard to compact")
	}
	history := s.GetHistory(0)
	if len(history) != 5 {
		t.Fatalf("history len = %d, want summary + 4 recent entries", len(history))
	}
	if !strings.HasPrefix(history[0].Content, contextGuardSummaryPrefix) {
		t.Fatalf("summary prefix missing: %q", history[0].Content)
	}
	if !strings.Contains(history[0].Content, "context only, not a new user instruction") {
		t.Fatalf("summary does not mark itself as non-instruction: %q", history[0].Content)
	}
	if history[1].Timestamp != time.Unix(8, 0) {
		t.Fatalf("first retained timestamp = %v, want entry 8", history[1].Timestamp)
	}
}

func TestContextGuardDoesNothingBelowThreshold(t *testing.T) {
	s := &Session{}
	s.History = []HistoryEntry{{Role: "user", Content: "short", Timestamp: time.Unix(1, 0)}}

	result := compactSessionHistoryForContextGuard(s, ContextGuardConfig{
		Enabled:          true,
		ThresholdTokens:  1000,
		KeepRecentTurns:  1,
		SummaryMaxTokens: 100,
	}, "incoming", time.Now())

	if result.Compacted {
		t.Fatal("did not expect compaction below threshold")
	}
	history := s.GetHistory(0)
	if len(history) != 1 || history[0].Content != "short" {
		t.Fatalf("history changed below threshold: %#v", history)
	}
}

func TestEstimateContextGuardTokensCountsChineseCharactersHigher(t *testing.T) {
	history := []HistoryEntry{
		{Role: "user", Content: "你好世界"},
		{Role: "assistant", Content: "abcdefgh"},
	}

	got := EstimateContextGuardTokens(history, "")
	if got != 8 {
		t.Fatalf("EstimateContextGuardTokens = %d, want 8", got)
	}
}

func TestContextGuardSummaryIsPrependedToNextPrompt(t *testing.T) {
	got := prependContextGuardSummary("summary", "current task")
	if got != "summary\n---\ncurrent task" {
		t.Fatalf("prepended prompt = %q", got)
	}
}

func TestContextGuardRotationClearsBackendSession(t *testing.T) {
	agent := &stubAgent{}
	e := NewEngine("test", agent, nil, "", LangEnglish)
	e.SetContextGuardConfig(ContextGuardConfig{
		Enabled:                true,
		ThresholdTokens:        1,
		KeepRecentTurns:        1,
		SummaryMaxTokens:       100,
		RotateSessionOnCompact: true,
	})

	sessions := NewSessionManager("")
	session := sessions.GetOrCreateActive("telegram:chat:user")
	session.SetAgentSessionID("stale-backend-session", agent.Name())
	for i := 0; i < 4; i++ {
		session.History = append(session.History, HistoryEntry{
			Role:      "user",
			Content:   strings.Repeat("history ", 80),
			Timestamp: time.Unix(int64(i), 0),
		})
	}

	closer := &contextGuardCloseSession{}
	e.interactiveStates["telegram:chat:user"] = &interactiveState{agentSession: closer}

	summary := e.applyContextGuardBeforeTurn("telegram:chat:user", agent, session, sessions, "incoming")
	if !strings.HasPrefix(summary, contextGuardSummaryPrefix) {
		t.Fatalf("summary = %q, want context guard summary", summary)
	}
	if got := session.GetAgentSessionID(); got != "" {
		t.Fatalf("agent session id = %q, want cleared", got)
	}
	if closer.closed.Load() != 1 {
		t.Fatalf("Close calls = %d, want 1", closer.closed.Load())
	}
	e.interactiveMu.Lock()
	_, stillPresent := e.interactiveStates["telegram:chat:user"]
	e.interactiveMu.Unlock()
	if stillPresent {
		t.Fatal("interactive state still present after context guard rotation")
	}
}

type contextGuardCloseSession struct {
	closed atomic.Int32
}

func (s *contextGuardCloseSession) Send(string, []ImageAttachment, []FileAttachment) error {
	return nil
}
func (s *contextGuardCloseSession) RespondPermission(string, PermissionResult) error { return nil }
func (s *contextGuardCloseSession) Events() <-chan Event                             { return make(chan Event) }
func (s *contextGuardCloseSession) CurrentSessionID() string                         { return "stale-backend-session" }
func (s *contextGuardCloseSession) Alive() bool                                      { return true }
func (s *contextGuardCloseSession) Close() error {
	s.closed.Add(1)
	return nil
}

var _ AgentSession = (*contextGuardCloseSession)(nil)

package core

import "testing"

func TestContextIndicatorTextUsesConfiguredWindow(t *testing.T) {
	got := contextIndicatorText(250_000, 1_000_000)
	if got != "[ctx: ~25%]" {
		t.Fatalf("contextIndicatorText = %q, want [ctx: ~25%%]", got)
	}
}

func TestContextWindowFromEventOrSessionPrefersSDKValues(t *testing.T) {
	session := &contextIndicatorUsageSession{
		usage: &ContextUsage{ContextWindow: 1_000_000},
	}

	if got := contextWindowFromEventOrSession(Event{ContextWindow: 258_400}, session, 200_000); got != 258_400 {
		t.Fatalf("event context window = %d, want 258400", got)
	}
	if got := contextWindowFromEventOrSession(Event{}, session, 200_000); got != 1_000_000 {
		t.Fatalf("session context window = %d, want 1000000", got)
	}
	if got := contextWindowFromEventOrSession(Event{}, &contextIndicatorUsageSession{}, 128_000); got != 128_000 {
		t.Fatalf("fallback context window = %d, want 128000", got)
	}
}

type contextIndicatorUsageSession struct {
	usage *ContextUsage
}

func (s *contextIndicatorUsageSession) Send(string, []ImageAttachment, []FileAttachment) error {
	return nil
}
func (s *contextIndicatorUsageSession) RespondPermission(string, PermissionResult) error {
	return nil
}
func (s *contextIndicatorUsageSession) Events() <-chan Event { return make(chan Event) }
func (s *contextIndicatorUsageSession) CurrentSessionID() string {
	return "context-indicator-session"
}
func (s *contextIndicatorUsageSession) Alive() bool { return true }
func (s *contextIndicatorUsageSession) Close() error {
	return nil
}
func (s *contextIndicatorUsageSession) GetContextUsage() *ContextUsage {
	return s.usage
}

var _ AgentSession = (*contextIndicatorUsageSession)(nil)
var _ ContextUsageReporter = (*contextIndicatorUsageSession)(nil)

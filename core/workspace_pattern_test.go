package core

import (
	"path/filepath"
	"testing"
)

func init() {
	RegisterAgent("stub", func(opts map[string]any) (Agent, error) {
		return &stubAgent{}, nil
	})
}

func TestWorkspacePatternResolvesLetterIDFromDispatchLedger(t *testing.T) {
	root := t.TempDir()
	e := NewEngine("dev-pro", &stubAgent{}, nil, filepath.Join(root, "sessions.json"), LangEnglish)
	e.SetDataDir(root)
	e.SetWorkspacePattern(filepath.Join(root, "worktrees", "letter-{{LETTER_ID}}"))

	if err := e.ensureDispatchStore().upsert(DispatchExpectation{
		Letter:          "L-0158",
		To:              "dev-pro",
		TopicID:         "1091",
		TopicSessionKey: "telegram:-1003917051393:1091:7664413698",
		State:           dispatchStateDispatched,
	}); err != nil {
		t.Fatalf("upsert dispatch expectation: %v", err)
	}

	want := filepath.Join(root, "worktrees", "letter-L-0158")
	if got := e.resolveWorkspacePattern("1091"); got != want {
		t.Fatalf("resolveWorkspacePattern() = %q, want %q", got, want)
	}
	if got := e.branchNameForWorkspace(want); got != "letter/L-0158" {
		t.Fatalf("branchNameForWorkspace() = %q, want %q", got, "letter/L-0158")
	}
}

func TestWorkspacePatternLetterFallbackUsesTaskBranch(t *testing.T) {
	root := t.TempDir()
	e := NewEngine("dev-pro", &stubAgent{}, nil, filepath.Join(root, "sessions.json"), LangEnglish)
	e.SetDataDir(root)
	e.SetWorkspacePattern(filepath.Join(root, "worktrees", "letter-{{LETTER_ID}}"))

	want := filepath.Join(root, "worktrees", "letter-task-2222")
	if got := e.resolveWorkspacePattern("2222"); got != want {
		t.Fatalf("resolveWorkspacePattern() = %q, want %q", got, want)
	}
	if got := e.branchNameForWorkspace(want); got != "task-2222" {
		t.Fatalf("branchNameForWorkspace() = %q, want %q", got, "task-2222")
	}
}

func TestWorkspacePatternHelpers(t *testing.T) {
	// Test extractThreadID
	if got := extractThreadID("chatID:123"); got != "123" {
		t.Errorf("extractThreadID(chatID:123) = %q, want %q", got, "123")
	}
	if got := extractThreadID("chatID"); got != "" {
		t.Errorf("extractThreadID(chatID) = %q, want %q", got, "")
	}

	// Test extractThreadIDFromSessionKey
	if got := extractThreadIDFromSessionKey("telegram:chatID:123:userID"); got != "123" {
		t.Errorf("extractThreadIDFromSessionKey(telegram:chatID:123:userID) = %q, want %q", got, "123")
	}
	if got := extractThreadIDFromSessionKey("telegram:chatID:userID"); got != "" {
		t.Errorf("extractThreadIDFromSessionKey(telegram:chatID:userID) = %q, want %q", got, "")
	}

	// Test extractThreadIDFromPath
	pattern := `F:\nexus\worktrees\task-{{THREAD_ID}}`
	if got := extractThreadIDFromPath(pattern, `F:\nexus\worktrees\task-123`); got != "123" {
		t.Errorf("extractThreadIDFromPath(F:\\nexus\\worktrees\\task-123) = %q, want %q", got, "123")
	}
}

func TestWorkspacePatternRouting(t *testing.T) {
	agent := &stubAgent{}
	p := &stubPlatformEngine{n: "telegram"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	e.SetWorkspacePattern(`F:\nexus\worktrees\task-{{THREAD_ID}}`)

	msg := &Message{
		SessionKey: "telegram:-1003917051393:123:7664413698",
		ChannelKey: "-1003917051393:123",
		Platform:   "telegram",
	}

	_, _, _, effectiveDir, err := e.commandContextWithWorkspace(p, msg)
	if err != nil {
		t.Fatalf("unexpected error in commandContextWithWorkspace: %v", err)
	}

	wantDir := `F:\nexus\worktrees\task-123`
	if effectiveDir != wantDir {
		t.Errorf("effectiveDir = %q, want %q", effectiveDir, wantDir)
	}
}

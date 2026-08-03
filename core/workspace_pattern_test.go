package core

import (
	"os"
	"path/filepath"
	"strings"
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
	if got := e.resolveWorkspacePattern("1091", ""); got != want {
		t.Fatalf("resolveWorkspacePattern() = %q, want %q", got, want)
	}
	if got := e.branchNameForWorkspace(want); got != "L-0158" {
		t.Fatalf("branchNameForWorkspace() = %q, want %q", got, "L-0158")
	}
}

// TestWorkspacePatternLetterFallbackRefusesUnauthorizedTopic used to assert the
// opposite: that a topic with no ledger entry silently became letter
// "L-<threadID>" with a worktree and branch of its own. Boss ruled that
// fabrication out on 2026-08-03 (L-0767) after topic 10565 produced letter
// "L-10565", a worktree disconnected from the L-0765 work it actually carried,
// and a discarded 5.75-hour session. Fail closed, and say so out loud.
func TestWorkspacePatternLetterFallbackRefusesUnauthorizedTopic(t *testing.T) {
	root := t.TempDir()
	e := NewEngine("dev-pro", &stubAgent{}, nil, filepath.Join(root, "sessions.json"), LangEnglish)
	e.SetDataDir(root)
	e.SetWorkspacePattern(filepath.Join(root, "worktrees", "letter-{{LETTER_ID}}"))

	if got, err := e.resolveWorkspacePatternChecked("2222", ""); err == nil {
		t.Fatalf("resolveWorkspacePatternChecked() = %q with no error; want refusal for an unauthorized topic", got)
	}
}

// TestWorkspacePatternStaysPinnedAcrossMessagesWithoutLedgerEntry reproduces
// the bug reported against the resonova-pipeline-controller thread: a topic
// bound by an earlier authorized resolution must stay pinned to that letter
// regardless of what any later message says. Updated for L-0666 (Phase 4 of the
// L-0658 RFC): message-hint text is no longer consulted for pattern seats AT
// ALL, so a mention of any letter, matching or not, must never redirect an
// established topic (the old manual-dispatch text redirect, L-0320, is retired).
//
// Updated again for L-0767: an UNBOUND topic no longer fabricates
// "L-<threadID>" to pin to — it is refused. This test therefore establishes the
// binding the authorized way (a dispatch-ledger entry) and then asserts that
// later message text cannot move it. The "hint is ignored" coverage is
// unchanged; only the way the topic first becomes legitimate is.
func TestWorkspacePatternStaysPinnedAcrossMessagesWithoutLedgerEntry(t *testing.T) {
	root := t.TempDir()
	e := NewEngine("dev-pro", &stubAgent{}, nil, filepath.Join(root, "sessions.json"), LangEnglish)
	e.SetDataDir(root)
	e.SetWorkspacePattern(filepath.Join(root, "worktrees", "letter-{{LETTER_ID}}"))

	const threadID = "6031"
	// Authorized binding, written the only sanctioned way.
	e.ensureTopicLetterBindingStore().bind("dev-pro", threadID, "L-6031")
	want := filepath.Join(root, "worktrees", "letter-L-6031")

	if got := e.resolveWorkspacePattern(threadID, "L-0650 Step 0-B/C landed"); got != want {
		t.Fatalf("first resolution = %q, want %q (hint must be ignored)", got, want)
	}

	// A resume after a crash/quota-cut, phrased without the "L-" prefix and
	// without 4+ digits, must stay pinned.
	if got := e.resolveWorkspacePattern(threadID, "continue from where 650 stopped"); got != want {
		t.Fatalf("resume with bare '650' = %q, want %q (must stay pinned, not fabricate L-%s)", got, want, threadID)
	}

	// No hint at all: still pinned.
	if got := e.resolveWorkspacePattern(threadID, ""); got != want {
		t.Fatalf("resume with no hint = %q, want %q", got, want)
	}

	// An explicit, well-formed mention of a DIFFERENT letter must NOT
	// redirect an established topic either — the manual-dispatch text
	// redirect feature (L-0320) is retired (L-0666); only the dispatch
	// ledger or the ControlPlane confirm-card flow can now bind a topic to
	// a letter.
	if got := e.resolveWorkspacePattern(threadID, "L-9999"); got != want {
		t.Fatalf("mention of a different letter = %q, want %q (must stay pinned, redirect feature retired)", got, want)
	}
}

// TestWorkspacePatternFirstResolutionViaFallbackStaysPinned covers the case
// where even the FIRST message in a brand-new topic fails to name a letter:
// the threadID-based fallback fires once, and must be remembered from then
// on rather than re-derived (which would be a no-op here since threadID is
// stable, but this guards the invariant explicitly rather than relying on
// coincidence).
// TestWorkspacePatternFirstResolutionViaFallbackRefuses asserts that a topic
// with no dispatch-ledger entry and no sticky binding is refused, not silently
// assigned "L-<threadID>" (L-0767 fail-closed).
func TestWorkspacePatternFirstResolutionViaFallbackRefuses(t *testing.T) {
	root := t.TempDir()
	e := NewEngine("dev-pro", &stubAgent{}, nil, filepath.Join(root, "sessions.json"), LangEnglish)
	e.SetDataDir(root)
	e.SetWorkspacePattern(filepath.Join(root, "worktrees", "letter-{{LETTER_ID}}"))

	const threadID = "7777"
	if got, err := e.resolveWorkspacePatternChecked(threadID, ""); err == nil {
		t.Fatalf("resolveWorkspacePatternChecked() = %q, want refusal (no ledger, no binding)", got)
	}
	// Nothing should have been pinned.
	if bound := e.ensureTopicLetterBindingStore().lookup("dev-pro", threadID); bound != "" {
		t.Fatalf("binding written after refusal = %q, want empty", bound)
	}
}

// TestWorkspacePatternLedgerAlwaysWinsOverBinding confirms the dispatch ledger
// is authoritative: if a topic IS properly [DISPATCH]-registered, the ledger
// answer wins. The pre-L-0767 "fallback binding" path is retired; this test
// no longer exercises that path (it was fabricating "L-<threadID>").
func TestWorkspacePatternLedgerAlwaysWinsOverBinding(t *testing.T) {
	root := t.TempDir()
	e := NewEngine("dev-pro", &stubAgent{}, nil, filepath.Join(root, "sessions.json"), LangEnglish)
	e.SetDataDir(root)
	e.SetWorkspacePattern(filepath.Join(root, "worktrees", "letter-{{LETTER_ID}}"))

	const threadID = "8888"

	// Register via dispatch ledger (the only authorized way).
	if err := e.ensureDispatchStore().upsert(DispatchExpectation{
		Letter:          "L-0200",
		To:              "dev-pro",
		TopicID:         threadID,
		TopicSessionKey: "telegram:-1003917051393:" + threadID + ":7664413698",
		State:           dispatchStateDispatched,
	}); err != nil {
		t.Fatalf("upsert dispatch expectation: %v", err)
	}

	ledgerWant := filepath.Join(root, "worktrees", "letter-L-0200")
	if got := e.resolveWorkspacePattern(threadID, ""); got != ledgerWant {
		t.Fatalf("resolution = %q, want %q (ledger must win)", got, ledgerWant)
	}
}

// TestWorkspacePatternEmptyPatternDispatchTopicIsolationStaysPinned covers the
// sibling fallback branch (workspacePattern == "" with dispatchTopicIsolation)
// for symmetry: it also writes through ensureTopicLetterBindingStore on a
// ledger miss, even though "L-"+threadID is already deterministic here, so
// the two branches don't silently diverge in behavior over time.
func TestWorkspacePatternEmptyPatternDispatchTopicIsolationStaysPinned(t *testing.T) {
	root := t.TempDir()
	e := NewEngine("dev-pro", &stubAgent{}, nil, filepath.Join(root, "sessions.json"), LangEnglish)
	e.SetDataDir(root)
	e.SetDispatchTopicIsolation(true)

	const threadID = "9999"
	want := "L-9999"
	if got := e.resolveWorkspacePattern(threadID, ""); got != want {
		t.Fatalf("first resolution = %q, want %q", got, want)
	}
	bound := e.ensureTopicLetterBindingStore().lookup("dev-pro", threadID)
	if bound != want {
		t.Fatalf("persisted binding = %q, want %q", bound, want)
	}
	if got := e.resolveWorkspacePattern(threadID, ""); got != want {
		t.Fatalf("second resolution = %q, want %q", got, want)
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

func TestAppendRehydrationEnvUsesDispatchLetter(t *testing.T) {
	root := t.TempDir()
	seedArchive(t, root)

	dataDir := filepath.Join(root, "data")
	e := NewEngine("dev-pro", &stubAgent{}, nil, filepath.Join(root, "sessions.json"), LangEnglish)
	e.SetDataDir(dataDir)
	e.SetWorkspacePattern(filepath.Join(root, "worktrees", "letter-{{LETTER_ID}}"))

	if err := e.ensureDispatchStore().upsert(DispatchExpectation{
		Letter:          "L-0251",
		Thread:          "rehydration-mechanism",
		To:              "dev-pro",
		TopicID:         "1091",
		TopicSessionKey: "telegram:-1003917051393:1091:7664413698",
		State:           dispatchStateDispatched,
	}); err != nil {
		t.Fatalf("upsert dispatch expectation: %v", err)
	}

	env := e.appendRehydrationEnv(nil, "telegram:-1003917051393:1091:7664413698", "", "", PersonaClassWrite)
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "CC_REHYDRATION_ACTIVE_LETTER=L-0251") {
		t.Fatalf("missing active letter env:\n%s", joined)
	}
	if !strings.Contains(joined, "CC_REHYDRATION_BUDGET=write-heavy") {
		t.Fatalf("missing write budget env:\n%s", joined)
	}
	if !strings.Contains(joined, "Rehydration Digest") || !strings.Contains(joined, "实现方案 B") {
		t.Fatalf("digest did not include active letter context:\n%s", joined)
	}
}

// TestAppendRehydrationEnvPrefersConfiguredArchiveDir verifies the L-0469
// fix: an explicit SetArchiveDir wins over the DeriveArchiveDir(dataDir)
// fallback, even when the derived path also resolves to a real archive.
func TestAppendRehydrationEnvPrefersConfiguredArchiveDir(t *testing.T) {
	root := t.TempDir()
	seedArchive(t, root) // seeds a derivable archive at root/docs/archive (decoy)

	explicitRoot := t.TempDir()
	explicitArchive := filepath.Join(explicitRoot, "nexus-archive")
	if err := os.MkdirAll(explicitArchive, 0o755); err != nil {
		t.Fatalf("mkdir explicit archive: %v", err)
	}
	explicitIndex := "# EXPLICIT_ARCHIVE_MARKER\n\n| ID | Type | Thread | Parent | 一句话摘要 | Date |\n|---|---|---|---|---|---|\n"
	if err := os.WriteFile(filepath.Join(explicitArchive, "INDEX.md"), []byte(explicitIndex), 0o644); err != nil {
		t.Fatalf("write explicit INDEX.md: %v", err)
	}

	e := NewEngine("dev-pro", &stubAgent{}, nil, filepath.Join(root, "sessions.json"), LangEnglish)
	e.SetDataDir(filepath.Join(root, "data")) // derives to root/docs/archive (the decoy)
	e.SetArchiveDir(explicitArchive)

	env := e.appendRehydrationEnv(nil, "no-such-session", "", "", PersonaClassWrite)
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "EXPLICIT_ARCHIVE_MARKER") {
		t.Fatalf("expected digest to use the explicitly configured archive dir, not the derived one:\n%s", joined)
	}
	if strings.Contains(joined, "rehydration-mechanism") {
		t.Fatalf("digest leaked content from the decoy derived archive:\n%s", joined)
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

func TestIsThreadWorktreeBranch(t *testing.T) {
	cases := []struct {
		branch string
		want   bool
	}{
		{"letter-824", true},
		{"letter/L-0158", true},
		{"task-824", true},
		{"feature/foo", false},
	}
	for _, tc := range cases {
		if got := isThreadWorktreeBranch(tc.branch); got != tc.want {
			t.Fatalf("isThreadWorktreeBranch(%q) = %v, want %v", tc.branch, got, tc.want)
		}
	}
}

// Regression test for L-0320: manual dispatch (no ledger entry) should extract
// the letter ID from the message content (e.g. "处理 L-0313") instead of
// fabricating L-<topicID> (e.g. L-2793).
// TestResolveWorkspacePattern_MessageHintIgnoredForPatternSeats verifies that a
// pattern seat never reads the message body for routing. A topic with no ledger
// entry is refused (fail-closed, L-0767); a topic that IS ledger-registered
// ignores any letter mention in the message text.
func TestResolveWorkspacePattern_MessageHintIgnoredForPatternSeats(t *testing.T) {
	root := t.TempDir()
	e := NewEngine("dev-swift", &stubAgent{}, nil, filepath.Join(root, "sessions.json"), LangEnglish)
	e.SetDataDir(root)
	e.SetWorkspacePattern(filepath.Join(root, "worktrees", "letter-{{LETTER_ID}}"))

	// No ledger entry — must be refused regardless of message content.
	if got, err := e.resolveWorkspacePatternChecked("2793", "处理 L-0313"); err == nil {
		t.Fatalf("resolveWorkspacePatternChecked(no ledger, hint present) = %q, want refusal", got)
	}

	// Register legitimately.
	if err := e.ensureDispatchStore().upsert(DispatchExpectation{
		Letter:          "L-0313",
		To:              "dev-swift",
		TopicID:         "2793",
		TopicSessionKey: "telegram:-1003917051393:2793:7664413698",
		State:           dispatchStateDispatched,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	want := filepath.Join(root, "worktrees", "letter-L-0313")

	// Registered topic — hint is still irrelevant; ledger drives the result.
	if got := e.resolveWorkspacePattern("2793", "处理 L-0313"); got != want {
		t.Fatalf("resolveWorkspacePattern(ledger present, hint=L-0313) = %q, want %q", got, want)
	}
	if got := e.resolveWorkspacePattern("2793", "no mention at all"); got != want {
		t.Fatalf("resolveWorkspacePattern(ledger present, no hint) = %q, want %q", got, want)
	}
}

func TestWorkspacePatternRouting_DispatchTopicIsolation(t *testing.T) {
	root := t.TempDir()

	// Create a dummy agent that implements GetWorkDir() string
	dummyWorkDir := filepath.Join(root, "my_workdir")
	agent := &dummyAgentWithWorkDir{
		stubAgent: stubAgent{},
		workDir:   dummyWorkDir,
	}

	p := &stubPlatformEngine{n: "telegram"}
	e := NewEngine("reviewer-seat", agent, []Platform{p}, filepath.Join(root, "sessions.json"), LangEnglish)
	e.SetDataDir(root)
	e.SetDispatchTopicIsolation(true)

	// Verify that multiWorkspace is enabled and workspacePool is initialized
	if !e.multiWorkspace {
		t.Fatalf("expected multiWorkspace to be true")
	}
	if e.workspacePool == nil {
		t.Fatalf("expected workspacePool to be initialized")
	}

	// We simulate a message in threadID "2793" whose body mentions "L-0323".
	// For a cooperative (empty-pattern) seat this is chat, not a dispatch route:
	// the shard stays stable per-topic (L-2793) and does NOT hop to L-0323.
	// Real dispatch lands each letter in its own topic and resolves via the ledger.
	msg := &Message{
		SessionKey: "telegram:-1003917051393:2793:7664413698",
		ChannelKey: "-1003917051393:2793",
		Platform:   "telegram",
		Content:    "处理 L-0323",
	}

	// Make sure the agent type is registered in the pool
	RegisterAgent("reviewer-seat-agent", func(opts map[string]any) (Agent, error) {
		return agent, nil
	})
	// Change dummy name to match
	agent.name = "reviewer-seat-agent"

	// Resolve the command context
	wsAgent, wsSessions, interactiveKey, effectiveDir, err := e.commandContextWithWorkspace(p, msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The shard is the stable per-topic key L-2793 (from threadID), NOT the
	// L-0323 mentioned in the body — a free-text letter mention must not switch
	// the cooperative seat's session shard.
	if shard := e.resolveWorkspacePattern("2793", msg.Content); shard != "L-2793" {
		t.Errorf("session shard = %q, want %q (a free-text letter mention must not switch shards)", shard, "L-2793")
	}

	// The interactive key is prefixed with the *effective work dir*, not the
	// shard key. This assertion used to read "L-2793:..." — it pinned the
	// command path's own output without ever comparing it to the key
	// handleMessage files live state under, which is what let the two drift
	// apart until /new stopped clearing anything (see
	// TestCommandContext_MatchesInteractiveSessionTarget).
	wantKey := dummyWorkDir + ":telegram:-1003917051393:2793:7664413698"
	if interactiveKey != wantKey {
		t.Errorf("interactiveKey = %q, want %q", interactiveKey, wantKey)
	}

	// The effective directory should NOT be the virtual workspace "L-2793"
	// but the agent's workdir because "L-2793" is not an absolute path.
	if effectiveDir != dummyWorkDir {
		t.Errorf("effectiveDir = %q, want %q", effectiveDir, dummyWorkDir)
	}

	if wsAgent == nil || wsSessions == nil {
		t.Fatalf("expected non-nil wsAgent and wsSessions")
	}
}

type dummyAgentWithWorkDir struct {
	stubAgent
	workDir string
	name    string
}

func (a *dummyAgentWithWorkDir) Name() string {
	if a.name != "" {
		return a.name
	}
	return "dummy-agent-with-workdir"
}

func (a *dummyAgentWithWorkDir) GetWorkDir() string {
	return a.workDir
}

func TestWorkspacePattern_DispatchBranchIsolation(t *testing.T) {
	root := t.TempDir()
	e := NewEngine("dev-pro", &stubAgent{}, nil, filepath.Join(root, "sessions.json"), LangEnglish)
	e.SetDataDir(root)
	e.SetWorkspacePattern(filepath.Join(root, "worktrees", "letter-{{LETTER_ID}}"))

	// By default, dispatchBranchIsolation is true, so it returns the bare
	// letter id L-XXXX (the "letter/" prefix was excised in L-0674).
	wantLetter := filepath.Join(root, "worktrees", "letter-L-0158")
	if got := e.branchNameForWorkspace(wantLetter); got != "L-0158" {
		t.Fatalf("default branchNameForWorkspace() = %q, want %q", got, "L-0158")
	}

	// When set to false, it should return "main"
	e.SetDispatchBranchIsolation(false)
	if got := e.branchNameForWorkspace(wantLetter); got != "main" {
		t.Fatalf("dispatchBranchIsolation=false branchNameForWorkspace() = %q, want %q", got, "main")
	}
}

func TestWorkspacePattern_EmptyThreadIDBypass(t *testing.T) {
	root := t.TempDir()
	e := NewEngine("dev-swift", &stubAgent{}, nil, filepath.Join(root, "sessions.json"), LangEnglish)
	e.SetDataDir(root)

	// Scenario 1: pattern doesn't contain {{THREAD_ID}} but contains {{LETTER_ID}}
	e.SetWorkspacePattern(filepath.Join(root, "worktrees", "letter-{{LETTER_ID}}"))
	want1 := filepath.Join(root, "worktrees", "letter-L-0319")
	if got1 := e.resolveWorkspacePattern("", "process L-0319"); got1 != want1 {
		t.Fatalf("resolveWorkspacePattern with empty threadID (pattern with LETTER_ID) = %q, want %q", got1, want1)
	}

	// Scenario 2: empty-pattern dispatchTopicIsolation seat (cooperative chat seat)
	// in a DM. A letter mentioned in the message body must NOT switch the session
	// shard — ad-hoc chat stays on the stable "general" session, it does not hop
	// into that letter's (often empty) shard and cold-start (L-0587). Formal letter
	// dispatch arrives on a topic and is resolved from the ledger, not free text.
	e.SetWorkspacePattern("")
	e.SetDispatchTopicIsolation(true)
	want2 := defaultDispatchWorkspaceKey
	if got2 := e.resolveWorkspacePattern("", "process L-0319"); got2 != want2 {
		t.Fatalf("resolveWorkspacePattern with empty threadID + letter mention (dispatchTopicIsolation) = %q, want %q", got2, want2)
	}

	// Scenario 3: default case (returns "")
	e.SetWorkspacePattern("")
	e.SetDispatchTopicIsolation(false)
	if got3 := e.resolveWorkspacePattern("", "process L-0319"); got3 != "" {
		t.Fatalf("resolveWorkspacePattern with empty threadID (default fallback) = %q, want empty", got3)
	}

	// Scenario 4: dispatchTopicIsolation is true, message has no letter ID —
	// falls back to the shared default key instead of "" (L-0570 follow-up:
	// plain ad-hoc chat in General/private DM must not demand /workspace init).
	e.SetWorkspacePattern("")
	e.SetDispatchTopicIsolation(true)
	if got4 := e.resolveWorkspacePattern("", "hi"); got4 != defaultDispatchWorkspaceKey {
		t.Fatalf("resolveWorkspacePattern with empty threadID/no letter ID (dispatchTopicIsolation) = %q, want %q", got4, defaultDispatchWorkspaceKey)
	}
}

// TestResolveWorkspacePattern_ChatLetterMentionDoesNotSwitchShard is the L-0587
// regression: for an empty-pattern cooperative chat seat, a letter number that
// merely appears in the message body must NOT redirect the session to that
// letter's shard (which cold-starts and drops the ongoing conversation). The
// session shard stays stable — "general" in a DM, "L-<threadID>" in a topic that
// is not a dispatch target. Formal dispatch still routes via the ledger.
func TestResolveWorkspacePattern_ChatLetterMentionDoesNotSwitchShard(t *testing.T) {
	root := t.TempDir()
	e := NewEngine("product-manager", &stubAgent{}, nil, filepath.Join(root, "sessions.json"), LangEnglish)
	e.SetDataDir(root)
	e.SetWorkspacePattern("") // cooperative chat seat: static work_dir, no per-letter worktree
	e.SetDispatchTopicIsolation(true)

	// DM (threadless): mentioning L-0587 stays on the stable "general" shard.
	if got := e.resolveWorkspacePattern("", "看看 L-0587 那封信"); got != defaultDispatchWorkspaceKey {
		t.Fatalf("DM letter mention = %q, want %q (must not switch shard)", got, defaultDispatchWorkspaceKey)
	}

	// Topic with no ledger entry: mentioning L-0587 stays on the per-topic shard
	// L-<threadID>, not the mentioned letter's shard.
	if got := e.resolveWorkspacePattern("5720", "看看 L-0587 那封信"); got != "L-5720" {
		t.Fatalf("topic letter mention = %q, want %q (must not switch shard)", got, "L-5720")
	}
}

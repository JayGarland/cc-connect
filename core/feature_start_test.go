package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseFeatureStartArgs(t *testing.T) {
	opts, err := parseFeatureStartArgs([]string{"build", "tts", "batch"})
	if err != nil {
		t.Fatalf("parseFeatureStartArgs: %v", err)
	}
	if opts.Title != "build tts batch" {
		t.Fatalf("Title = %q, want build tts batch", opts.Title)
	}
}

func TestParseFeatureStartArgsRejectsUnknownFlag(t *testing.T) {
	for _, flag := range []string{"--impl", "--risk", "--review", "--auto"} {
		if _, err := parseFeatureStartArgs([]string{"x", flag}); err == nil {
			t.Fatalf("expected unknown flag error for %s", flag)
		}
	}
}

func TestFeatureBoardStoreCreate(t *testing.T) {
	dir := t.TempDir()
	store := NewFeatureBoardStore(filepath.Join(dir, "data"))
	store.now = func() time.Time {
		return time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	}

	task, err := store.Create(
		"TTS Batch spike",
		featureChefSeat,
		`F:\GitHub\resonova`,
		"Chef scope feature",
		"telegram:chat:user",
		[]string{featureChefSeat, featureImplSeat, featureCounselSeat},
	)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if task.TaskID != "feat-20260629-120000-tts-batch-spike" {
		t.Fatalf("TaskID = %q", task.TaskID)
	}
	if task.Owner != featureChefSeat || task.Status != "planning" || task.HandbackState != "not_started" {
		t.Fatalf("unexpected task defaults: %+v", task)
	}

	data, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var board FeatureBoard
	if err := json.Unmarshal(data, &board); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(board.Tasks) != 1 || board.Tasks[0].TaskID != task.TaskID {
		t.Fatalf("stored board = %+v", board.Tasks)
	}
	if board.ActiveFeature == nil || board.ActiveFeature.TaskID != task.TaskID {
		t.Fatalf("active feature = %+v, want task %s", board.ActiveFeature, task.TaskID)
	}
	if got := board.ActiveFeature.Seats[featureImplSeat].Status; got != "pending" {
		t.Fatalf("dev seat status = %q, want pending", got)
	}
}

func TestFeatureBoardStoreSeatRefreshLifecycle(t *testing.T) {
	dir := t.TempDir()
	store := NewFeatureBoardStore(filepath.Join(dir, "data"))
	store.now = func() time.Time {
		return time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	}
	task, err := store.Create("Lazy context", featureChefSeat, `F:\GitHub\resonova`, "Scope", "telegram:chat:user", []string{featureImplSeat})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	active, shouldRefresh, err := store.ActiveTaskForSeat(featureImplSeat)
	if err != nil {
		t.Fatalf("ActiveTaskForSeat: %v", err)
	}
	if !shouldRefresh || active.TaskID != task.TaskID {
		t.Fatalf("ActiveTaskForSeat = (%+v, %v), want task refresh", active, shouldRefresh)
	}
	if err := store.MarkSeatRefreshed(task.TaskID, featureImplSeat, "relay-session"); err != nil {
		t.Fatalf("MarkSeatRefreshed: %v", err)
	}
	if _, shouldRefresh, err := store.ActiveTaskForSeat(featureImplSeat); err != nil || shouldRefresh {
		t.Fatalf("after refreshed shouldRefresh=%v err=%v, want false nil", shouldRefresh, err)
	}
}

func newFeatureTestEngine(name, dataDir string) *Engine {
	eng := newTestEngine()
	eng.name = name
	eng.dataDir = dataDir
	return eng
}

func TestApplyLazyFeatureContextToMessage_PendingSeat(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	store := NewFeatureBoardStore(dataDir)
	store.now = func() time.Time {
		return time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	}
	task, err := store.Create("Smoke test", featureChefSeat, `F:\GitHub\resonova`, "Chef scope feature", "telegram:chat:user", []string{featureImplSeat})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	eng := newFeatureTestEngine(featureImplSeat, dataDir)
	sessions := NewSessionManager("")
	msg := &Message{SessionKey: "test-session", Content: "hello world"}

	session, didRefresh, err := eng.applyLazyFeatureContextToMessage(msg, sessions, "interactive-key")
	if err != nil {
		t.Fatalf("applyLazyFeatureContextToMessage: %v", err)
	}
	if !didRefresh {
		t.Fatal("expected didRefresh=true for pending seat")
	}
	if session == nil {
		t.Fatal("expected non-nil session after refresh")
	}
	if !strings.HasPrefix(msg.Content, "[FEATURE-CONTEXT]") {
		t.Fatalf("Content should start with [FEATURE-CONTEXT], got: %s", msg.Content)
	}
	if !strings.Contains(msg.Content, task.TaskID) {
		t.Fatalf("Content should contain task ID %s, got: %s", task.TaskID, msg.Content)
	}
	if !strings.Contains(msg.Content, "hello world") {
		t.Fatalf("Content should retain original message, got: %s", msg.Content)
	}

	_, shouldRefresh, err := store.ActiveTaskForSeat(featureImplSeat)
	if err != nil {
		t.Fatalf("ActiveTaskForSeat after refresh: %v", err)
	}
	if shouldRefresh {
		t.Fatal("after lazy refresh seat should be marked refreshed")
	}
}

func TestApplyLazyFeatureContextToMessage_AlreadyRefreshed(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	store := NewFeatureBoardStore(dataDir)
	store.now = func() time.Time {
		return time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	}
	_, err := store.Create("Smoke test", featureChefSeat, `F:\GitHub\resonova`, "Chef scope feature", "telegram:chat:user", []string{featureImplSeat})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	eng := newFeatureTestEngine(featureImplSeat, dataDir)
	sessions := NewSessionManager("")
	msg := &Message{SessionKey: "test-session", Content: "hello world"}

	_, didRefresh, err := eng.applyLazyFeatureContextToMessage(msg, sessions, "interactive-key")
	if err != nil {
		t.Fatalf("first applyLazyFeatureContextToMessage: %v", err)
	}
	if !didRefresh {
		t.Fatal("first call should refresh")
	}

	msg2 := &Message{SessionKey: "test-session-2", Content: "second message"}
	session2, didRefresh2, err := eng.applyLazyFeatureContextToMessage(msg2, sessions, "interactive-key-2")
	if err != nil {
		t.Fatalf("second applyLazyFeatureContextToMessage: %v", err)
	}
	if didRefresh2 {
		t.Fatal("second call should NOT refresh already-refreshed seat")
	}
	if session2 != nil {
		t.Fatal("session should be nil when no refresh needed")
	}
	if msg2.Content != "second message" {
		t.Fatalf("Content should be unchanged for already-refreshed seat, got: %s", msg2.Content)
	}
}

func TestApplyLazyFeatureContextToRelayMessage_PendingSeat(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	store := NewFeatureBoardStore(dataDir)
	store.now = func() time.Time {
		return time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	}
	task, err := store.Create("Smoke test", featureChefSeat, `F:\GitHub\resonova`, "Chef scope feature", "telegram:chat:user", []string{featureImplSeat})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	eng := newFeatureTestEngine(featureImplSeat, dataDir)
	sessions := NewSessionManager("")
	message := "hello from relay"

	err = eng.applyLazyFeatureContextToRelayMessage(sessions, "relay-session", "source-session", &message)
	if err != nil {
		t.Fatalf("applyLazyFeatureContextToRelayMessage: %v", err)
	}
	if !strings.HasPrefix(message, "[FEATURE-CONTEXT]") {
		t.Fatalf("Relay message should start with [FEATURE-CONTEXT], got: %s", message)
	}
	if !strings.Contains(message, task.TaskID) {
		t.Fatalf("Relay message should contain task ID %s, got: %s", task.TaskID, message)
	}
	if !strings.Contains(message, "hello from relay") {
		t.Fatalf("Relay message should retain original content, got: %s", message)
	}

	_, shouldRefresh, err := store.ActiveTaskForSeat(featureImplSeat)
	if err != nil {
		t.Fatalf("ActiveTaskForSeat after relay refresh: %v", err)
	}
	if shouldRefresh {
		t.Fatal("after relay lazy refresh seat should be marked refreshed")
	}
}

func TestApplyLazyFeatureContextToRelayMessage_AlreadyRefreshed(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	store := NewFeatureBoardStore(dataDir)
	store.now = func() time.Time {
		return time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	}
	_, err := store.Create("Smoke test", featureChefSeat, `F:\GitHub\resonova`, "Chef scope feature", "telegram:chat:user", []string{featureImplSeat})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	eng := newFeatureTestEngine(featureImplSeat, dataDir)
	sessions := NewSessionManager("")

	msg1 := "first relay message"
	if err := eng.applyLazyFeatureContextToRelayMessage(sessions, "relay-session-1", "source-1", &msg1); err != nil {
		t.Fatalf("first applyLazyFeatureContextToRelayMessage: %v", err)
	}

	msg2 := "second relay message"
	if err := eng.applyLazyFeatureContextToRelayMessage(sessions, "relay-session-2", "source-2", &msg2); err != nil {
		t.Fatalf("second applyLazyFeatureContextToRelayMessage: %v", err)
	}
	if msg2 != "second relay message" {
		t.Fatalf("Already-refreshed relay message should be unchanged, got: %s", msg2)
	}
}

func TestLazyFeatureContext_NoActiveFeature(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")

	eng := newFeatureTestEngine(featureImplSeat, dataDir)
	sessions := NewSessionManager("")

	msg := &Message{SessionKey: "test-session", Content: "hello world"}
	session, didRefresh, err := eng.applyLazyFeatureContextToMessage(msg, sessions, "interactive-key")
	if err != nil {
		t.Fatalf("applyLazyFeatureContextToMessage with no task: %v", err)
	}
	if didRefresh {
		t.Fatal("should not refresh when no active feature")
	}
	if session != nil {
		t.Fatal("session should be nil when no active feature")
	}
	if msg.Content != "hello world" {
		t.Fatalf("Content should be unchanged, got: %s", msg.Content)
	}

	relayMsg := "relay with no task"
	if err := eng.applyLazyFeatureContextToRelayMessage(sessions, "relay-session", "source", &relayMsg); err != nil {
		t.Fatalf("applyLazyFeatureContextToRelayMessage with no task: %v", err)
	}
	if relayMsg != "relay with no task" {
		t.Fatalf("Relay message should be unchanged, got: %s", relayMsg)
	}
}

func TestCmdFeatureStartNonChefSilentlyIgnores(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	p := &stubPlatformEngine{n: "telegram"}
	agentSession := newResultAgentSession("should not run")
	eng := NewEngine(featureImplSeat, &resultAgent{session: agentSession}, []Platform{p}, "", LangEnglish)
	eng.dataDir = dataDir

	msg := &Message{
		Platform:   "telegram",
		SessionKey: "telegram:chat:user",
		ReplyCtx:   "reply",
	}
	eng.cmdFeatureStart(p, msg, []string{"Smoke", "test"})

	if _, err := os.Stat(NewFeatureBoardStore(dataDir).Path()); !os.IsNotExist(err) {
		t.Fatalf("non-Chef /feature-start wrote board file, stat err=%v", err)
	}
	if sent := p.getSent(); len(sent) != 0 {
		t.Fatalf("non-Chef /feature-start should be silent, sent=%v", sent)
	}
	if len(agentSession.sentPrompts) != 0 {
		t.Fatalf("non-Chef /feature-start invoked agent prompts: %v", agentSession.sentPrompts)
	}
}

func TestCmdFeatureStartChefBusyDoesNotCreateBoard(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	p := &stubPlatformEngine{n: "telegram"}
	chef := NewEngine(featureChefSeat, &resultAgent{session: newResultAgentSession("unused")}, []Platform{p}, "", LangEnglish)
	chef.dataDir = dataDir
	msg := &Message{
		Platform:   "telegram",
		SessionKey: "telegram:chat:user",
		ReplyCtx:   "reply",
	}
	active := chef.sessions.GetOrCreateActive(msg.SessionKey)
	if !active.TryLock() {
		t.Fatal("failed to pre-lock active Chef session")
	}
	defer active.UnlockWithoutUpdate()

	chef.cmdFeatureStart(p, msg, []string{"Busy", "smoke"})

	if _, err := os.Stat(NewFeatureBoardStore(dataDir).Path()); !os.IsNotExist(err) {
		t.Fatalf("busy Chef /feature-start wrote board file, stat err=%v", err)
	}
	sent := p.getSent()
	if len(sent) != 1 || !strings.Contains(sent[0], "chef session is still processing") {
		t.Fatalf("busy Chef reply = %v, want processing error", sent)
	}
}

func TestCmdFeatureStartFanoutCreatesOneBoardItem(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	rm := NewRelayManager("")
	seatNames := []string{
		featureChefSeat,
		featureChefFlashSeat,
		featureImplSeat,
		"dev-swift",
		featureCounselSeat,
		featureReviewSeat,
		"secretary-seat",
	}
	platforms := make(map[string]*stubPlatformEngine)
	engines := make(map[string]*Engine)
	var chefSession *resultAgentSession
	var chefFlashSession *resultAgentSession
	for _, seat := range seatNames {
		p := &stubPlatformEngine{n: "telegram"}
		platforms[seat] = p
		session := newResultAgentSession("feature scoped")
		if seat == featureChefSeat {
			chefSession = session
		}
		if seat == featureChefFlashSeat {
			chefFlashSession = session
		}
		eng := NewEngine(seat, &resultAgent{session: session}, []Platform{p}, "", LangEnglish)
		eng.dataDir = dataDir
		eng.relayManager = rm
		engines[seat] = eng
		rm.RegisterEngine(seat, eng)
	}
	msg := &Message{
		Platform:   "telegram",
		SessionKey: "telegram:chat:user",
		ReplyCtx:   "reply",
	}

	for _, seat := range seatNames {
		engines[seat].cmdFeatureStart(platforms[seat], msg, []string{"Smoke", "test", "lazy", "refresh"})
	}

	data, err := os.ReadFile(NewFeatureBoardStore(dataDir).Path())
	if err != nil {
		t.Fatalf("read board: %v; chef sent=%v", err, platforms[featureChefSeat].getSent())
	}
	var board FeatureBoard
	if err := json.Unmarshal(data, &board); err != nil {
		t.Fatalf("unmarshal board: %v", err)
	}
	if len(board.Tasks) != 1 {
		t.Fatalf("board task count = %d, want 1: %+v", len(board.Tasks), board.Tasks)
	}
	if board.ActiveFeature == nil || board.ActiveFeature.TaskID != board.Tasks[0].TaskID {
		t.Fatalf("active feature = %+v, tasks=%+v", board.ActiveFeature, board.Tasks)
	}
	for _, seat := range seatNames {
		state := board.ActiveFeature.Seats[seat]
		if state == nil {
			t.Fatalf("missing seat state for %s: %+v", seat, board.ActiveFeature.Seats)
		}
		want := "pending"
		if seat == featureChefSeat || seat == featureChefFlashSeat {
			want = "refreshed"
		}
		if state.Status != want {
			t.Fatalf("seat %s status = %q, want %q", seat, state.Status, want)
		}
	}
	for _, seat := range seatNames {
		sent := platforms[seat].getSent()
		if seat == featureChefSeat {
			foundFeatureStartReply := false
			for _, item := range sent {
				if strings.Contains(item, "Feature started") {
					foundFeatureStartReply = true
					break
				}
			}
			if !foundFeatureStartReply {
				t.Fatalf("Chef sent = %v, want feature-start reply", sent)
			}
			continue
		}
		if seat == featureChefFlashSeat {
			if len(sent) != 1 || !strings.Contains(sent[0], "feature scoped") {
				t.Fatalf("chef-flash sent = %v, want one feature packet response", sent)
			}
			continue
		}
		if len(sent) != 0 {
			t.Fatalf("non-Chef seat %s sent %v, want silent ignore", seat, sent)
		}
	}
	if chefSession == nil || len(chefSession.sentPrompts) != 1 || !strings.Contains(chefSession.sentPrompts[0], "[FEATURE-START]") {
		t.Fatalf("Chef prompts = %+v, want one [FEATURE-START] prompt", chefSession)
	}
	if chefFlashSession == nil || len(chefFlashSession.sentPrompts) != 1 || !strings.Contains(chefFlashSession.sentPrompts[0], "[FEATURE-START]") {
		t.Fatalf("Chef flash prompts = %+v, want one [FEATURE-START] prompt", chefFlashSession)
	}
}

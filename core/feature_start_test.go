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

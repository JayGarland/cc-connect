package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseFeatureStartArgs(t *testing.T) {
	opts, err := parseFeatureStartArgs([]string{"build", "tts", "batch", "--impl", "--risk", "--review"})
	if err != nil {
		t.Fatalf("parseFeatureStartArgs: %v", err)
	}
	if opts.Title != "build tts batch" {
		t.Fatalf("Title = %q, want build tts batch", opts.Title)
	}
	if !opts.Impl || !opts.Risk || !opts.Review {
		t.Fatalf("flags = impl:%v risk:%v review:%v, want all true", opts.Impl, opts.Risk, opts.Review)
	}
}

func TestParseFeatureStartArgsRejectsUnknownFlag(t *testing.T) {
	if _, err := parseFeatureStartArgs([]string{"x", "--auto"}); err == nil {
		t.Fatal("expected unknown flag error")
	}
}

func TestFeatureBoardStoreCreate(t *testing.T) {
	dir := t.TempDir()
	store := NewFeatureBoardStore(filepath.Join(dir, "data"))
	store.now = func() time.Time {
		return time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	}

	task, err := store.Create("TTS Batch spike", featureChefSeat, `F:\GitHub\resonova`, "Chef scope feature")
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
}

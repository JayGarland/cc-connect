package core

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"testing"
)

// TestSessionSnapshot_MatchesOnDiskShardNaming proves the live snapshot's
// ShardFile field is the session manager's real, on-disk StorePath — not a
// guessed or independently-derived path — using the same verification
// method as the founding retro for this invariant
// (docs/retros/2026-07-23-session-drop-two-causes.md §2: "hash the
// candidate workspace strings and match them to the shard filenames on
// disk"). This is the L-0660 acceptance test promised in the QUERY.
func TestSessionSnapshot_MatchesOnDiskShardNaming(t *testing.T) {
	dir := t.TempDir()
	mainStore := filepath.Join(dir, "main_sessions.json")
	agentName := "sessions-snapshot-test-agent"
	RegisterAgent(agentName, func(opts map[string]any) (Agent, error) {
		return &namedTestAgent{name: agentName}, nil
	})
	e := NewEngine("testseat", &namedTestAgent{name: agentName}, nil, mainStore, LangEnglish)

	// Default (non-workspace) session.
	e.sessions.GetOrCreateActive("chat:1")

	// A per-workspace session, created exactly the way handleMessage does
	// via getOrCreateWorkspaceAgent — proves the snapshot describes the real
	// object cc-connect uses at runtime, not a parallel reconstruction.
	workspace := filepath.Join(dir, "worktree-A")
	_, wsSessions, err := e.getOrCreateWorkspaceAgent(workspace)
	if err != nil {
		t.Fatalf("getOrCreateWorkspaceAgent: %v", err)
	}
	wsSessions.GetOrCreateActive("chat:2")

	entries := e.sessionSnapshot()
	if len(entries) != 2 {
		t.Fatalf("expected 2 snapshot entries, got %d: %+v", len(entries), entries)
	}

	var defaultEntry, wsEntry *SessionSnapshotEntry
	for i := range entries {
		switch entries[i].Workspace {
		case defaultSessionSnapshotWorkspace:
			defaultEntry = &entries[i]
		case workspace:
			wsEntry = &entries[i]
		}
	}
	if defaultEntry == nil {
		t.Fatalf("no default-workspace entry found in %+v", entries)
	}
	if defaultEntry.ShardFile != mainStore {
		t.Errorf("default entry ShardFile = %q, want %q", defaultEntry.ShardFile, mainStore)
	}

	if wsEntry == nil {
		t.Fatalf("no per-workspace entry found in %+v", entries)
	}
	if wsEntry.ShardFile != wsSessions.StorePath() {
		t.Errorf("workspace entry ShardFile = %q, want %q", wsEntry.ShardFile, wsSessions.StorePath())
	}

	// Independently recompute the shard filename the same way
	// getOrCreateWorkspaceAgent does (engine.go: sha256(workspace), first 4
	// bytes hex) and confirm it matches the file the snapshot reports.
	h := sha256.Sum256([]byte(workspace))
	wantName := fmt.Sprintf("%s_ws_%s.json", e.name, hex.EncodeToString(h[:4]))
	if got := filepath.Base(wsEntry.ShardFile); got != wantName {
		t.Errorf("shard filename = %q, want %q (independently recomputed hash)", got, wantName)
	}
}

// TestSessionSnapshot_EmptyWhenNoSessions confirms the zero-session case
// renders without panicking and produces no entries.
func TestSessionSnapshot_EmptyWhenNoSessions(t *testing.T) {
	dir := t.TempDir()
	e := NewEngine("testseat", nil, nil, filepath.Join(dir, "main_sessions.json"), LangEnglish)
	if entries := e.sessionSnapshot(); len(entries) != 0 {
		t.Errorf("expected no entries, got %+v", entries)
	}
}

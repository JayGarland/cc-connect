package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildPreferenceExtractQuery(t *testing.T) {
	got := buildPreferenceExtractQuery("L-0500", "L-0467", `F:\ws\chat_history.md`, "2026-07-19")
	for _, want := range []string{
		"ID: L-0500",
		"Parent: L-0467",
		"To: reviewer",
		"Type: QUERY",
		"Thread: preference-extract",
		"Source-Session-Path: F:\\ws\\chat_history.md",
		"boss-profile-candidates.md",
		"Fail-closed",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("query missing %q:\n%s", want, got)
		}
	}
}

func TestResolvePreferenceTranscript_PrefersChatHistorySibling(t *testing.T) {
	dir := t.TempDir()
	jsonl := filepath.Join(dir, "session.jsonl")
	chat := filepath.Join(dir, chatHistoryFileName)
	if err := os.WriteFile(jsonl, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(chat, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := resolvePreferenceTranscript("", "L-0467", receiptRecord{SourceSessionPath: jsonl}, "")
	if got != chat {
		t.Fatalf("got %q, want chat_history sibling %q", got, chat)
	}
}

func TestResolvePreferenceTranscript_UsesProvenance(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "alone.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := resolvePreferenceTranscript("", "L-0467", receiptRecord{}, path)
	if got != path {
		t.Fatalf("got %q, want %q", got, path)
	}
}

func TestLooksPersonalPreferencePath(t *testing.T) {
	if !looksPersonalPreferencePath(`F:\personal\archive\chat.md`) {
		t.Fatal("expected personal path")
	}
	if looksPersonalPreferencePath(`F:\nexus-archive\threads\x\chat.md`) {
		t.Fatal("did not expect personal")
	}
}

func TestWritePreferenceExtractQueryFile(t *testing.T) {
	root := t.TempDir()
	path, err := writePreferenceExtractQueryFile(root, "L-0501", "L-0467", `F:\tmp\chat_history.md`)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "threads", preferenceExtractThread, "L-0501.query.md")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Source-Session-Path: F:\\tmp\\chat_history.md") {
		t.Fatalf("file missing source path:\n%s", data)
	}
}

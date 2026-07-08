package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestExtractLetterIDFromPath(t *testing.T) {
	pattern := `F:\foundry\worktrees\letter-{{LETTER_ID}}`
	if got := core.ExtractLetterIDFromPath(pattern, `F:\foundry\worktrees\letter-L-0158`); got != "L-0158" {
		t.Fatalf("ExtractLetterIDFromPath() = %q, want %q", got, "L-0158")
	}
}

func TestLoadDispatchLettersByTopic(t *testing.T) {
	root := t.TempDir()
	data := []byte(`{
  "expectations": [
    {
      "letter": "L-0158",
      "to": "dev-pro",
      "topic_id": "1091",
      "topic_session_key": "telegram:-1003917051393:1091:7664413698"
    },
    {
      "letter": "L-0160",
      "to": "dev-swift",
      "topic_id": "1092",
      "topic_session_key": "telegram:-1003917051393:1092:7664413698"
    }
  ]
}`)
	if err := os.WriteFile(filepath.Join(root, "dispatch_expectations.json"), data, 0o644); err != nil {
		t.Fatalf("write dispatch ledger: %v", err)
	}

	got := loadDispatchLettersByTopic(root, "dev-pro")
	if got["1091"] != "L-0158" {
		t.Fatalf("topic 1091 maps to %q, want %q", got["1091"], "L-0158")
	}
	if _, ok := got["1092"]; ok {
		t.Fatal("dev-pro mapping included dev-swift topic 1092")
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

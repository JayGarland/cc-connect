package main

import "testing"

func TestParseContextGuardConfig(t *testing.T) {
	cfg, ok := parseContextGuardConfig(map[string]any{
		"enabled":                   true,
		"threshold_tokens":          int64(90000),
		"keep_recent_turns":         int64(12),
		"summary_max_tokens":        int64(3000),
		"rotate_session_on_compact": true,
	})
	if !ok {
		t.Fatal("parseContextGuardConfig returned ok=false")
	}
	if !cfg.Enabled {
		t.Fatal("Enabled = false, want true")
	}
	if cfg.ThresholdTokens != 90000 {
		t.Fatalf("ThresholdTokens = %d, want 90000", cfg.ThresholdTokens)
	}
	if cfg.KeepRecentTurns != 12 {
		t.Fatalf("KeepRecentTurns = %d, want 12", cfg.KeepRecentTurns)
	}
	if cfg.SummaryMaxTokens != 3000 {
		t.Fatalf("SummaryMaxTokens = %d, want 3000", cfg.SummaryMaxTokens)
	}
	if !cfg.RotateSessionOnCompact {
		t.Fatal("RotateSessionOnCompact = false, want true")
	}
}

func TestParseContextGuardConfigDisabled(t *testing.T) {
	_, ok := parseContextGuardConfig(map[string]any{"enabled": false})
	if ok {
		t.Fatal("disabled context guard should return ok=false")
	}
}

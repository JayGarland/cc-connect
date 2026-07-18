package codex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNew_ParsesProjectEnvFromOpts verifies that env vars declared under
// [projects.agent.options.env] in config.toml are loaded into the agent's
// configEnv field. Without this, user-scoped env (e.g. HTTPS_PROXY in the
// shell that launched cc-connect) silently overrides the values intended
// for the codex subprocess.
//
// Regression for: codex agent ignoring opts["env"] in factory.
func TestNew_ParsesProjectEnvFromOpts(t *testing.T) {
	// Use "go" as cliBin to satisfy exec.LookPath without requiring codex
	// to be installed on the test runner.
	opts := map[string]any{
		"work_dir": t.TempDir(),
		"cmd":      "go",
		"env": map[string]string{
			"HTTPS_PROXY": "http://127.0.0.1:10808",
			"HTTP_PROXY":  "http://127.0.0.1:10808",
			"ALL_PROXY":   "http://127.0.0.1:10808",
		},
	}

	a, err := New(opts)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	agent := a.(*Agent)
	agent.mu.RLock()
	got := envSliceToMap(agent.configEnv)
	agent.mu.RUnlock()

	if len(got) != 3 {
		t.Fatalf("expected 3 env vars, got %d: %v", len(got), agent.configEnv)
	}
	if v := got["HTTPS_PROXY"]; v != "http://127.0.0.1:10808" {
		t.Errorf("HTTPS_PROXY = %q, want http://127.0.0.1:10808", v)
	}
	if v := got["ALL_PROXY"]; v != "http://127.0.0.1:10808" {
		t.Errorf("ALL_PROXY = %q, want http://127.0.0.1:10808", v)
	}
}

// TestNew_ParsesProjectEnvFromMapStringAny covers the TOML decoder path
// where the env table arrives as map[string]any rather than map[string]string.
func TestNew_ParsesProjectEnvFromMapStringAny(t *testing.T) {
	opts := map[string]any{
		"work_dir": t.TempDir(),
		"cmd":      "go",
		"env": map[string]any{
			"OPENAI_BASE_URL": "https://api.example.com/v1",
			"CUSTOM_FLAG":     "yes",
		},
	}

	a, err := New(opts)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	agent := a.(*Agent)
	agent.mu.RLock()
	got := envSliceToMap(agent.configEnv)
	agent.mu.RUnlock()

	if v := got["OPENAI_BASE_URL"]; v != "https://api.example.com/v1" {
		t.Errorf("OPENAI_BASE_URL = %q", v)
	}
	if v := got["CUSTOM_FLAG"]; v != "yes" {
		t.Errorf("CUSTOM_FLAG = %q", v)
	}
}

// TestNew_NoEnvOpts ensures the absence of an env block produces an empty
// configEnv slice (no panics, no surprise inheritance).
func TestNew_NoEnvOpts(t *testing.T) {
	opts := map[string]any{
		"work_dir": t.TempDir(),
		"cmd":      "go",
	}

	a, err := New(opts)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	agent := a.(*Agent)
	agent.mu.RLock()
	defer agent.mu.RUnlock()

	if len(agent.configEnv) != 0 {
		t.Fatalf("expected 0 env vars, got %d: %v", len(agent.configEnv), agent.configEnv)
	}
}

func TestNew_ParsesProjectPromptsFromOpts(t *testing.T) {
	opts := map[string]any{
		"work_dir":             t.TempDir(),
		"cli_path":             "go",
		"system_prompt":        "You are Linear Reporter.",
		"append_system_prompt": "Always use linear-bug-intake.",
	}

	a, err := New(opts)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	agent := a.(*Agent)
	agent.mu.RLock()
	defer agent.mu.RUnlock()

	if agent.systemPrompt != "You are Linear Reporter." {
		t.Fatalf("systemPrompt = %q", agent.systemPrompt)
	}
	if agent.appendPrompt != "Always use linear-bug-intake." {
		t.Fatalf("appendPrompt = %q", agent.appendPrompt)
	}
}

func TestSyncArchiveFirstAGENTSMD_ConsumesRehydrationDigest(t *testing.T) {
	workDir := t.TempDir()
	personasDir := t.TempDir()
	preambleDir := filepath.Join(personasDir, "_preamble")
	if err := os.MkdirAll(preambleDir, 0o755); err != nil {
		t.Fatalf("mkdir preamble dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(preambleDir, "archive-first.write.md"), []byte("ARCHIVE FIRST"), 0o644); err != nil {
		t.Fatalf("write preamble: %v", err)
	}
	digest := strings.Repeat("REHYDRATION_DIGEST ", 2000)

	syncArchiveFirstAGENTSMD(workDir, []string{
		"CC_PERSONAS_DIR=" + personasDir,
		"CC_PERSONA_CLASS=write",
		"CC_REHYDRATION_DIGEST=" + digest,
	})

	data, err := os.ReadFile(filepath.Join(workDir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "ARCHIVE FIRST") {
		t.Fatal("expected AGENTS.md to contain archive-first preamble")
	}
	if strings.Contains(content, digest) {
		t.Fatal("expected AGENTS.md to exclude session-scoped rehydration digest")
	}
}

func TestSyncArchiveFirstAGENTSMD_IncludesSystemPromptAndSeatPersona(t *testing.T) {
	workDir := t.TempDir()
	personasDir := t.TempDir()
	preambleDir := filepath.Join(personasDir, "_preamble")
	if err := os.MkdirAll(preambleDir, 0o755); err != nil {
		t.Fatalf("mkdir preamble dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(preambleDir, "archive-first.write.md"), []byte("WRITE PREAMBLE"), 0o644); err != nil {
		t.Fatalf("write preamble: %v", err)
	}
	if err := os.WriteFile(filepath.Join(personasDir, "dev-pro.md"), []byte("DEV PRO PERSONA"), 0o644); err != nil {
		t.Fatalf("write persona: %v", err)
	}

	syncArchiveFirstAGENTSMD(workDir, []string{
		"CC_PROJECT=dev-pro",
		"CC_PERSONAS_DIR=" + personasDir,
		"CC_PERSONA_CLASS=write",
	})

	data, err := os.ReadFile(filepath.Join(workDir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	content := string(data)

	systemMarker := "You are running inside cc-connect"
	systemIndex := strings.Index(content, systemMarker)
	preambleIndex := strings.Index(content, "WRITE PREAMBLE")
	personaIndex := strings.Index(content, "DEV PRO PERSONA")
	if systemIndex < 0 || preambleIndex < 0 || personaIndex < 0 {
		t.Fatalf("AGENTS.md must contain system rules, preamble, and seat persona, got:\n%s", content)
	}
	if systemIndex >= preambleIndex || preambleIndex >= personaIndex {
		t.Fatalf("expected order system → preamble → persona, got indexes %d/%d/%d\n%s",
			systemIndex, preambleIndex, personaIndex, content)
	}
}

func TestSyncArchiveFirstAGENTSMD_SystemPromptOnlyWithoutPersonaEnv(t *testing.T) {
	workDir := t.TempDir()
	syncArchiveFirstAGENTSMD(workDir, nil)

	data, err := os.ReadFile(filepath.Join(workDir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	if !strings.Contains(string(data), "You are running inside cc-connect") {
		t.Fatalf("expected system rules even without persona env, got:\n%s", data)
	}
}

func TestPromptFootprint_CountsManagedStaticAndRuntimeDigest(t *testing.T) {
	personasDir := t.TempDir()
	preambleDir := filepath.Join(personasDir, "_preamble")
	if err := os.MkdirAll(preambleDir, 0o755); err != nil {
		t.Fatalf("mkdir preamble: %v", err)
	}
	if err := os.WriteFile(filepath.Join(preambleDir, "archive-first.write.md"), []byte("WRITE PREAMBLE"), 0o644); err != nil {
		t.Fatalf("write preamble: %v", err)
	}
	if err := os.WriteFile(filepath.Join(personasDir, "architect-codex.md"), []byte("CODEX PERSONA"), 0o644); err != nil {
		t.Fatalf("write persona: %v", err)
	}

	agent := &Agent{
		workDir:      t.TempDir(),
		systemPrompt: "SYS",
		appendPrompt: "APPEND",
		sessionEnv: []string{
			"CC_PROJECT=architect-codex",
			"CC_PERSONAS_DIR=" + personasDir,
			"CC_PERSONA_CLASS=write",
			"CC_REHYDRATION_DIGEST=" + strings.Repeat("DIGEST ", 100),
		},
	}

	fp := agent.PromptFootprint()
	if fp.StaticTokens <= 0 {
		t.Fatalf("StaticTokens = %d, want > 0", fp.StaticTokens)
	}
	if fp.SessionTokens <= 0 {
		t.Fatalf("SessionTokens = %d, want > 0 (runtime preamble/digest)", fp.SessionTokens)
	}
	if fp.Total() != fp.StaticTokens+fp.SessionTokens {
		t.Fatalf("Total() = %d, want %d", fp.Total(), fp.StaticTokens+fp.SessionTokens)
	}
}

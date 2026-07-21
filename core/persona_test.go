package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolvePersonaClass(t *testing.T) {
	cases := []struct {
		name                string
		project             string
		hasWorkspacePattern bool
		want                PersonaClass
	}{
		{"secretary always secretary", "secretary-seat", false, PersonaClassSecretary},
		{"secretary with workspace pattern still secretary", "secretary-seat", true, PersonaClassSecretary},
		{"execution seat with workspace pattern is write", "dev-pro", true, PersonaClassWrite},
		{"non-execution seat is read", "reviewer-seat", false, PersonaClassRead},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolvePersonaClass(tc.project, tc.hasWorkspacePattern); got != tc.want {
				t.Errorf("ResolvePersonaClass(%q, %v) = %q, want %q", tc.project, tc.hasWorkspacePattern, got, tc.want)
			}
		})
	}
}

func TestComposePersona_UsesPreambleFile(t *testing.T) {
	tmpDir := t.TempDir()
	preambleDir := filepath.Join(tmpDir, "_preamble")
	if err := os.MkdirAll(preambleDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(preambleDir, "archive-first.write.md"), []byte("WRITE_PREAMBLE"), 0o644); err != nil {
		t.Fatalf("write preamble: %v", err)
	}

	got := ComposePersona(tmpDir, PersonaClassWrite, "PERSONA_BODY", "F:\\test-archive", "")
	if !strings.HasPrefix(got, "WRITE_PREAMBLE") {
		t.Errorf("expected preamble at head, got:\n%s", got)
	}
	if !strings.Contains(got, "PERSONA_BODY") {
		t.Errorf("expected persona body present, got:\n%s", got)
	}
	if strings.Index(got, "WRITE_PREAMBLE") > strings.Index(got, "PERSONA_BODY") {
		t.Errorf("expected preamble before persona body, got:\n%s", got)
	}
}

func TestComposePersona_FallsBackWhenPreambleMissing(t *testing.T) {
	tmpDir := t.TempDir() // no _preamble dir at all

	got := ComposePersona(tmpDir, PersonaClassRead, "PERSONA_BODY", "F:\\test-archive", "")
	want := archiveFirstFallback("F:\\test-archive", "")
	if !strings.HasPrefix(got, want) {
		t.Errorf("expected fallback at head, got:\n%s", got)
	}
	if !strings.Contains(got, "PERSONA_BODY") {
		t.Errorf("expected persona body still present, got:\n%s", got)
	}
}

func TestComposePersona_EmptyPersonaContentReturnsOnlyPreamble(t *testing.T) {
	got := ComposePersona("", PersonaClassRead, "", "F:\\test-archive", "")
	want := archiveFirstFallback("F:\\test-archive", "")
	if got != want {
		t.Errorf("expected bare fallback preamble, got:\n%s", got)
	}
}

func TestArchiveFirstFallback_UnknownArchiveDirDoesNotGuessPath(t *testing.T) {
	got := archiveFirstFallback("", "")
	if strings.Contains(got, `F:\`) || strings.Contains(got, "docs/archive") || strings.Contains(got, "docs\\archive") {
		t.Errorf("fallback with unknown archiveDir must not guess a path, got:\n%s", got)
	}
	if !strings.Contains(got, "archive_dir") {
		t.Errorf("expected fallback to point at the archive_dir config key, got:\n%s", got)
	}
}

func TestArchiveFirstFallback_ConfiguredTemplateOverridesWording(t *testing.T) {
	got := archiveFirstFallback("F:\\test-archive", "CUSTOM_WORDING {ARCHIVE_DIR} CUSTOM_TAIL")
	want := "CUSTOM_WORDING F:\\test-archive CUSTOM_TAIL"
	if got != want {
		t.Errorf("archiveFirstFallback with custom template = %q, want %q", got, want)
	}
}

func TestArchiveFirstFallback_EmptyTemplateUsesBuiltinDefault(t *testing.T) {
	got := archiveFirstFallback("F:\\test-archive", "")
	if !strings.Contains(got, "你是无状态的壳") || !strings.Contains(got, "F:\\test-archive") {
		t.Errorf("expected built-in default wording with archiveDir substituted, got:\n%s", got)
	}
}

func TestSyncManagedBlock_CreatesFileWhenMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "AGENTS.md")

	if err := SyncManagedBlock(path, ArchiveFirstMarkerStart, ArchiveFirstMarkerEnd, "hello"); err != nil {
		t.Fatalf("SyncManagedBlock: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, ArchiveFirstMarkerStart) || !strings.Contains(content, "hello") || !strings.Contains(content, ArchiveFirstMarkerEnd) {
		t.Errorf("expected bounded block, got:\n%s", content)
	}
}

func TestSyncManagedBlock_PreservesSurroundingContentAndOverwritesBlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "AGENTS.md")
	initial := "# Project notes\n\nSome human-written content.\n\n" +
		ArchiveFirstMarkerStart + "\nOLD_PREAMBLE\n" + ArchiveFirstMarkerEnd +
		"\n\nMore human content below.\n"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	if err := SyncManagedBlock(path, ArchiveFirstMarkerStart, ArchiveFirstMarkerEnd, "NEW_PREAMBLE"); err != nil {
		t.Fatalf("SyncManagedBlock: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	content := string(data)
	if strings.Contains(content, "OLD_PREAMBLE") {
		t.Errorf("expected old block content replaced, got:\n%s", content)
	}
	if !strings.Contains(content, "NEW_PREAMBLE") {
		t.Errorf("expected new block content present, got:\n%s", content)
	}
	if !strings.Contains(content, "Some human-written content.") || !strings.Contains(content, "More human content below.") {
		t.Errorf("expected surrounding human content preserved, got:\n%s", content)
	}
}

func TestLoadComposedPersonaFromEnv(t *testing.T) {
	personasDir := t.TempDir()
	preambleDir := filepath.Join(personasDir, "_preamble")
	if err := os.MkdirAll(preambleDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(preambleDir, "archive-first.write.md"), []byte("PREAMBLE"), 0o644); err != nil {
		t.Fatalf("write preamble: %v", err)
	}
	if err := os.WriteFile(filepath.Join(personasDir, "dev-pro.md"), []byte("PERSONA"), 0o644); err != nil {
		t.Fatalf("write persona: %v", err)
	}

	got := LoadComposedPersonaFromEnv([]string{
		"CC_PROJECT=dev-pro",
		"CC_PERSONAS_DIR=" + personasDir,
		"CC_PERSONA_CLASS=write",
		"CC_ARCHIVE_DIR=F:\\test-archive",
		"CC_ARCHIVE_FIRST_FALLBACK=UNUSED_SINCE_PREAMBLE_FILE_EXISTS {ARCHIVE_DIR}",
	})
	if !strings.Contains(got, "PREAMBLE") || !strings.Contains(got, "PERSONA") {
		t.Fatalf("LoadComposedPersonaFromEnv = %q", got)
	}
	if EnvValue([]string{"CC_REHYDRATION_DIGEST=abc"}, "CC_REHYDRATION_DIGEST") != "abc" {
		t.Fatal("EnvValue failed")
	}
	if got := EnvValue([]string{
		"CC_REHYDRATION_DIGEST=old",
		"CC_REHYDRATION_DIGEST=new",
	}, "CC_REHYDRATION_DIGEST"); got != "new" {
		t.Fatalf("EnvValue last-wins = %q, want new", got)
	}
}

func TestPersonaNameFromEnv_PrefersExplicitPersona(t *testing.T) {
	got := PersonaNameFromEnv([]string{
		"CC_PROJECT=dev-pro",
		"CC_PERSONA=dev",
	})
	if got != "dev" {
		t.Fatalf("PersonaNameFromEnv = %q, want dev", got)
	}
}

func TestPersonaNameFromEnv_FallsBackToProject(t *testing.T) {
	got := PersonaNameFromEnv([]string{"CC_PROJECT=dev-pro"})
	if got != "dev-pro" {
		t.Fatalf("PersonaNameFromEnv = %q, want dev-pro", got)
	}
}

func TestLoadComposedPersonaFromEnv_UsesExplicitPersona(t *testing.T) {
	personasDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(personasDir, "dev.md"), []byte("CANONICAL DEV PERSONA"), 0o644); err != nil {
		t.Fatalf("write persona: %v", err)
	}

	got := LoadComposedPersonaFromEnv([]string{
		"CC_PROJECT=dev-pro",
		"CC_PERSONA=dev",
		"CC_PERSONAS_DIR=" + personasDir,
	})
	if !strings.Contains(got, "CANONICAL DEV PERSONA") {
		t.Fatalf("LoadComposedPersonaFromEnv = %q, want canonical Persona", got)
	}
}

func TestLoadComposedPersonaFromEnv_PropagatesArchiveFirstFallbackTemplate(t *testing.T) {
	personasDir := t.TempDir() // no _preamble dir at all, so fallback fires

	got := LoadComposedPersonaFromEnv([]string{
		"CC_PROJECT=dev-pro",
		"CC_PERSONAS_DIR=" + personasDir,
		"CC_PERSONA_CLASS=write",
		"CC_ARCHIVE_DIR=F:\\test-archive",
		"CC_ARCHIVE_FIRST_FALLBACK=CONFIGURED_WORDING {ARCHIVE_DIR}",
	})
	if !strings.Contains(got, "CONFIGURED_WORDING F:\\test-archive") {
		t.Fatalf("expected CC_ARCHIVE_FIRST_FALLBACK template to reach the fallback text, got:\n%s", got)
	}
}

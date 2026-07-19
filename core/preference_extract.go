package core

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const preferenceExtractThread = "preference-extract"

// resolvePreferenceTranscript picks the best session log path for a closed
// letter (L-0467 pursuit): prefer an existing chat_history.md beside a
// transcript, then Source-Session-Path from the receipt / RESULT / ledger.
func resolvePreferenceTranscript(archiveRoot string, letter string, receipt receiptRecord, provenancePath string) string {
	candidates := make([]string, 0, 6)
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		for _, existing := range candidates {
			if existing == p {
				return
			}
		}
		candidates = append(candidates, p)
	}
	add(receipt.SourceSessionPath)
	add(provenancePath)
	if archiveRoot != "" && letter != "" {
		if info, body, err := resolveLetterResult(filepath.Join(archiveRoot, "threads"), letter); err == nil {
			add(declaredSourceSessionPath(string(body)))
			_ = info
		}
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			// Prefer sibling chat_history.md when the recorded path is a JSONL transcript.
			if strings.EqualFold(filepath.Ext(c), ".jsonl") {
				sibling := filepath.Join(filepath.Dir(c), chatHistoryFileName)
				if st2, err2 := os.Stat(sibling); err2 == nil && !st2.IsDir() {
					return sibling
				}
			}
			return c
		}
		// If candidate is a workspace dir, use its chat_history.md.
		ch := filepath.Join(c, chatHistoryFileName)
		if st, err := os.Stat(ch); err == nil && !st.IsDir() {
			return ch
		}
	}
	return ""
}

func buildPreferenceExtractQuery(id, parentLetter, transcriptPath, date string) string {
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("ID: " + id + "\n")
	b.WriteString("Thread: " + preferenceExtractThread + "\n")
	b.WriteString("Parent: " + parentLetter + "\n")
	b.WriteString("Type: QUERY\n")
	b.WriteString("To: reviewer\n")
	b.WriteString("From: secretary\n")
	b.WriteString("Route: flash\n")
	b.WriteString("Project: nexus\n")
	b.WriteString("Source-Session-Path: " + transcriptPath + "\n")
	b.WriteString("Date: " + date + "\n")
	b.WriteString("---\n\n")
	b.WriteString("## Context Digest\n")
	b.WriteString("Boss closed letter `" + parentLetter + "` and opted into preference extraction via the Telegram close-success card (L-0467). ")
	b.WriteString("Session log path is declared in `Source-Session-Path` (Telegram `chat_history.md` or IDE `transcript.jsonl`).\n\n")
	b.WriteString("## Query\n")
	b.WriteString("1. Read the session log at `Source-Session-Path` (support `.md` / `.txt` and Claude/IDE `.jsonl` with `type=user|assistant`).\n")
	b.WriteString("2. Extract at most 3 durable Boss preference observations across the 8 slugs: ")
	b.WriteString("`directive_style`, `time_memory`, `evidence_bar`, `documentation_appetite`, `cost_token`, `approval_boundary`, `communication_density`, `domain_preferences`.\n")
	b.WriteString("3. APPEND only to `F:\\nexus\\docs\\candidates\\boss-profile-candidates.md` using the Observation / Evidence / Scope / Proposed classification template and `.boss-profile-candidates.lock`.\n")
	b.WriteString("4. Fail-closed skip if the log path or content indicates personal domain (`p-*`, `F:\\personal\\`). Never write `boss-profile.md` or `working_memory.md`.\n\n")
	b.WriteString("## Expected Output\n")
	b.WriteString("RESULT with Status DONE/STUCK/BLOCKED: list appended candidate scopes (or skip reason). Budget: 1 round.\n")
	return b.String()
}

type nextLetterIDResult struct {
	NextID string `json:"next_id"`
}

func allocateNextLetterID(archiveRoot string) (string, error) {
	script := filepath.Join(archiveRoot, "scripts", "next-letter-id.ps1")
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-File", script, "-ArchiveRoot", archiveRoot)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("next-letter-id: %v: %s", err, strings.TrimSpace(string(out)))
	}
	var parsed nextLetterIDResult
	if err := json.Unmarshal(out, &parsed); err != nil {
		return "", fmt.Errorf("parse next-letter-id: %v (output: %s)", err, strings.TrimSpace(string(out)))
	}
	if !dispatchLetterRe.MatchString(parsed.NextID) {
		return "", fmt.Errorf("invalid next letter id %q", parsed.NextID)
	}
	return parsed.NextID, nil
}

func registerArchiveQuery(archiveRoot, letterPath, summary string) error {
	script := filepath.Join(archiveRoot, "scripts", "archive-daily.ps1")
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-File", script,
		"-ArchiveRoot", archiveRoot,
		"-LetterPath", letterPath,
		"-Summary", sanitizeArchiveSummary(summary),
		"-CommitAuthor", "Secretary <secretary@nexus.local>",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("archive-daily QUERY: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func writePreferenceExtractQueryFile(archiveRoot, id, parentLetter, transcriptPath string) (string, error) {
	threadDir := filepath.Join(archiveRoot, "threads", preferenceExtractThread)
	if err := os.MkdirAll(threadDir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(threadDir, id+".query.md")
	body := buildPreferenceExtractQuery(id, parentLetter, transcriptPath, time.Now().Format("2006-01-02"))
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

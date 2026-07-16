# `/letter` Pull Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Resolve a Boss-specified RESULT deterministically and inject its source plus an optional question into the invoking Secretary session.

**Architecture:** Extend the Engine built-in command router with `letter`. A small resolver uses `NotifyConfig.threadsDir()` and the existing `scanResultFiles` discovery rules to find one exact L-ID, then replaces `Message.Content` with one complete source envelope and lets the normal agent pipeline run. No platform send is used for the source itself.

**Tech Stack:** Go; `core.Engine`; existing RESULT scanner and Engine tests.

## Global Constraints

- `/letter` does not read INDEX or use keyword matching.
- It does not use Inbox, receipt state, or `receipt_session_key`.
- RESULT bytes are the source; do not create snapshots or hashes.
- Errors reply once and do not call the agent.

---

### Task 1: Exact RESULT resolver and envelope

**Files:**
- Modify: `core/notify.go`
- Modify: `core/notify_test.go`

**Interfaces:**
- Produces: `resolveLetterResult(threadsDir, letter string) (resultFileInfo, []byte, error)` and `formatLetterSourceEnvelope(letter, path string, source []byte, query string) string`.

- [x] Add failing tests for one exact RESULT, absent RESULT, duplicate L-ID across threads, and envelope query omission/inclusion.
- [x] Run `go test ./core -run 'TestResolveLetterResult|TestFormatLetterSourceEnvelope' -count=1`; confirm missing functions fail.
- [x] Implement strict L-ID validation, scan once through `scanResultFiles`, require exactly one match, read it once, and format the fixed envelope.
- [x] Re-run the focused tests; confirm pass.

### Task 2: Command routing and no-agent error behavior

**Files:**
- Modify: `core/engine.go`
- Modify: `core/engine_test.go`

**Interfaces:**
- Consumes: resolver/envelope from Task 1.
- Produces: `/letter L-#### [question]` as an internal context substitution that returns `false` from `handleCommand` so normal session processing calls the agent.

- [x] Add command tests proving valid `/letter` reaches the agent with one complete source envelope, optional query, original session key, and no platform source message; invalid/missing RESULT sends an error and does not reach the agent.
- [x] Run the focused Engine tests; confirm the former receipt-ledger implementation did not meet direct-source behavior.
- [x] Route `letter` in `handleCommand` to `cmdLetter`, which mutates only `msg.Content` on success.
- [x] Re-run focused Engine tests; confirm pass.

### Task 3: Regression verification

**Files:**
- Modify: this plan, marking completed checkboxes.

- [x] Run `go test ./core -count=1`, `go test ./platform/telegram -count=1`, `go vet ./core`, and `git diff --check`.
- [x] Commit code and completed plan with `feat(letter): inject result source by id`.

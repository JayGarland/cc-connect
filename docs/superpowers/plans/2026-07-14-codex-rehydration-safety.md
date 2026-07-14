# Codex Rehydration Safety Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Keep Codex rehydration state session-scoped and ensure every app-server process receives it once, including a resumed process.

**Architecture:** The managed `AGENTS.md` block remains for durable instructions only. The existing first-turn app-server preamble carries session-scoped rehydration content, and preamble state is owned by the process rather than inferred from whether it resumes a remote thread.

**Tech Stack:** Go, Codex app-server adapter, Go standard testing.

## Global Constraints

- Do not modify production configuration or restart, push, merge, or open a PR.
- Defer L-0418 P1-A static parity and P1-C context-footprint accounting without source changes.
- Preserve the managed-block preamble/persona behavior delivered by L-0416.

---

### Task 1: Keep rehydration outside managed AGENTS.md

**Files:**
- Modify: `agent/codex/project_env_test.go`
- Modify: `agent/codex/codex.go`

**Interfaces:**
- Consumes: `syncArchiveFirstAGENTSMD(workDir string, extraEnv []string)`.
- Produces: a managed block containing only durable persona/preamble content.

- [ ] **Step 1: Write the failing test**

Change `TestSyncArchiveFirstAGENTSMD_ConsumesRehydrationDigest` to assert that `ARCHIVE FIRST` remains present and `REHYDRATION_DIGEST` is absent after calling `syncArchiveFirstAGENTSMD` with `CC_REHYDRATION_DIGEST`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/codex -run TestSyncArchiveFirstAGENTSMD_ConsumesRehydrationDigest -count=1`

Expected: FAIL because the current managed block includes the digest.

- [ ] **Step 3: Write minimal implementation**

Remove digest extraction and concatenation from `syncArchiveFirstAGENTSMD`, leaving its durable content sourced only from `core.ComposePersona`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/codex -run TestSyncArchiveFirstAGENTSMD_ConsumesRehydrationDigest -count=1`

Expected: PASS.

### Task 2: Inject session rehydration once per app-server process

**Files:**
- Modify: `agent/codex/appserver_session_test.go`
- Modify: `agent/codex/appserver_session.go`
- Modify: `agent/codex/codex.go`

**Interfaces:**
- Consumes: `buildCodexPromptPreamble(systemPrompt, appendPrompt string)` and `prependCodexPromptPreamble(prompt, preamble string)`.
- Produces: a first-turn preamble that includes `CC_REHYDRATION_DIGEST`, irrespective of a resume ID, and is never duplicated on later turns in that process.

- [ ] **Step 1: Write the failing tests**

Add tests that construct fresh and resumed `appServerSession` values with a non-empty runtime preamble, assert both begin with `preambleSent == false`, and assert the first turn prepends the digest while a second turn does not.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./agent/codex -run 'TestAppServerSession_(Fresh|Resumed).*Preamble' -count=1`

Expected: resumed-session assertion fails because construction currently sets `preambleSent` from the resume ID.

- [ ] **Step 3: Write minimal implementation**

Extract `CC_REHYDRATION_DIGEST` before removing it from the child environment; append it to the app-server `promptPreamble`; initialize `preambleSent` to false for every process.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./agent/codex -run 'TestAppServerSession_(Fresh|Resumed).*Preamble' -count=1`

Expected: PASS.

### Task 3: Verify scoped behavior and record the deferred work

**Files:**
- Modify: `docs/superpowers/plans/2026-07-14-codex-rehydration-safety.md`

- [ ] **Step 1: Run focused adapter suite**

Run: `go test ./agent/codex/... -count=1`

Expected: PASS.

- [ ] **Step 2: Run repository build**

Run: `go build ./...`

Expected: exit code 0.

- [ ] **Step 3: Record deferrals**

Mark P1-A and P1-C deferred in the L-0418 RESULT rather than changing their code in this safety pass.

- [ ] **Step 4: Commit**

Run: `git add agent/codex/codex.go agent/codex/project_env_test.go agent/codex/appserver_session.go agent/codex/appserver_session_test.go docs/superpowers/plans/2026-07-14-codex-rehydration-safety.md; git commit --author="architect-claude <architect-claude@resonova.local>" -m "fix(codex): scope rehydration to each process"`


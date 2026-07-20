# Letter Delivery Phase 1/2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Inbox and Outbox restart-safe and content-addressed without changing their public Telegram commands.

**Architecture:** Persist Outbox card/lifecycle state in `outbox_ledger.json`, use SHA-256 content digests for callback generations, and reconcile incomplete Inbox/Outbox platform effects. Archive files and `dispatch_ledger.json` remain the authoritative protocol/execution sources.

**Tech Stack:** Go 1.25, existing Engine/Telegram interfaces, JSON, `AtomicWriteFile`.

## Global Constraints

- No configuration, deployment, restart, push, or merge.
- All new behavior begins with a focused failing Go test.
- Persist state before hiding a state-changing Outbox action.
- Platform I/O must use a bounded context outside `outboxMu`.

---

### Task 1: Content identity and terminal discovery

**Files:**
- Modify: `core/notify.go`, `core/outbox.go`, `core/notify_test.go`, `core/outbox_test.go`

- [ ] Add failing tests: mtime-only changes retain one generation; changed bytes alter a generation; a `result.md` suppresses an Outbox QUERY without an INDEX RESULT row.
- [ ] Run `go test ./core -run 'Test(Notify.*Digest|Outbox.*Result)' -count=1`; expect failure because digest terminal behavior is absent.
- [ ] Implement `contentDigest`, add digest to file info/records, and make Outbox terminal discovery read result filenames.
- [ ] Re-run the focused tests; expect PASS.

### Task 2: Durable Outbox ledger and restart reconciliation

**Files:**
- Modify: `core/outbox.go`, `core/outbox_test.go`

- [ ] Add failing tests: a stored card locator reloads after a fresh Engine; failed manual-state save does not hide the item; failed send is retried by reconciliation.
- [ ] Run `go test ./core -run 'Test(Outbox.*Restart|Outbox.*Manual|Outbox.*Retry)' -count=1`; expect failure because no ledger exists.
- [ ] Implement `outboxStore`, versioned `outbox_ledger.json`, one-time manual-file migration, atomic state transitions, and retry metadata.
- [ ] Re-run the focused tests; expect PASS.

### Task 3: Inbox delivery recovery and lock-safe card effects

**Files:**
- Modify: `core/notify.go`, `core/notify_test.go`, `core/outbox.go`, `core/outbox_test.go`

- [ ] Add failing tests: a current Inbox receipt lacking a card is retried after restart; a closed receipt does not reopen from an updated result; Outbox send/delete does not run under `outboxMu`.
- [ ] Run `go test ./core -run 'Test(Notify.*Recovery|Notify.*Closed|Outbox.*Lock)' -count=1`; expect failure because reconciliation is absent.
- [ ] Implement pending-delivery reconciliation, terminal-close guard, and plan/execute split for Outbox effects with 30-second contexts.
- [ ] Re-run the focused tests; expect PASS.

### Task 4: Regression verification and pause handoff

**Files:**
- Modify: `docs/telegram.md` only if commands/behavior need clarification.

- [ ] Run `go test ./core -count=1`, `go vet ./core`, `go build ./cmd/cc-connect`, and `git diff --check`.
- [ ] Re-read the approved spec and record any unmet requirement in the RESULT rather than claiming success.
- [ ] Commit the Phase 1/2 implementation on `codex/L-0479-delivery-ledger`; do not push, deploy, restart, merge, or start Phase 3/4.

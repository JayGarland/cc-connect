# Letter Delivery Phase 1/2 Design

## Goal

Eliminate the production Inbox/Outbox reliability gaps before the later unified-ledger and incremental-scanner work: cards and cleanup survive restart, result delivery cannot be permanently skipped after a crash, and content—not mtime—defines a card generation.

## Scope

Phase 1/2 changes only the cc-connect feature branch `codex/L-0479-delivery-ledger`. It preserves current Telegram commands and configuration, makes no production deployment or restart, and stops after testable Phase 1/2 commits for Boss functional verification.

## Architecture

The archive remains the protocol source of truth. `outbox_ledger.json` becomes the durable Outbox projection alongside the existing `notify_ledger.json`; it is not an archive replacement and does not duplicate `dispatch_ledger.json`.

Each Outbox record is keyed by L-ID and contains the query path/metadata, a SHA-256 content digest, persisted Telegram `MessageLocator`, lifecycle state, attempts, and retry time. A small internal `outboxStore` owns atomic read-modify-write transactions. The existing dispatch ledger remains authoritative for cross-seat dispatch facts.

Inbox keeps its current durable ledger, but its scanner distinguishes an observed file from a successfully materialized delivery. A record with a current digest but no card remains pending and is reconciled on the next watcher pass. Digest equality makes mtime-only touches no-ops; a changed digest before closure supersedes an open delivery. A result update after a recorded close is surfaced as a warning and does not reopen the closed receipt.

## State Transitions

### Outbox

`Pending → DispatchClaimed → Dispatched` and `Pending → ManuallyHandled` are terminal queue outcomes. `CardSent` and `CleanupPending` record Telegram projection state independently. A result file or dispatch-ledger entry suppresses the Outbox projection. Failed sends/deletes receive a bounded retry timestamp; a restart reloads the ledger and resumes reconciliation.

### Inbox

`Observed → PendingDelivery → CardSent → Acknowledged/Forwarded → ClosePending → Closed`. The scanner records `Observed` before external I/O, but only considers the delivery complete once a card locator is stored or a configured non-card delivery completes. `Closed` is immutable for its digest.

## Interfaces

- `contentDigest([]byte) string` returns a SHA-256 hex digest.
- `outboxStore` provides load/save/record transition helpers and persists with `AtomicWriteFile`.
- `reconcileOutbox()` calculates desired cards from archive + dispatch state, then performs platform I/O outside `outboxMu`.
- `reconcileInbox()` ensures a current, unclosed receipt without a card is retried.

## Compatibility

- Existing `outbox_manual.json` values migrate on first load to `ManuallyHandled` Outbox records; the legacy file is retained as a read-only fallback until Phase 3/4 removes it.
- Existing callback payloads continue to include `L-ID generation`; `generation` becomes the digest rather than an RFC3339 mtime.
- Existing `outbox_enabled`, session key, and polling options remain unchanged.

## Failure Handling

- State is committed before a dispatch/manual action becomes hidden from the card.
- Telegram send/delete runs with bounded contexts after releasing internal locks.
- Failed card effects persist `retry_at`; they are retried rather than silently forgotten.
- A duplicate archive L-ID is skipped and logged as an observable conflict; no potentially incorrect card is sent.

## Testing

Tests are written first for: touch-only updates, changed content, restart recovery, failed manual-state persistence, result-file terminal suppression without INDEX RESULT, Inbox seen-before-send crash recovery, and card retry. Existing core suite, focused lifecycle tests, `go vet ./core`, and `go build ./cmd/cc-connect` are required before the Phase 1/2 pause.

## Non-Goals

- No unified Inbox/Outbox ledger in this phase.
- No filesystem-notification dependency or incremental scanner in this phase.
- No daemon deployment, restart, push, merge, or configuration change.

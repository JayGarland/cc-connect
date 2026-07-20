# Single-Writer Delivery Ledger Design

## Decision

`delivery_ledger.json` is the only writable runtime projection. The legacy
`notify_ledger.json`, `outbox_ledger.json`, and `outbox_manual.json` files are
read only migration fallback inputs and are never modified by a migrated
daemon.

## Record model

Every L-ID record embeds a complete Inbox and Outbox runtime projection:

- Inbox: the full receipt lifecycle, card locator, acknowledgement, forwarding,
  close state, summary and source metadata.
- Outbox: the complete query/card/manual/dispatched/attempt/retry state.
- Scanner: archive fingerprints and audit timestamp remain ledger metadata.

The `deliveryStore` mutex serializes every read-modify-write transition.

## Migration

On first startup without a unified ledger, import the three legacy files,
atomically save the combined ledger, and retain the originals untouched. Once
the unified file exists, runtime reads and writes never touch legacy paths.

## Compatibility

Existing Telegram callbacks retain their L-ID/generation shape. Archive and
dispatch ledgers remain protocol truth; the unified ledger is a reconstructible
daemon projection. Old files stay available for one release as read-only
recovery inputs, then may be removed in a later migration.

## Verification

TDD covers migration preservation, restart persistence of Inbox and Outbox
states, no legacy-file mtime/content changes after lifecycle transitions, and
one unified mutex transaction across both watchers. Full core, Telegram, vet,
build and live-daemon checks are required before handoff.

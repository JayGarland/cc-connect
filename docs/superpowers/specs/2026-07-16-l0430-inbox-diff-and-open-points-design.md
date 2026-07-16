# L-0430 Inbox Diff and Open Points Design

## Goal

Make each Telegram Inbox card a deterministic, mobile-readable RESULT envelope. It must show the conclusion summary, any declared open points, and—only when the RESULT content changed since its previous arrival generation—a readable description of that update. No agent is invoked to construct or explain the card.

## Canonical RESULT sections

New RESULT letters use `## Open Points` for unresolved decisions, risks, questions, or follow-up work. `## Open Questions` remains a legacy-compatible input heading. The Inbox UI always labels this information `Open points`.

The parser matches only complete Markdown headings. It does not keyword-search prose. If neither heading exists, the card omits the Open points block; absence means the letter did not declare one, not that code inferred there are none.

`Summary` remains the existing first non-empty paragraph under `## Conclusion` (or `## Blocker` for STUCK/BLOCKED). It is not redesigned by this change.

## Watcher state and change detection

The existing `core/notify.go` RESULT watcher remains the sole arrival mechanism. A file modification time still identifies a generation, and the existing ledger still owns card locator, receipt state, and generation safety.

For each letter, the notify data directory holds exactly one rolling previous Markdown body used solely as a diff base. On a detected content change, the watcher:

1. reads the current `result.md`;
2. compares it with the rolling previous body by level-two Markdown section;
3. stores a `receiptUpdate` payload for the current generation;
4. atomically replaces the rolling previous body with the current body; and
5. updates or sends the existing receipt card according to the established generation lifecycle.

This is a bounded operational cache, not an immutable source snapshot: it has no SHA-256, is never routed to a secretary, retains only one predecessor, and is removed when a letter is closed or reaches the configured cleanup age. The live archive `result.md` remains the only authoritative original.

The current archive repository cannot use `git diff` as this base: RESULT files are untracked live artifacts, so there is no committed predecessor on which `git diff -- <file>` can operate. The cache avoids imposing a Git commit on each pursuit edit.

An mtime-only change whose bytes are identical produces no update payload and does not add update UI.

## Mobile Inbox card

The normal card remains an envelope: L-ID, thread, status, full existing summary, arrival time, and absolute result path. It additionally renders `Open points` directly below the summary when the parsed section exists.

For an updated generation, the card has an `Updated` marker and directly renders a compact `Changes` block when its rendered content fits the Telegram message budget. The block lists changed section names and the current text only; the prior text is used solely for detection and is never rendered, for example:

```text
📬 L-0430 · Updated
Summary: ...

Open points:
• Decide the retention policy.

Changes:
Conclusion
New conclusion.
```

`查看本次更新` is conditional: it is present only if a non-empty update cannot fit in the card's safe text budget. It replaces the card content with the same paginated update view used for long original letters. No button appears for a first arrival, an unchanged generation, or an update already fully shown on the card.

Existing `展开原信` / pagination / `收件` / `交主秘书` semantics remain unchanged. Update viewing never invokes an agent.

## Failure handling

If the cache cannot be read or saved, receipt delivery still proceeds with the normal envelope and no update block. The watcher logs the cache failure and must not suppress or lose the RESULT card. Cache replacement occurs only after the current bytes are successfully available; a failed write leaves the previous base intact for a later retry.

## Verification

- Exact `## Open Points` and legacy `## Open Questions` parse; prose keywords do not.
- A first arrival contains no update UI.
- A pending RESULT update edits the same card and includes parsed open points plus the current text of each changed section, without prior text.
- A short diff has no `查看本次更新` button; a large diff has that button and paginates without agent invocation.
- Identical bytes with a newer mtime produce no update block.
- Cache read/write failure preserves ordinary receipt delivery.
- Acknowledged-generation re-arrival retains existing re-entry semantics and uses the prior body for its update when available.

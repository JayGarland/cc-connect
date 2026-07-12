# L-0404 Permission Lifecycle Completion Design

## Goal

Complete the existing PR #28 permission bridge without creating a second
permission system: background requests must never be denied because an
in-memory FIFO reaches a fixed capacity, and every unresolved stdio request
must receive one denial when its live session ends.

## Existing state and scope

Keep `interactiveState.pending`, `pendingQueue`, and `approveAll` as the only
permission state. Remove the fixed queue-capacity/overflow path and its
platform notification. Background requests append to the existing FIFO; only
the active request is displayed. `allow all` keeps its current shared-session
semantics.

## Lifecycle

An active displayed permission owns a bounded, configurable 60-second TTL.
The timer begins only when the request is made active, never while it waits in
the FIFO. Expiry atomically removes that request, sends exactly one stdio
`deny`, then promotes the next item. Promotion starts a fresh TTL.

`/yolo` is accepted only when an active pending permission exists and routes to
the existing `allow all` resolver. It changes no Claude adapter permission
mode. It approves the active request, all non-question queued requests, and
future non-question requests in the same live session. An AskUserQuestion
still requires an answer; once answered, queued normal requests continue under
the shared allow-all state.

All session-ending paths share one helper: atomically detach active and queued
requests, clear `approveAll`, then issue one idempotent stdio `deny` per
unresolved request. Late TTLs, buttons, and event callbacks are harmless.

## Non-goals

- No `allowed_tools` or Secretary configuration change.
- No cc-connect-specific tool-rule parser.
- No `bypassPermissions` mode change.
- No Telegram typing/progress bridge in this change.

## Acceptance tests

1. More than sixteen unsolicited requests remain queued rather than denied;
   only one prompt is displayed before a response.
2. TTL starts on active display, denies once on expiry, and advances FIFO.
3. `/yolo` approves active, queued, and later ordinary requests without
   changing adapter mode; it falls through with no active request.
4. Stop, cancel, reset, idle close, and cleanup deny each active/queued stdio
   request at most once and clear allow-all state.

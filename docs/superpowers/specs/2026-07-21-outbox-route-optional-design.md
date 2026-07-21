# Outbox Route-Optional Design

## Goal

Allow a registered QUERY without a `Route:` front-matter field to appear in the Outbox; Route remains optional display metadata and is not used to select delivery behavior.

## Scope

`core/scanOutboxQueries` currently rejects a QUERY when `Route` is empty. Remove Route from its required identity fields. The scanner will retain a supplied value verbatim and use `default` solely for card/list presentation when the field is absent.

The scanner remains strict for `ID`, `Type: QUERY`, `Thread`, `To`, and `Date`. A missing or malformed identity field still excludes the file. The change must not parse `ROUTING.md`, introduce a default Route policy, or use Route for dispatch selection.

## Data Flow

1. The Outbox scanner selects registered, unfinished QUERY files from `INDEX.md`.
2. It parses their front matter and validates required identity fields excluding Route.
3. It carries the optional Route string into the Outbox record.
4. Outbox formatting renders the supplied Route, or `default` when absent.

Existing cards with explicit Route keep their current text. A newly scanned route-less QUERY becomes a normal pending Outbox record and can use all existing card actions.

## Audit Boundary

`dispatch.go` validates letter identity and type but does not reject absent Route. `notify.go` consumes optional RESULT metadata and `rehydration.go` reads Parent only; neither has a Route-based notification filter. No changes are required in those paths unless the code audit reveals a direct Route gate missed by the focused scan.

## Failure Handling

The scanner continues to skip malformed files that cannot establish identity. It does not infer missing Route from `ROUTING.md`, config, or a hard-coded map; this avoids coupling a soon-to-be-degraded field to delivery behavior.

## Tests

Add focused `outbox_test.go` coverage for a registered QUERY lacking Route. It must be returned by `scanOutboxQueries`, preserve an empty stored Route, and render `Route: default` in the Outbox card. Existing tests continue to demonstrate that malformed/terminal/dispatched entries are excluded.

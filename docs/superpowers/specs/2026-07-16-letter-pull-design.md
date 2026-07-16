# `/letter` Pull Design

## Goal

Let Boss explicitly name a RESULT letter and optionally ask a question. Code resolves the one current source and injects it into the current Secretary session, eliminating archive searches and path guessing by the agent.

## Command and visibility

The command is `/letter L-#### [question]`.

- The Boss's typed command remains the only request visible in Telegram.
- The expanded envelope and RESULT source are internal agent input in the same cc-connect session; they are not emitted as a separate Telegram message.
- The Secretary's normal reply remains visible in that same conversation.

## Resolution and injection

1. Parse only a strict `L-` followed by digits identifier.
2. Resolve exactly one `threads/**/L-####.result.md` under the configured archive root. Do not consult INDEX and do not do keyword matching.
3. On one match, read the file once and replace the command's internal content with:

```text
[LETTER SOURCE]
L-ID: L-####
Result path: <absolute path>
Instruction: Treat the following as the exact source for this L-ID. Do not search for another copy.
---
<current RESULT bytes>
---
[Boss query]
<only when supplied>
```

4. Continue through the normal message-to-agent pipeline using the command's original session key and reply context.

## Failure and size handling

- Invalid syntax, zero matches, multiple matches, or a read failure return a short error to the invoking chat and never invoke the Secretary.
- A source under the ordinary message budget is injected in one agent input.
- A source above that budget is deterministically split into numbered parts with the same L-ID; no agent file read or archive search is requested.

## Constraints

- No Inbox card, receipt state, or `receipt_session_key` is involved.
- No Telegram Rich Message or additional source-message is sent.
- The current `result.md` is the only source; no snapshot, hash, or copied durable source is introduced.
- Tests must prove exact resolution, internal-only routing, query inclusion, errors without agent invocation, and long-source chunking.

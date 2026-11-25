# ADR-0003: A provider call is retried until it persists; a watch on a live turn always attaches

## Status

Accepted — ratified by PA in round 1 (proposed by SA in round 0), with four
amendments recorded under [Amendments](#amendments-ratified-round-1).
Implementation: BE T-02/T-04, FE T-09, contract text in
`docs/api/websocket.md`.

Citation errata (round 1): the retry gate is `application/job/turn.go:225-227`
(`transient` plus the condition on `226-227`); `interruptedReply` starts at
`turn.go:572`, not `611` — `611` is its body's middle.

Errata (round 3, T-22/D-12): the function named `interruptedReply` below now
lives in the domain as `entity.NewInterruptedReply` (`entity/message.go:190`),
and the call-site line numbers in this record describe the tree at
ratification. The decision is unchanged; only the owner moved.

## Context

Two turn-transport behaviours are decided today by accident rather than by a
statement, and they interlock:

1. **Retry.** `runTurn` stops retrying a transient upstream failure as soon as
   anything reached the client: `application/job/turn.go:226-229` breaks on
   `len(added) > 0 || len(parts) > 0`. `parts` is the *streamed* capture of the
   current attempt; it resets per attempt (`turn.go:217`). So one `429` after the
   first delta ends the turn, even though the attempt committed nothing and a
   retry would leave no duplicate. `retry_test.go:144`
   (`TestRunTurnNoRetryAfterEmittedDelta`) pins that as intended. The turn then
   persists the shown text as an interrupted reply (`turn.go:278-300`,
   `interruptedReply` at `turn.go:611`), so the abandoned attempt becomes
   history and only `turn.resume` can continue.
2. **Attachment.** `turn.watch` answers `turn.resync_required` and **returns**
   when the client's `afterSeq` is ahead of the journal
   (`hub.go:604-611`). On an active turn that leaves the socket unsubscribed:
   the tab gets a snapshot and then never sees the rest of the stream or the
   terminal. The gap branch right below it does attach
   (`hub.go:613-625`), and so does the normal replay path
   (`hub.go:626-634`) — three answers to one question.

Line numbers throughout this section are the round-0 tree; the behaviour is
unchanged by the contract text landed in round 1.

Neither behaviour appears in `docs/api/websocket.md`: the page documents
`turn.phase: retry_wait` as "the harness pauses before retrying a transient
provider failure" (`websocket.md:489-494`) and says nothing about what a client
must do with the abandoned attempt's output, and its `turn.watch` section says
both "Afterwards the socket stays attached and receives live frames"
(`websocket.md:297-300`) and, for gaps only, "still attaches for live frames"
(`websocket.md:307-311`).

## Decision

1. **The retry gate is persistence, not the screen.** A provider call may be
   retried iff the failure is transient (`429` or `>= 500`), the turn was not
   cancelled, the attempt appended no message to the conversation, the retry
   budget is not exhausted, and a provider-asked `Retry-After` is within
   `maxRateLimitWait` (60 s). Streamed-but-uncommitted output never blocks a
   retry; an appended message always does.

2. **Only the surviving attempt is persisted.** A failed attempt's streamed
   text/reasoning is written as an interrupted reply *after* the loop decides
   not to retry. A retry cycle therefore leaves at most one assistant line.

3. **The client owns the abandoned tail.** On `turn.phase {phase:"retry_wait"}`
   the client drops the trailing run of live segments the store cannot hold —
   text, reasoning, and tool rows without a recorded result — back to the last
   **finished step** (`turn.step_finished`, amended in round 1). It never drops
   history rows, the user line, folded steers, or finished-step usage. This is
   only correct because the server persists a step before any further provider
   call starts (`turn.go:322-336`), so a finished step is a safe boundary — and
   a *settled* tool row is not one on its own, since the client settles rows on
   the terminal too (`web/lib/turn.js:23-25`, `:93-96`).

4. **A watch on a live turn always attaches.** Any unsatisfiable `turn.watch` on
   an active turn — unknown-but-running is impossible, so: ahead of the journal,
   or a gap — answers `turn.resync_required` and then joins the subscriber set
   under the same lock. A finished turn answers `turn.resync_required` and
   attaches nothing. The guarantee a client may rely on: *if it received
   `turn.resync_required` for a turn that was active at that moment, it will also
   receive that turn's terminal frame*, unless its own socket dies or its queue
   overflows.

5. **`turn.failed` is self-describing.** Every `turn.failed` carries `status`
   (>= 400), `code` (a member of the catalog), a non-empty display-safe
   `message`, `resyncRequired: true`, and — on send turns —
   `userMessagePersisted` and `steering`. A failure that has nothing to say must
   say so explicitly, never send an empty message.

6. **The compaction retry keeps the stricter gate.** The `413`-driven
   compact-and-retry (`turn.go:257-263`) still requires that the attempt
   streamed and persisted nothing, because a fold rewrites the history the
   client is rendering; relaxing it would need a mid-turn history invalidation
   the protocol does not have.

## Amendments (ratified, round 1)

1. **Boundary = the last `turn.step_finished`, not a settled tool row.** The
   client also settles tool rows when the turn ends (`web/lib/turn.js:22-24`,
   called from the terminal branch at `:40`), so `settled` alone does not mean
   "the store holds it". A finished step does.
2. **`turn.failed` may carry `retryAfterMs`.** When the provider names a pause
   longer than the turn will sit out (`maxRateLimitWait`), the failure carries
   the provider's requested wait so the client does not retry sooner. The field
   is absent on every other failure; `status`, `code`, and a non-empty
   `message` remain mandatory.
3. **The attach rule stands unchanged** — every unsatisfiable watch on a live
   turn answers and then attaches.
4. **A replay bound is part of the contract.** This is the base's own reason,
   not the fork's: `conn.enqueue` kills a socket whose outbound queue is full
   (`infrastructure/delivery/websocket/conversation/conn.go:53-67`,
   `outboundQueueCap = 64` at `conn.go:16`), so a replay larger than the queue
   would kill the watcher and loop it through reconnect → replay → death. A
   watch replays verbatim only while the window fits the socket's free queue
   space **minus the server's headroom for live frames** — an exact fit is not
   enough, the next live delta must still land; a deeper window answers
   `turn.resync_required` and, on a live turn, still attaches. The doc's "verbatim" becomes "verbatim as far as
   the window fits".

## Consequences

### Positive

- A transient `429` in the middle of a long answer costs a pause, not the
  answer; the store never holds two copies of one reply.
- A reloaded or second tab always reaches the terminal frame of a turn it
  attached to, so "the tab sits still until reload" (T-04) stops happening.
- The three `turn.watch` branches collapse to one shape: answer, then attach if
  live.

### Negative

- The client gains a rule it must implement correctly (T-09): dropping persisted
  segments would erase an answer, dropping too little would splice two
  attempts.
- `turn.step_finished` becomes load-bearing for the client's boundary decision,
  although the frame is emitted by the adapter before the message is persisted;
  the ordering is safe only because a failed persist ends the turn — the docs
  must say so.
- Users lose the "interrupted reply survives a 429" behaviour in favour of a
  retry; the interrupted reply now appears only when retries are exhausted.

## Alternatives considered

- **Keep the current gate and rely on `turn.resume`.** Cheapest, but the user
  must press Retry after a pause the harness could have absorbed, and the
  abandoned attempt stays in history as a half answer.
- **Retry and let the client keep both attempts (no reset).** Rejected: the two
  attempts splice into one answer (T-09).
- **Server-side live shadow replayed after a retry** (the private `liveDoc`
  design NusaShell grew). Heavier: the hub would carry a second copy of every
  turn's content, and the reset rule would still be needed for frames the shadow
  drops.
- **Attach only on the normal path and require the client to re-watch after a
  resync.** Works, but every client must know the retry dance, and the doc
  already promises the attach.

# Findings — FE (round 0)

Status: ready
Tickets: T-08, T-09, T-10, T-11

Round 0 changed no source. Every claim below was produced by running the
base's own modules — the real `web/lib/markdown.js` (marked + DOMPurify +
Pele, as vendored) inside the served page, and the real `web/lib/turn.js` /
`web/components/transcript.js` fed with frame sequences and history
fixtures — against `bin/nusallm` on a scratch data root. Throwaway scripts
lived in `/tmp/fe-audit/`; nothing was added to the repo and no git command
that writes was run.

## Reproduction recipe (rerun by QA / round 1)

```sh
# 18081 was already taken by another thread on this host; use any free port.
mkdir -p /tmp/fe-audit/data
printf '{"server":{"port":18091,"hosts":["127.0.0.1","localhost"]},"storage":{"dir":"/tmp/fe-audit/data"}}' > /tmp/fe-audit/config.json
CONFIG_PATH=/tmp/fe-audit/config.json NUSALLM_WEB_DIR=web ./bin/nusallm serve --port 18091
```

Then, in the page (`http://127.0.0.1:18091/`), the app's own modules are
importable and the real transcript can be rendered from a synthetic room:

```js
const md = await import('/lib/markdown.js');            // real renderer
const d = document.createElement('div'); md.renderInto(d, 'text'); d.innerHTML;

const { render, html } = await import('/vendor/preact/htm-preact.mjs');
const { Transcript } = await import('/components/transcript.js');   // real component
const turn = await import('/lib/turn.js');              // real projection
render(html`<${Transcript} room=${room} scroller=${{ current: host }} />`, host);
```

A synthetic `room` only needs `{ messages, live, hist:{status:'ready'},
sel:{}, info:{}, cutHistory }`; `Transcript` renders history + live exactly
as in the app (`transcript.js:147-166`). `node -e "import('…/web/lib/turn.js')"`
works too (Node ≥ 22 syntax detection), which is how the projection-only
cases below were produced.

---

## T-08 — Adjacent text blocks parse as separate Markdown documents

**Verdict: CONFIRMED**
**Severity: user-visible** (splits an interrupted answer's prose; a fenced
block inside it comes out broken). The ticket's *example* flow is wrong and
is corrected below — the defect itself is real and reachable.

**Evidence**

1. Renderer, two documents vs one (real `markdown.js`, `/tmp/fe-audit` DOM
   capture; input is one sentence cut in half):

   | input | DOM |
   |---|---|
   | `'The cache has one'` and `'write path, and it is wrong.'` rendered as two `<Markdown>` items | two sibling roots `<div class="md"><div class="contents"><p>The cache has one</p></div></div>` and `<div class="md"><div class="contents"><p>write path, and it is wrong.</p></div></div>` — measured flex gap between the two items: **16 px** (`transcript.js:226` `gap-4`) |
   | same text joined before render | `<div class="md"><p>The cache has one write path, and it is wrong.</p></div>` |

   A paragraph break that is not in the text, plus a 16 px gap: the reader
   sees two paragraphs; a selection copy carries a block boundary
   (`Range.toString()` = `"The cache has one\nwrite path, and it is wrong."`).

2. Fenced block cut at the boundary (the "broken blocks" claim):

   part A `Here is the fix.\n\n```go\nfunc invalidate() {\n`,
   part B `  cache.drop()\n}\n```\n\nThat is all.`

   * rendered as two items → **2** `.codeblock`s: one labelled `go`
     containing only `func invalidate() {`, one labelled `text` containing
     `\nThat is all.`, plus a paragraph holding the raw `  cache.drop()\n}`.
   * rendered joined → **1** `.codeblock`, label `go`, content
     `func invalidate() {\n  cache.drop()\n}` and a separate paragraph
     `That is all.`

3. Projection of the real record shape that produces adjacency. The
   ticket's flow — one message, `parts [text, tool_call, text]` — does **not**
   produce side-by-side text items: the tool part yields a `tool` item and
   `group()` folds it into an `activity` item (`turn.js:112-133`,
   `turn.js:135-146`), so the reader sees *prose → "Used 1 tool" → prose*.
   Adjacency needs a separating part that produces no item, and the base
   has exactly one writer that does that:

   `interruptedReply` (`application/job/turn.go:568-591`) keeps `text`
   parts and **drops `tool_call` parts** when a provider call fails after
   streaming. A stream that emitted *text → tool_call → text* and then broke
   persists `parts [text, text]` with `finishReason: interrupted`
   (`turn.go:291`, `:319`). Feeding that record to `historyItems` gives:

   ```
   ['user', 'text("Here is the fix.")', 'text("The cause is invalidate().")', 'interrupted']
   ```

   → `group()` returns `['user','text','text','interrupted']` (verified).
   The `interrupted` marker is pushed *after* the message's parts
   (`turn.js:128`), so it never separates the two texts.

   Second, fixture-only writer with the same effect: a `tool_call` part whose
   call is absent from `m.toolCalls` is skipped by `turn.js:116`
   (`if (!c) continue`), and an `injection` part by `turn.js:118-121`.
   Both are store-shape dependent; I could not produce either from today's
   job loop.

4. Live vs stored disagree for the same failed turn: the live stream keeps
   the `turn.tool_call` segment (`turn.js:53-56`) — a "Used 1 tool" row for a
   call the store deliberately dropped — while history shows the two prose
   parts with no tool item at all. Same failure, two different transcripts.

**Root cause**

The projection treats a *persistence* boundary as a *presentation* boundary.
`historyItems` emits one `text` item per `MessagePart` (`turn.js:112`) and
the live path emits one `text` segment per uninterrupted delta run
(`turn.js:37-43`); `transcript.js:218` then renders each item through its
own `<Markdown>` (`transcript.js:22` → `renderInto`), i.e. each item is its
own Markdown document. Nothing joins neighbours, so a part split — which the
server makes for reasons of its own, e.g. dropping an unstreamed tool call —
becomes a paragraph/fence split.

**Owner layer**

`web/` presentation projection (`lib/turn.js`), consumed by
`components/transcript.js`. No backend change: the store's part boundaries
are legitimate, the transcript's use of them is not.

**Proposed fix**

Add `joinText(items)` to `web/lib/turn.js` — fold a `text` item into the
previous item when it is also `text`, keeping the first item's `id` so the
DOM key is stable while streaming — and call it for **both** lists,
immediately before `group()` in `transcript.js:159-160`:

```js
const items = group(joinText(hist));
const liveGroups = group(joinText(liveItems));
```

Boundaries that must **not** be joined (they are separate items and stay
so): `user`, `note`, `steer`, `interrupted`, `activity` (any reasoning/tool),
and — once they exist — plan/media/approval/elicitation. Concretely, the
joined text of two neighbouring items is unsafe when the thing between them
is a real event: a tool ran, the owner steered, a system note landed, or the
turn was interrupted. In the base all of those are already distinct item
kinds, so "join only `text`+`text`" is complete.

Blast radius: one new function in `turn.js`, two call sites in
`transcript.js`; no controller change; DOM keys unchanged for unjoined
items. Copy path: the base has no per-answer Copy action, so only selection
copy is affected — joining removes the artificial block boundary.

Tests (for T-13's harness, projection level, no DOM needed):
`joinText([text A, text B])` → one item with concatenated text and A's id;
`joinText([text, tool, text])` → unchanged; `joinText([text, reasoning,
text])` → unchanged; `historyItems` of an `interruptedReply`-shaped message
(`parts [text, text]`, `finishReason: interrupted`) → one text item after
`joinText`, two before.

**Open question**

Keep the `interrupted` marker as a boundary (proposed), or fold it into the
joined text? A marker between two halves of one interrupted answer is what
tells the reader the answer was cut — it should stay an item, but that is a
presentation decision worth one line in T-06 or the round-1 patch note.

---

## T-09 — Retried attempt leaves its abandoned tail on screen

**Verdict: CONFIRMED** (projection defect), **latent today**: the frame
sequence the ticket prescribes cannot be emitted by this backend's current
retry gate. It becomes live the moment T-02/T-06 loosen that gate.
**Severity: user-visible once retried-after-output is allowed**; today the
same code path shows a phantom tool row after an interrupted failure (see
T-08 §4).

**Evidence**

1. Stub sequence through the real `applyFrame`
   (`/tmp/fe-audit`, verbatim from the ticket plus the input frame a real
   tool call always carries):

   ```
   seq1 turn.delta      "The answer is "
   seq2 turn.tool_call  {callId:"c9", name:"exec"}
   seq3 turn.tool_input {callId:"c9", text:'{"command":"grep -r foo"}'}
   seq4 turn.phase      {phase:"retry_wait", retryAfterMs:45000}
   seq5 turn.delta      "The answer is 42."
   ```

   Segments after each frame (`kind(status)text`):

   ```
   seq1: text("The answer is ")
   seq2: text("The answer is ") | tool(requested)
   seq3: (same)
   seq4: (same)                      ← nothing removed
   seq5: text("The answer is ") | tool(settled) | text("The answer is 42.")
   ```

   Rendered DOM: `<p>The answer is </p>` + `Used 1 tool` + `<p>The answer is 42.</p>`
   in one flex column — the abandoned attempt's prose and its never-answered
   tool row survive, and the retried answer is drawn as a second paragraph
   under them.

2. Worse variant, when the failed attempt ended in text (no tool call):

   ```
   delta "The answer is " → phase(retry_wait) → delta "The answer is 42."
   ```

   `turn.js:37-43` appends the retry into the last segment, which is still
   `text` → **one** segment, text `"The answer is The answer is 42."`. One
   Markdown document, the sentence spliced, and no visible seam at all.

3. Why the backend cannot produce (1) today:
   `application/job/turn.go:214-229` breaks the retry unless
   `len(parts) == 0 && len(added) == 0`, and `parts` accumulates deltas,
   reasoning and tool-call parts (`turn.go:174-192`). So a `retry_wait`
   (`turn.go:245` → `event.go:120-130` → `frames.go:182-186`) can only be
   emitted before anything of that attempt reached the client. Confirmed
   by `TurnStatusRetryWait` having exactly one producer
   (`application/job/turn.go:245`).

4. What the client can know at `retry_wait` time: the frame carries
   `{phase, retryAfterMs}` only (`frames.go:182-186`) — no attempt number,
   no step index, no "persisted up to" marker. `turn.step_finished` carries
   `{finishReason, usage}` (`frames.go:247-250`), also no step index. The
   only durable identity a retry can lean on is *the step boundary itself*:
   `turn.go:312-336` appends and saves each step's messages before the loop
   starts the next provider call, so at a `retry_wait` everything up to the
   last `turn.step_finished` is persisted and everything after it is not.
   `turn.failed`/`resync_required` add nothing usable here
   (`terminalPayload`, `frames.go:252-270`).

**Root cause**

`applyFrame`'s `turn.phase` case stores the phase and nothing else
(`turn.js:50-52`). There is no notion of "this attempt's output is
speculative"; `settleTools` only marks a row settled, it never removes it
(`turn.js:22-24`), so a failed attempt's rows and text stay in the same
segment list the retry appends to.

**Owner layer**

`web/` projection (`lib/turn.js`). The *rule* ("on retry_wait the client
discards the unpersisted tail of the attempt") is T-06's; the base has no
such statement in `docs/api/websocket.md:487-503` — it only says "any
stream frame that follows ends the wait".

**Proposed fix**

Track what the store already holds as a *segment position*, and cut back to
it on `retry_wait`:

* `newTurn` gains `persistedMark: 0`;
* `applyFrame` sets `t.persistedMark = t.segments.length` in
  `case 'turn.step_finished'` and after handling a `turn.steer` /
  `turn.steer_recalled` frame — both are points where the server has already
  committed the preceding output (`turn.go:312-336` for the step,
  `turn.go:109-131` for a folded steer, which appends the user line *before*
  emitting `turn.steer`);
* `case 'turn.phase'`, `if (p.phase === 'retry_wait') t.segments.length = t.persistedMark;`

Tool rows survive the cut correctly: a `turn.tool_call` segment is created
during the model call (before `turn.step_finished`), and its
`turn.tool_output` text lands later but appends *in place*, so cutting to
the mark keeps the row and its persisted output. A steer folded just before
the retry keeps its bubble for the same reason.

This needs no history read and no new frame: it uses the persistence order
the backend already guarantees (`turn.go:312-336`), so it stays correct if
T-02 lets a retried call resume after a delta. It is deliberately *not* the
"drop segments whose tool status is not success/failed" rule: the base has
no `turn.tool_result` frame and no success/failed status vocabulary
(`turn.js:22-24`, `turn.js:74-77`), so that predicate cannot be evaluated
here.

Blast radius: `turn.js` only (`newTurn` gains `persistedMark`, `applyFrame`
three branches). No consumer change. Tests (projection): the two sequences
above → segments after `retry_wait` equal the pre-attempt prefix; a
`retry_wait` in step 1 keeps step 0's text/tool rows *and their output*; a
steer folded between two steps survives a retry in the next step; a
`turn.completed` afterwards still settles rows.

**Open question**

T-06 has since been decided (`docs/audit/decisions.md` D-06): the retry gate
becomes persistence, and on `retry_wait` the client drops the trailing run of
live segments the store cannot hold, back to the last settled step. That
matches the fix above — with one correction D-06's action list still carries:

> "…tool `status` moving `requested → streaming → settled`, so 'settled tool
> row' is the persisted boundary the client already has"
> (`decisions.md`, FE / T-09 bullet)

In this codebase *settled* does not mean *recorded*. `settleTools`
(`turn.js:22-24`) is called when model activity resumes (`turn.js:40`,
`:70`), when a step ends (`:76`), and on either terminal frame (`:80`), and
it settles every unsettled row regardless of whether a result ever arrived —
a call that never returned is settled by `turn.failed` with
`settled: 'Turn ended'` and an empty output. There is no frame that records a
tool result (`grep turn.tool_result web/` → none). So a boundary keyed on
"settled" would treat an abandoned call as persisted and keep it on screen —
exactly the T-09 symptom. The persisted boundary the client actually has is
the **step**: `turn.step_finished`, plus each folded steer (`turn.steer` is
emitted after its user line is committed, `turn.go:109-131`).

Ask: one line in D-06 replacing "the last settled step" with "the last
`turn.step_finished` / folded steer, i.e. what the server has committed", so
round 1 implements the same rule on both sides.

---

## T-10 — Late-watch duplication: live journal vs stored history

**Verdict: CONFIRMED** — both directions are wrong, and which one you get
depends on whether the last user item is the running turn's prompt.
**Severity: user-visible** (duplicated prose/tool run/steer bubble; or the
previous turn's answer disappearing while a turn runs).

**Evidence**

All four cases rendered through the real `Transcript` with a real journal
replay (`turn.js`), messages from the HTTP DTO shape.

Journal used for every case (a turn that already finished step 1 and is
streaming step 2), fed through `applyFrame` from `seq 1`:

```
delta "Step one says the cache is " · tool_call {c2, read_file} · tool_input ·
tool_output "read 40 lines" · step_finished {tool_calls} · delta "wrong because …"
```

History: `u0` user, `a1` assistant ("Earlier answer about the store."),
`u1` user ("check the cache invalidation"), `a2` assistant
(`parts [text, tool_call c2]`, stored call `c2` with output
`read 40 lines (exit 0)`).

1. **`cutHistory = true`** (the path `watch(…, -1)` takes): trim is
   `hist.slice(0, findLastIndex(kind==='user') + 1)` (`transcript.js:150-152`)
   → keeps `u0, a1, u1`, drops `a2`; live replay redraws step 1 + step 2.
   Visible text: correct, one copy. Duplication avoided — **but** the
   stored tool result is gone: the replayed row is `status:'streaming'` with
   only the preview chunk (`turn.js:62-66`), while the store's row is
   `settled` with `read 40 lines (exit 0)`. A late watcher sees a
   *worse* card than history held, for the whole turn's duration.

2. **`cutHistory = false`** (same room state; what a tab that sent the turn
   sees, `chat.js:407`): the session renders

   ```
   … Step one says the cache is  Used 1 tool  Step one says the cache is  Used 1 tool  wrong because …
   ```

   — the whole persisted prefix is drawn twice (`mdTexts` length 4 instead
   of 3, `Used 1 tool` twice). This is the duplication the ticket describes,
   and the trim is the only thing masking it.

3. **Mid-turn steer** (`turn.steer` folds as an ordinary user message,
   `turn.go:109-131`, journalled as `turn.steer`, `event.go:83-110`), then a
   late watcher: history `u0, a1, u1, a2, u2steer`, journal replay +
   `turn.steer{inputId, final}`. `findLastIndex(user)` is now `u2steer`, so
   the trim **keeps `a2`** and the replay redraws it:

   Measured: `mdTexts` = `["Earlier answer about the store.", "Step one says
   the cache is", "Step one says the cache is", "wrong because invalidate()
   is never called."]` — step 1's prose twice; the `Used 1 tool` label
   twice; and the steer text twice (once as the history user bubble, once as
   the journal `steered …` bubble). Confirmed: prose **and** tool run **and**
   the steer bubble all double. This is reachable with the current backend —
   a second tab that attaches after a steer was persisted.

4. **Adopted turn while this tab's history is one prompt behind**
   (`conversation.activity` `opened` → `watch(r, f.turnId, p.kind, -1)`,
   `chat.js:451-459`, with no history re-read): history `u0, a1` (the new
   prompt `u1` has not been read yet). `findLastIndex(user)` is `u0`
   → **`a1` is dropped from the screen**, and `u1` never appears (the
   journal never carries the user line; `live.userText` is only set by
   `onOpened`, `chat.js:405-406`). Visible text starts
   `why does the cache miss?Step one says the cache is …` — the previous
   answer vanished and the prompt is missing. This is the failure mode the
   trim's `findLastIndex` guess makes inevitable whenever the running turn's
   prompt is not the newest user item in the loaded page.

**Root cause**

`cutHistory` (`chat.js:319-322`) is a boolean derived from *how* the turn was
attached (`afterSeq < 0`), and the trim it drives guesses the merge boundary
from "the last user item" (`transcript.js:150-152`). Neither knows what the
store already holds: the journal replays the whole turn from `seq 0`
(`hub.go:567-620` replays every frame `> afterSeq`, `emitLocked`
journals every frame, `hub.go:638-670`), while HTTP history holds every
persisted step (`turn.go:312-336`). The two sources therefore describe
overlapping content, and the only thing deciding which copy wins is the
position of the last user message — which is wrong whenever a steered input
landed mid-turn (duplication) or the prompt has not been read yet (loss).
`onResync` flips the same boolean off (`chat.js:437`) without adding a
rule, and `onTerminal` re-reads history and clears it only at the end
(`chat.js:487-489`), so the wrong copy is on screen for the turn's lifetime.

**Owner layer**

`web/` controller + projection (`lib/chat.js`, `lib/turn.js`,
`components/transcript.js`). One part of the invariant is a backend/contract
question: the record has no way to identify a steered user line
(`messageDTO` at `infrastructure/delivery/http/conversation/dto.go:121-133`
carries `userType` only; the `turn.steer` frame carries `inputId`,
`frames.go:206-215`, with the comment "the frame carries no message id
because ids persist later").

**Proposed fix**

Replace the boolean with two explicit projection rules, both in `turn.js`
so they can be tested without a DOM:

1. `liveHistory(items, live)` — history trimming only where the live turn
   *replaces* stored content: an edit turn (drop everything from the target
   message, today `transcript.js:154-157`), and a resume whose prompt this
   tab read before sending (`live.kind === 'resume' && !live.adopted`: keep
   through the newest user item). A plain send trims nothing. This requires
   `onOpened` to stop labelling a resume `'edit'` (`chat.js:403` maps
   everything that is not `send` to `edit`) and to record `adopted` on
   turns created by `watch` (`chat.js:319-322`) instead of `cutHistory`.
2. `liveSegments(live, items)` — drop the replayed prefix the store already
   holds, and draw the rest. Identity available today: a live `tool`
   segment's `callId` matches a history tool item's `callId`
   (`turn.js:53-56`, `turn.js:121-124`), so "the lead run of replayed
   segments whose callIds all appear in history" is the persisted prefix;
   the first live tool call history does not know ends it. Steers need the
   record field from the invariant below; until it exists the segment can
   be matched by `inputId` only on the live side, so a steered bubble must
   be dropped from the journal side (the record's copy wins) or it doubles.

Invariant the merge needs, stated for T-06:

> At the moment a client attaches with `afterSeq: -1`, HTTP history is
> authoritative for every step the turn has finished; the journal is
> authoritative only for output after the last persisted step. The live tail
> therefore starts after the last segment the record holds, and the record is
> identified by durable ids (tool `callId`; a steered user line's `inputId`),
> never by position.

What the frames do **not** carry today, and the merge needs:
`activeTurnKind` is `send`/`edit` only (`docs/api/websocket.md:40-46`), so a
resumed turn looks like an edit to the watcher; the journal's `turn.opened`
(seq 0) carries no "history already holds…" marker; and the steer record
field above does not exist.

Blast radius: `chat.js` (watch/onOpened/onActivity/onResync/onTerminal),
`transcript.js` (drop the trim block, use the two functions), `turn.js`
(two new functions, `adopted`), plus one backend DTO record field for
steers (BE, gated by T-06). Tests (projection, T-13): each of the four
cases above as a unit — expected "one copy, prompt present, previous answer
kept"; plus a resume case (superseded partial answer replaced, not doubled).

**Open question**

D-06 §3 settles *attach* (a live turn always joins the subscriber set after
`resync_required`) but says nothing about what the client draws for the
overlap between journal and history, which is where T-10 lives. Still
needs: (a) may the client assume history is current at attach time — today
`openRoom` awaits `loadHistory` first (`chat.js:123-124`), but `onActivity`
does not re-read it before watching;
(b) does the record gain a steer `inputId` (or does the journal drop
`turn.steer` for a client that can read history?); (c) is a resync-`active`
answer supposed to mean "history is the prefix, live only adds a tail"
(today the hub replays nothing on a gap, `hub.go:614-621`, so the base
silently loses the unpersisted text streamed before the gap — a separate
finding for T-04/T-06, not fixed by the trim).

---

## T-11 — Markdown sanitizer, link policy, and copy fidelity

**Verdict: CONFIRMED** — no XSS hole found in a 33-payload matrix, but one
unhandled exception truncates an answer and aborts the render, two
renderer-chrome spoofs survive, and selection copy carries renderer
artefacts.
**Severity: user-visible** (crash/truncation), **trust** (spoofable chrome),
**cosmetic** (copy).

**Evidence** (each payload rendered through `renderInto`; sanitized
`innerHTML` recorded)

Neutralised correctly — no change needed:

| payload | DOM result |
|---|---|
| `<script>window.__pwn=1</script>after` | `<script>` gone, `after` kept |
| `<img src="/api/conversations" onerror=…>x` | `<img>` gone, `x` kept; **no request** (network log empty for `probe-*`, `HTMLImageElement.src` setter never called) |
| `<style>body{display:none}</style>ok` | `<style>` gone |
| `<form action=…><input type=password><button>Go</button></form>` | `<form>`/`<input>` gone, `<button>` kept (inert) |
| `<iframe>`, `<svg><foreignObject><iframe>` | all gone |
| `[x](javascript:alert(1))`, `<a href="javascript:…">`, `JaVaScRiPt:`, `java%09script:`, `jav&#x09;ascript:` | `<a>` with **no `href`**, `target=_blank rel="noopener noreferrer"` |
| `[x](data:text/html,…)`, `[x](//evil.example/x)` | `href` removed |
| `[x](/\\evil.example/x)` | `href="/%5Cevil.example/x"` → resolves same-origin (`http://127.0.0.1:18091/%5Cevil.example/x`); no open redirect |
| `[x](mailto:a@b.c)`, `[x](https://…)`, `[x](/api/…)` | kept, forced `target=_blank rel="noopener noreferrer"` |
| `<a rel="opener" target="_self">` (raw HTML) | rel/target **overridden** to `noopener noreferrer`/`_blank` |
| `<template><style><img onerror>` / `<math><mtext><table><mglyph><style>` / `<svg><style>` / `<noscript>` mXSS | emptied, no handler survives |
| `![x](data:image/svg+xml,<svg onload=…>)` | rendered as text `![x](data:image/svg+xml,` (image renderer, `markdown.js:31`); nothing fetched |
| `<p style="position:fixed">`, `<span class="fixed inset-0 z-50 bg-black md">` | `style` dropped; `class` reduced to allowlist (`markdown.js:98-108`) → `<span>` |
| `<div data-diagram>…` + mermaid `click`/`img:`/`%%{init}%%`/entity-encoded tags | preflight rejects → source shown (`diagram.js:9-40`) |
| ` ```mermaid graph TD…` | Pele SVG mounted, `<a>` unwrapped, `sanitizeSvg` runs (`diagram.js:60-120`) |
| GFM table, `<details open><summary>`, `<kbd>`, `[^a]` footnotes (defined + undefined), ` ```` ` nested fence | correct; undefined ref stays literal; `javascript:` inside a footnote body is stripped |

Holes:

1. **Unhandled exception in the render path — the answer is truncated and
   the render aborts.** Payload (allowed by the allowlist: `div`, `class`,
   `data-diagram`):

   ```
   Here you go:

   <div data-diagram><div class="canvas"></div></div>

   and the rest of the answer.
   ```

   `decorate` runs `box.querySelector('details pre').textContent`
   (`markdown.js:137`) with no null check → `TypeError: Cannot read
   properties of null (reading 'textContent')`. Measured: `renderInto`
   throws; rendered through the real `<Transcript>` the exception escapes
   `render()` and the transcript stops at the payload —
   `host.textContent` = `"hiHere you go:"`, `mdCount` = 1, "and the rest of
   the answer." **never rendered**. The same throw repeats on every
   re-render (each delta), because the block's `_raw`/`_sig` never get set
   (`markdown.js:179-189`). `markdown.js:142` and `:152`
   (`box.querySelector('details').open = true`) would fail the same way in
   the reject branch.

2. **Renderer chrome is spoofable from model output** (trust, not XSS):
   `data-copy`, `data-diagram`, `data-fn` and the `codeblock`/`canvas`
   classes are in the allowlist (`markdown.js:94-98`), so
   `<div class="codeblock"><pre><code>rm -rf / # harmless</code></pre><button data-copy>Copy</button></div>`
   renders a real "Copy" button (`handleCopyClick`,
   `markdown.js:210-225`) that copies attacker-chosen text, and
   `<button data-fn="a">9</button>` renders a footnote back-link that
   scrolls to a footnote. The model can therefore draw UI that looks like
   the app's own.

3. **Selection copy carries renderer artefacts.** Code-block **Copy button
   is exact** — `code.textContent` === source, verified with
   `const s = "<b>&amp;</b>";\nif (a < b && c > d) { x(); }` (`equal:
   true`). But selecting a rendered answer yields
   `"The fix:1\njsCopyconst a = 1;Done1.\nbecause reasons\n"`:
   the code block's **language label and "Copy" button text are inside the
   selection** ("jsCopy"), footnote digits glue to the preceding word
   ("fix:1", "Done1."), and the footnote body is appended at the end. The
   base has **no "Copy response" action** (only `CopyButton` for user
   messages, per-code-block Copy and tool-card Copy), so this is the only
   copy path for an answer — the ticket's "Copy adds a blank line" is, in
   the base, selection copy plus the T-08 block boundary.

**Root cause**

`markdown.js` mixes a hardened sanitizer with a renderer that decorates its
own output by convention (fixed `data-*` names and class names, and
`querySelector` on structure it assumes it produced). Untrusted HTML cannot
be distinguished from renderer HTML after sanitization, and `decorate`
trusts the structure it finds. Copy fidelity is a CSS/selection concern
nobody owns: header, buttons and footnote markers are selectable.

**Owner layer**

`web/lib/markdown.js` (+ `web/styles/app.css` for the copy surface). The
sanitizer itself is sound; the decoration step is not. No sanitizer rewrite
is needed — and per the backlog it should wait for T-13 anyway.

**Proposed fix**

* `decorate`: treat a `[data-diagram]` box that lacks `details > pre` as
  invalid (show source / drop the box) instead of throwing
  (`markdown.js:135-155`); and wrap the per-block `decorate(next)` call in
  `renderInto` (`markdown.js:187`) in a `try/catch` so a single bad
  block can never abort the transcript. Blast radius: two functions,
  no API change. Test: the payload above renders the rest of the answer and
  leaves one invalid diagram box.
* Chrome spoofing: decide whether model HTML may keep renderer-owned
  attributes at all. Cheapest clean shape: strip `data-copy`, `data-diagram`,
  `data-fn`, `data-fn-def` and the renderer-owned classes from *source* HTML
  before the Markdown parse (a `walkTokens`/`renderer.html` hook), keeping
  them only on nodes the renderer itself emits. Alternative: mark renderer
  nodes with a per-render random attribute and have `decorate`/
  `handleCopyClick` require it. Blast radius: `markdown.js` only; test:
  the two spoof payloads above produce no button/copy target.
* Copy surface: `.codeblock header, .copybtn, .md-fnref button, .md-image
  { user-select: none }` in `web/styles/app.css` so a selection of the
  answer is the answer's text; keep the code-block Copy button exact as it
  is. Test: `Range.toString()` over a rendered answer equals the answer's
  source text (minus footnote bodies, which are a design choice).

**Open question**

Two decisions, both for the PA (no SA needed unless the answer changes the
render contract): (a) may model-authored HTML keep the renderer's chrome
attributes/classes (proposed: no — they become invisible to
`decorate`/copy), or is a fake Copy button an accepted risk? (b) Should
footnote bodies be excluded from a selection copy of an answer (current
behaviour appends them), or is "copy exactly what is on screen" the rule?

---

## Cross-cutting notes (round 1 input, no ticket of their own)

* **The base has no per-answer Copy.** T-08's "copy adds a blank line" and
  T-11's copy-fidelity clause both read as if a "Copy response" button
  existed; it does not (`grep -i copy web/` → code-block Copy
  (`markdown.js:26`, `:210`), user-message Copy
  (`transcript.js:27-33`), tool-card Copy (`toolcard.js:31-37`)). If round 1
  adds one, it must copy the joined projection (`joinText`) and nothing
  from the DOM.
* **A resume turn is projected as `kind:'edit'`.** `onOpened`
  (`chat.js:403`) maps every non-`send` command to `'edit'`, and
  `live.editOf` is only set for a real edit (`chat.js:406`), so
  `{kind:'edit', editOf:null}` is the shape of a resume. Anything that keys
  on `live.kind` (including the T-10 `liveHistory` rule and `steer`'s
  `kind !== 'send'` guard, `chat.js:269`) is therefore reading the wrong
  value today.
* **`turn.steer` has no record identity.** The frame carries `inputId`
  (`frames.go:206-215`), the persisted user line does not
  (`dto.go:121-133`). Any dedupe across live/history for steers needs a
  field; until then a steer can only be matched by text, which the protocol
  explicitly refuses to treat as identity (`docs/api/websocket.md:252-253`).
* **No web test harness yet.** `make test-web` today is
  `node --check` only (`Makefile`), so every verdict above is either
  DOM-captured by hand or projection-level. The T-13 harness should adopt
  the four fixtures verbatim: T-08's interrupted-record shape, T-09's two
  frame sequences, T-10's four room states, T-11's payload table.

# Audit & hardening of NusaLLM (llm-base)

NusaLLM is the base NusaShell was built from. Everything NusaShell learned
after the fork — the bugs its users hit, the contracts its frontend had to
re-learn — points back at code that still lives here. This directory is the
working record of maturing the base on its own terms: find the defect,
explain its root cause, fix it at its owner layer, prove it with a test.

Two rules frame every ticket:

- **Evidence before opinion.** A finding is a reproduction: command, input,
  observed output, and the `file:line` that explains it. "Looks wrong" is a
  hypothesis, and it is labeled as one.
- **Fix the owner, not the call site.** `AGENTS.md` says which layer owns a
  rule. A ticket that ends with a patch in the wrong layer is not done.

## Rounds

| Round | Question | Output |
| --- | --- | --- |
| 0 — audit | Does the defect exist here, how does it show, and which layer owns it? | `findings-<AREA>.md`, decisions where a contract is at stake |
| 1 — fix | What is the smallest clean change that removes it? | patch + regression test + gate run |
| 2 — verify | Does it hold under the gate and in a rendered/real run? | QA verdict, ticket closed |

Round 0 changes no production source. `QA` may add the test harness and
gate target; `SA` may write decision records. Nobody commits; the owner
reviews the diff.

## Areas and file ownership

| Area | Thread | Owns (writes) | Must not write |
| --- | --- | --- | --- |
| BE | `BE · llm-base` | `application/**`, `infrastructure/**` (Go), `cmd/**` | `web/**`, `docs/adr/**` |
| FE | `FE · llm-base` | `web/**` | Go sources, `Makefile` |
| SA | `SA · llm-base` | `docs/audit/decisions.md`, `docs/adr/**` | production code |
| QA | `QA · llm-base` | `web/tests/**`, `Makefile`, `test/**`, `*_test.go` for verification | production code |

Two threads never edit the same file in the same round. When a fix spans
areas (backend frame + frontend projection), the PA splits it: contract
first (SA decision), producer next (BE), consumer last (FE).

## Reporting

Each thread reports to the PA thread (`@dolphin`) with `conversation_send`:
a summary of at most ~300 words, the findings file path, ticket verdicts,
and the decisions it needs. Team threads do not message each other in
round 0 — the PA relays, so the discussion stays in one place.

Findings file header:

```markdown
# Findings — <AREA> (round 0)
Status: draft | ready
Tickets: T-xx, T-yy
```

Per ticket:

```markdown
## T-xx — <title>
Verdict: CONFIRMED | NOT REPRODUCIBLE | NEEDS-DECISION
Severity: user-visible | data | cosmetic
Evidence: <command/output or fixture> — <file:line>
Root cause: <one paragraph>
Owner layer: <layer per AGENTS.md>
Proposed fix: <shape, blast radius, tests>
Open question: <only if a decision is needed>
```

## Gate

Read-only check used for every verdict. `make test` builds, `go fix`,
`go vet`, `go test`; `make test-web` syntax-checks the WebUI. QA owns the
combined target (`make check`) and records a baseline before any fix lands.

## Status board

PA maintains this table. `claimed` means a thread owns the ticket this
round; `done` means the fix is in with a passing test and a gate run.

Round 0 closed 2026-11-25: every audit ticket reported, ADR-0002 and
ADR-0003 ratified with amendments (see `decisions.md`), and the delta map
ranked. Round 1 runs T-01, T-02, T-04, T-08, T-09, T-11, T-17 plus the
contract clauses.

| Ticket | Area | Title | Owner | Status |
| --- | --- | --- | --- | --- |
| T-01 | BE | File-tool path policy: `~`, `file:` URL, `%VAR%`, absolute paths | BE | round 1 |
| T-02 | BE | Transient failure after streamed output kills the turn | BE | round 1 |
| T-03 | BE | Provider stream robustness: truncation, retry-after, usage | BE | round 1 (rows 1/2/5/7) |
| T-04 | BE | `turn.watch` answers without attaching a live turn | BE | round 1 |
| T-05 | SA | Path & placement ownership decision (feeds T-01) | SA | done (ADR-0002) |
| T-06 | SA | Retry contract: what may be retried, and what the client must reset | SA | done (ADR-0003), doc clauses in round 1 |
| T-07 | SA | Cross-repo delta map: NusaShell fixes to shared code, classified | SA | done (D-07) |
| T-08 | FE | Adjacent text blocks parse as separate Markdown documents | FE + BE | round 1 |
| T-09 | FE | Retried attempt leaves its abandoned tail on screen | FE | round 1 |
| T-10 | FE | Late-watch duplication: live journal vs stored history | FE | round 2 (contract from SA) |
| T-11 | FE | Markdown sanitizer, link policy, copy fidelity, diagram crash | FE | round 1 |
| T-12 | QA | Baseline gate + `make check` parity for the base | QA | done |
| T-13 | QA | WebUI test harness in the base | QA | done (23 tests) |
| T-14 | QA | Black-box reproduction kit | QA | done (4 scenarios), round 1 flips them to assertions |
| T-15 | FE | Thread refresh compares before writing | FE | round 2 |
| T-16 | BE | Windows & encoding hardening: CRLF patch, invalid UTF-8, `exec` no-output | BE | round 2 |
| T-17 | BE | Deep `turn.watch` replay kills the socket (queue cap 64) | BE | round 1 |
| T-18 | BE | A folded steer's stored row carries its `inputId` (enables T-10) | BE | round 2 |

### Round-1 decisions recorded

- **T-09 boundary** is the last `turn.step_finished`, not a settled tool row:
  `web/lib/turn.js` marks a tool settled on failure and on abandon, so
  "settled" cannot mean "persisted".
- **`turn.failed`** always carries `status`, `code`, and a non-empty
  `message`; `retryAfterMs` is optional, present when the provider asked
  for a wait.
- **Replay bound (T-17).** `conn.enqueue` kills a socket whose queue
  overflows (`conn.go:53-67`, `outboundQueueCap = 64`), so a verbatim
  replay of a long journal disconnects the very tab it is catching up.
  Replay is verbatim while it fits the queue budget; beyond that the
  answer is `turn.resync_required`, plus an attach when the turn is live.
- **T-08's producer** is `interruptedReply`: it drops tool-call parts and
  leaves two adjacent text parts in one message. BE fixes the message
  invariant; FE keeps neighbouring text in one Markdown block.

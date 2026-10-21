# Persistence and storage

Everything NusaLLM writes to disk lives under one storage root and is owned
by `infrastructure/repository/` — the layer responsible for storage format,
hydration, atomic writes, and read projections (see
`infrastructure/repository/AGENTS.md`). This document describes what lands
on disk, in which shape, and why. It is aimed at engineers and agents
debugging a corrupted store, inspecting state by hand, or extending the
format — not at end users.

The guiding idea is that history is an append-only line stream and
everything else is a projection of it. Messages and tool-output patches
share one JSONL stream per conversation, so the common write path is a
single `O_APPEND` + `fsync` and never rewrites stored bytes. The two
exceptions — compaction and edit — rewrite segment files atomically. That
split is what makes the store cheap on the hot path and still recoverable
after a crash.

## Storage root

`Config.Storage.Dir` (`infrastructure/config/storage.go`) is the single
root every repository derives its paths from. It defaults to `./data` via
the `default:"./data"` struct tag and is set in the config file as
`storage.dir`. One root means a deployment moves, backs up, or inspects
all state in a single place.

The layout is wired at the composition root, split by lifecycle:
`admin.LoadBoot` opens `secrets.db` (and a `gateways/` repository for
the management plane); `compose` (`cmd/nusallm/wire.go`) wires
`gateways/`, `conversations/`, and `logs/` for the engine; `run` points
the TUI at `cli/`, which `infrastructure/repository/history/history.go` creates on open. These are the
real directory names, not conventions:

```text
<storage.dir>/                  (default ./data)
├── gateways/                   one <id>.json per gateway — dir 0700
├── conversations/              one subdirectory per conversation
│   └── <convID>/               e.g. c_US57K3ZKE7NJ4TKZOOHCHDBOF4
│       ├── meta.json           record + counters
│       ├── 0.jsonl, 1.jsonl …  sealed archive segments (only after Compact)
│       ├── active.jsonl        the live tail
│       └── tool-output/        created on exec; full-output files only when result exceeds budget
├── logs/
│   ├── active.jsonl            current diagnostic log
│   └── 0.jsonl.gz, 1.jsonl.gz … compressed rotated archives
├── cli/
│   └── history                 REPL input history — dir 0700, file 0600
└── secrets.db                  sqlite key-value store — file 0600
```

Conversation and message IDs are minted as `<prefix>_<128-bit base32>`
(`valueobject.NewID` in `infrastructure/domain/valueobject/id.go`). Because
an ID becomes a path segment, every repository re-asserts
`valueobject.ValidID` at its own boundary — no `.`, `..`, or path
separators ever reach `filepath.Join`.

The JSON config file itself is *not* part of this tree. `resolveConfigPath`
(`delivery/cli/admin/admin.go`) takes an exported `CONFIG_PATH` first.
The binary at `~/.local/bin/nusallm` then defaults to
`$XDG_CONFIG_HOME/nusallm/config.json` or `~/.config/nusallm/config.json`
even when the file is missing and even when a `.env` in the working
directory names another path. A source build still lets that `.env` set
`CONFIG_PATH`. When `CONFIG_PATH` is still empty, the same user file is
used if it already exists. When no path is resolved, nothing is created
and `storage.dir` stays `./data`.
`admin.EnsureConfig` generates a file once — called by both `run` and
`serve`. For the user config path it writes `storage.dir` as an absolute
directory (`$XDG_DATA_HOME/nusallm` or `~/.local/share/nusallm`). Any
other path delegates to `setup.EnsureConfig`
(`application/setup/setup.go`), which writes a default-tagged file only
when the path is non-empty and nothing exists there. Read-only admin
commands never create a config. `storage.dir` inside the file selects
the root above. The same `.env` also feeds the `env:` credential
references described below.

When a non-empty config path is configured, loading fails on a missing,
unreadable, or malformed file. Defaults apply to fields absent from a valid
document, not to a failed document read.

## Conversations

A conversation is a directory under `conversations/` named by its ID. Two
file kinds live inside: `meta.json` for record-level state, and a JSONL
line stream split across `active.jsonl` (the live tail) plus any sealed
segments `N.jsonl` written by compaction. The logical history is the
sealed segments in numeric order followed by the live tail.

### `meta.json`

`metaRecord` (`infrastructure/repository/conversation/record.go`) is a
single JSON object holding both the conversation record and the derived
counters `Info` serves, so inspection never has to scan message files:

| Field | Meaning |
|---|---|
| `id` | conversation ID — also the directory name |
| `title`, `route`, `modelId` | display title, bound gateway ID, bound model ID |
| `environment`, `workspace` | execution-driver identity and that driver's bound workspace path; both survive resume |
| `tools` | bound tool catalog: `name`, `description`, `parameters` (raw JSON schema), `enabled`, `permission` (`system`/`agent`/`general`) |
| `usage` | accumulated `inputTokens`, `cacheReadTokens`, `cacheWriteTokens`, `outputTokens` |
| `messageCount` | total logical messages — sealed plus live |
| `activeCount` | messages in the live tail only — the range `Load` returns |
| `sealedSegments` | how many `N.jsonl` files are committed; also the index the next `Compact` writes |
| `createdAt`, `updatedAt` | timestamps; `updatedAt` is restamped on every meta write |

Two things are deliberately absent. There is no `systemPrompt` field — the
prompt ships embedded in the binary, so it is applied in memory and never
persisted (`load.go` ignores stamped copies in old metas for the same
reason). And there is no credential material: `route` is just the gateway
ID, resolved through the gateway repository at use time.

`environment` is required in the current format. A record without it fails
to load; `List` reports invalid metadata rather than silently hiding the
conversation. Existing records must be updated explicitly to a registered
environment before they can be used.

`Save` (`write.go`) upserts this file. It creates the directory on first
write and preserves record-level fields (`createdAt`, the counters) on
update, so a `Save` never disturbs the stream's accounting. The port
contract requires `Save` to precede `AppendMessage` — `AppendMessage`
fails with 404 on a conversation that has no meta.

### The JSONL line stream

Every `*.jsonl` file is a sequence of `lineRecord` envelopes — one JSON
object per line. Two line types share the stream so writes append without
rewriting earlier records, and ordering is literal:

```json
{"type":"message","data":{ …messageRecord… }}
{"type":"tool_output","callId":"call_…","output":"…","outputLatency":"12ms","attachments":[ … ]}
```

`type` is `"message"` or `"tool_output"`. A `tool_output` line carries the
patch fields (`callId`, raw `output`, `outputLatency`, `attachments`) at the envelope level; its
`data` still serializes as an empty message object but is meaningless.
Patches resolve **globally by `callId`** at read time
(`messagesFromLines` in `conversation.go`), so a patch may target a call
sealed in an earlier segment — this is why they are append-only records
rather than in-place edits.

A `message` line's `data` is a `messageRecord`:

| Field | Notes |
|---|---|
| `id`, `role`, `content`, `createdAt` | `role` is `system`/`user`/`developer`/`assistant` |
| `userType` | `origin` (real user input) or `system` (harness-injected) |
| `finishReason` | `stop`/`tool_calls`/`length`/`content_filter` |
| `parts` | Ordered assistant text, reasoning, and tool-call positions |
| `toolCalls` | each with `id`, `name`, `input`, raw `output`, `outputSet`, `outputLatency`, `outputBudget`, `outputAttachments`, `type` (`origin`/`injection`), `callAt`, `optionalContent` |
| `attachments` | `uri` + `mediaType`, optional `name` and `sha256` pin — stored references (`file://`, `https://`), materialized by adapters at send time |
| `optionalContent` | adapter-namespaced extras like `completion.reasoning_content`; domain never interprets values |

Hydration (`decodeMessage`) goes through the public entity constructors
and setters — there is no reconstitute seam — and stored enum strings are
validated strictly. An unknown `role`, `userType`, `finishReason`, or tool
`type`/`permission` fails the load as corrupt or forward-incompatible
data rather than silently coercing.

### Write mechanics

Two primitives cover everything (`conversation.go`):

- `appendLine` opens the file `O_APPEND|O_CREATE`, writes one marshaled
  line, and `fsync`s. This is the hot path — a message or a patch is
  durable in one syscall pair, and concurrent tool results racing to
  persist stay safe because nobody read-modify-writes a stored line.
  Before writing it mirrors `readLines`'s tolerance exactly: it drops a
  final record only when that line fails the same decode/type probe, and a
  complete record that lost its terminator — still decodable, so still a
  record to the reader — is kept, with the missing newline written instead.
  So a new line is never glued onto partial bytes and a tail readers would
  have tolerated is not left behind to turn interior.
- `writeFileAtomic` stages to `<path>.tmp`, `fsync`s, closes, renames over
  the target, then best-effort `fsync`s the directory. A crash mid-write
  leaves the old file or nothing — never a torn target. The file-level
  `fsync` before rename is load-bearing: without it a crash can resurrect
  the name with no contents, and a sealed segment that reads back empty is
  silent history loss rather than an error.

A `sync.Mutex` per repository serializes writers; the store assumes a
single process owns the directory.

### `AppendMessage` and `UpdateToolCallOutput`

`AppendMessage` (`append.go`) appends one `message` line to
`active.jsonl`, then bumps `messageCount`/`activeCount` in meta. It
returns `persisted=true` once the line itself is synced, even if the meta
rewrite afterwards fails — the message is already in history; the counter
is only a projection.

`UpdateToolCallOutput` (`updatetool.go`) appends a `tool_output` patch
line carrying raw output, fixed latency, and any media references. Because it is
a pure append with no read-modify-write, parallel tool calls completing
out of order are safe by construction, and a crash mid-batch leaves the
remaining calls honestly pending instead of losing every result.

### `Compact` — sealing history

`Compact` (`compact.go`) is called after the model has produced a summary
of old context. It seals the first `dropBefore` message lines of
`active.jsonl` **verbatim** into `<sealedSegments>.jsonl`, then rewrites
`active.jsonl` as `[summary line] + surviving message lines + every patch
line`. Patches all move to the tail regardless of position — they stay
global, so a late output still reaches a call that was just sealed.

The commit point is `meta.sealedSegments`: the segment file is written
first, then active is rewritten, then meta is bumped. That ordering is
chosen so the surviving bytes can be recovered — a segment file that exists
but is not counted is an orphan that normal readers reject; if the
active rewrite fails the orphaned segment is deleted (the prefix still
exists in the untouched active copy, so the orphan is a pure duplicate);
if only the meta write fails, the uncounted segment is left on disk as the
sole surviving copy of the dropped prefix, available for offline recovery.
Until metadata and numbered files agree, per-conversation reads and writes
fail rather than presenting a partial history or overwriting
an uncommitted era. Stop every NusaLLM process using the store and back up
the conversation directory before recovery. Run
`nusallm conversation repair <id> --discard-duplicate` if the orphaned
segment's message lines still lead `active.jsonl` (the active rewrite did
not land). Run `nusallm conversation repair <id> --commit` if the prefix is
absent from active (the active rewrite landed); this validates the stream
and recalculates both message counters. The command refuses the wrong
action. It supports exactly one extra numbered segment; a missing segment
must be restored from backup. A partially overlapping orphan is not a
documented Compact crash window: neither repair action can safely choose
which copy of a message to keep. If the command rejects both actions, leave
the files unchanged and reconcile them manually from a backup. A
different crash window — an edit interrupted inside sealed history —
carries its own roll-forward verb; see `RewriteFrom` below.

`messageCount` grows by one (the summary is a message) and `activeCount`
shrinks to `activeCount - dropBefore + 1`.

### `RewriteFrom` — editing a message

`RewriteFrom` (`rewrite.go`) is the persistence counterpart of
`Conversation.RewriteFrom` — the `/edit` flow. Editing means replacing one
user message's content and discarding everything recorded after it, so the
caller can regenerate the turn. Two shapes:

- **Target in `active.jsonl`** — the common case. The file is truncated
  right after the edited line, whose `content` is patched in place. Patch
  lines sitting after the cut are kept: they resolve globally by call ID,
  so one may still answer a call in the retained prefix (after `Compact`,
  every patch lives in the tail). Dead patches are harmless.
- **Target in sealed segment N** — repromotion. Segments `0..N-1` and N's
  surviving prefix are merged into the new `active.jsonl`, so the model
  regains the full pre-edit era verbatim. The numbered segments are then
  retired and `sealedSegments` resets to the re-globbed count — the next
  `Compact` starts again at `0.jsonl`. Compaction summaries covering later
  eras would lie about discarded content, so they go away with the old
  tail rather than being preserved.

  The merged stream and the retired eras live in different files, so no
  single rename can commit the edit. The protocol instead stages
  everything around a payload name. First the directory is swept of
  rewrite leftovers (`active.jsonl.rewrite`, `*.jsonl.rewrite-old`) — the
  archive just validated, so anything matching is debris, never something
  to publish. The merged stream is then written durable to
  `active.jsonl.rewrite`, each `k.jsonl` is *parked* as
  `k.jsonl.rewrite-old` — renamed, never deleted — with a directory
  fsync, and only then is `active.jsonl` rewritten with the merged
  bytes. That rename is the commit point: before it the canonical stream
  and every era are intact, after it the merged history is truth and the
  parked files are stale copies. A failure before the commit rolls the
  parking back and drops the payload once every canonical name is
  restored — if a rename-back itself fails, the payload stays because it
  is then the only roll-forward source. That leftover is diagnosed as an
  interrupted edit only when the parked set still has the protocol's shape —
  the contiguous prefix `0.jsonl.rewrite-old … j.jsonl.rewrite-old` with
  every surviving numbered segment an index above it. A gapped or otherwise
  wrong-side leftovers state is refused by `--finish-rewrite` (pinned by
  tests in `repair_test.go`) and needs manual reconciliation from a backup;
  only a properly shaped leftover may roll forward, committing an edit the
  user saw fail — no message is lost (the payload carries the merged stream),
  but the outcome is surprising. After it, `recountMeta` resyncs
  the counters (`sealedSegments` re-globs to zero — a parked name is not
  `*.jsonl`) and the payload and parked files are removed, best effort.

  Two states survive a crash inside that window. A payload plus an
  edit-shaped layout is an interrupted edit: every read fails and names
  the fix, `nusallm conversation repair <id> --finish-rewrite`. The
  failure is store-wide for listing, not just for that room: `readMeta`
  fails for it, so `List` — and with it `GET /api/conversations` — errors
  for the whole store and the sidebar stays empty until the room is
  repaired, the same blast radius the Compact-orphan window has. The
  layout is edit-shaped only when it is one the protocol can leave while
  a payload exists — fewer numbered segments than the metadata counts,
  *and* a parked set that is exactly the contiguous prefix
  `0.jsonl.rewrite-old … j.jsonl.rewrite-old` (segments are parked in
  ascending order and never skipped, so a crash mid-parking leaves a mix
  of parked and numbered files) with every surviving numbered segment an
  index above that prefix — or a payload already byte-identical to the
  live tail, where the publish landed and republishing it is a no-op. A
  payload plus a consistent archive is post-commit debris: invisible to
  readers, swept by the next sealed edit, never republished. The payload
  is only removed once the archive validates, so "inconsistent and no
  payload" is a state this code never produces. Any other mismatch — an
  extra segment, a missing numbered file, a gapped or wrong-side parked
  set, manual damage — is not this window; it keeps the mismatch's own
  recovery guidance.

  `--finish-rewrite` rolls forward, never back: it validates the payload
  (newline-terminated, every line a stream record), publishes it over
  `active.jsonl`, deletes every numbered and parked leftover, recounts
  the counters, and removes the payload. There is no abort — the merged
  stream already carries the only surviving copy of the parked eras, so
  publishing is the only safe completion. Run it with the app stopped; a
  second run, a run against a healthy room, or a payload sitting beside a
  mismatch the edit cannot leave refuses cleanly — under-diagnosis is the
  safe direction, since the only alternative is republishing stale bytes
  over live history.

Both paths re-run `entity.EditableMessage` on the stored
`(role, userType)` tuple — only real user messages (`role=user`,
`userType!=system`) are editable; anything else is a 400, and an ID that
is nowhere in history is a 404. After surgery `recountMeta` resyncs the
counters from the actual stream. There is no replay-record alternative by
design: an append-only "edit" line would leave the stale suffix visible to
the model.

### `Delete`

`Delete` (`delete.go`) is `os.RemoveAll` on the conversation directory —
meta, live tail, every sealed segment, and `tool-output/` spill files go together.
Spill files are retained until the user deletes the conversation. A missing
directory is a 404.

### Two read paths

The port (`infrastructure/domain/repository/conversation.go`) splits reads
by consumer:

- **`Load`** returns the aggregate a turn runs against: meta plus *only*
  `active.jsonl` — the live context the model actually sees. Sealed
  history stays on disk.
- **`Info` / `List` / `LoadAt`** are projections for UIs. `Info` is a
  meta projection with a sealed-filename consistency check; it parses no
  messages. `List` reads every
  conversation's meta, skips directories without a readable one, and
  sorts by `updatedAt` descending. `LoadAt(start, end)` slices the global
  logical history — sealed segments in numeric order, then the tail —
  with clamped indexes and negative-from-end semantics (`LoadAt(id, -10,
  0)` reads the latest 10), backing history browsers and pagination.

When a load includes sealed segments, only files numbered below
`meta.sealedSegments` qualify (`loadMessages`): because `Compact` writes
the segment file before bumping the count, a crash can leave a numbered
file whose prefix the intact active copy still holds — replaying it would
duplicate history, so meta is the authority on what is committed.

Torn-tail tolerance is the other recovery rule (`readLines`): a final
line that fails to decode or has no `type` is dropped, since that is the
signature of a crash mid-append. The writer mirrors that tolerance exactly,
not a narrower one: before appending, `appendLine` probes the true final
line with the same decode/type test. A line that fails it is dropped, but a
record that decodes and carries a `type` is kept even when its terminator
was lost — a missing terminator is not damage to the record, and `readLines`
would have kept it, so the file gains the newline instead of losing the
record. Only that final line is eligible, and the probe is not capped: a
long decodable line is exactly the tail the reader keeps, so the probe reads
it whole. A damaged tail left in place becomes interior corruption on the
next append, after which every read of the room fails; corruption earlier in
the file is reported, not swallowed.

## Gateways

Gateways are few and operator-configured rather than user-generated, so
`infrastructure/repository/gateway/` stores one JSON document per gateway
at `gateways/<id>.json`. `Save` is atomic — unique temp file via
`os.CreateTemp` (two processes writing the same ID cannot truncate each
other's staged bytes), `fsync`, rename, dir sync — and `List` is a
directory glob. The directory is created `0700` and files `0600` because
records may carry credential references.

### Record format

`gatewayRecord` (`gateway/record.go`):

```json
{
  "id": "openai",
  "name": "OpenAI",
  "style": "completion",
  "endpoint": "https://api.openai.com/v1",
  "apiKey": "secret:gateway/openai/api_key",
  "stream": true,
  "models": [ … ]
}
```

`style` is the wire family (`completion`, `responses`, `messages`,
`generatecontent`).
`models` is the catalog; each `modelRecord` mirrors every `Model` field —
`id`, `route`, the generation parameters (`temperature`, `topP`, `topK`,
`maxOutputTokens`, `presencePenalty`, `frequencyPenalty`, `seed`,
`stopSequences`, `effort`), and catalog metadata (`modelType`,
`contextWindow`, `outputLimit`, `maxImages`, `name`, `canonical`, `aliasOf`,
`supportedEfforts`, `pricing` with per-million `inputPerM`/`outputPerM`/
`cacheReadPerM`/`cacheWritePerM`, `inputModalities`, `outputModalities`,
`optionalContent`). Numeric parameters serialize as pointers' values and
are `omitempty` — unset means "don't send", not zero. Hydration again goes
through constructors and setters with strict enum parsing, so a catalog
entry round-trips losslessly or fails loudly.

### Credentials are references, never plaintext

The `apiKey` field never holds raw key material once a record goes through
the normal write path. The scheme (`infrastructure/domain/valueobject/
keyref.go`) is two prefixes:

- `"env:NAME"` — resolved at `Load` time via `os.Getenv`. An unset
  variable fails the load rather than resolving to `""` — a silent empty
  key would surface as an upstream 401 with no local explanation. `.env`
  in the working directory feeds this, loaded at boot before config.
- `"secret:<key>"` — resolved through the injected `SecretStore`. The
  canonical key a gateway's credential vaults under is
  `gateway/<id>/api_key` (`drepo.GatewaySecretKey`), shared by the
  repository and the management plane so both write the same vault entry.

When a literal key is supplied — `nusallm gateway add … --key-stdin`, or a
direct `Save` — the application vaults it under the canonical name and the
record stores only its `secret:` reference. With no `SecretStore` wired, a
literal is **refused** outright: persisting a key into JSON is treated as
a deployment defect, not a fallback. An already-formed `env:`/`secret:`
ref is persisted verbatim, and `""` clears the credential. The upsert
orders its writes vault-first, record-second — an orphaned vault value is
harmless garbage while a dangling `secret:` ref is a broken gateway — and
deletes the canonical vault entry once a record stops naming it (a switch
to `env:` or a foreign ref would otherwise leak the old material).

`Delete` removes `<id>.json` plus the canonical vault entry. Conversations
that referenced the route keep a dangling ID; resolution failure at send
time is the application's fallback.

### Reads: `Load` vs `Inspect`

Like conversations, gateways have two read paths split by consumer:

- **`Load` / `List`** are the live path — the record hydrated with the
  credential *resolved* into key material, ready to send against. `List`
  skips records that fail to load (e.g. a dangling `secret:` ref) since it
  backs model pickers.
- **`Inspect` / `InspectList`** are the management-plane reads — the
  aggregate hydrated with *no* key material, plus `ApiKeyRef` carrying the
  raw stored value (a ref, a literal, or `""`). This is what lets an
  update carry the ref forward instead of pinning whatever it resolved
  to, and what lets `gateway show` render a record whose credential is
  unresolvable — marked broken rather than hidden. A file that cannot even
  decode still surfaces as a snapshot with `Gateway: nil` and a
  best-effort `ApiKeyRef`, so a repair upsert can clean up the vaulted
  material it points at. Only a truly absent ID is 404.

## Secret store

`secrets.db` sits at the storage root and implements the `SecretStore`
port (`infrastructure/domain/repository/secret.go`) on SQLite via
`modernc.org/sqlite` — a pure-Go driver, so the binary needs zero cgo.
The schema is one table created at open
(`infrastructure/repository/secret/secret.go`):

```sql
CREATE TABLE IF NOT EXISTS secrets (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
)
```

`Set` upserts (preserving `created_at`), `Get` and `Delete` return the
apperror 404 for absent keys. The handle is capped at one connection and
runs `PRAGMA busy_timeout = 5000` so a short-lived writer like
`nusallm gateway add --key-stdin` retries while the REPL's open store
holds the write lock instead of failing instantly with `SQLITE_BUSY`.
The file is `chmod 0600` — including a pre-existing looser-mode file, which
`New` fixes on open — inside a `0700` parent.

This is explicitly **not encryption**: values sit in the file as
plaintext. What the binary container buys is keeping keys out of casual
`cat`/`grep` inspection and out of the JSON records, plus a port boundary
that leaves room for an encrypted backend later.

## Diagnostic log

`logs/` implements the `Logger` port — a write-only diagnostic sink for
provider traffic, kept deliberately separate from conversation history.
`infrastructure/repository/log/` reuses the segment pattern: a live
`active.jsonl` (mode `0600`) plus numbered compressed archives `0.jsonl.gz`,
`1.jsonl.gz`, ….

### Entry shape

Each line is a `record{ts, kind, data}` envelope — timestamp, a grep tag,
and the scrubbed payload. The completion adapter
(`infrastructure/service/core/text/completion/`) emits kinds
`provider.request`, `provider.response` (`request_id`, `model`),
`provider.error` (`status`, `request_id`), and `provider.stream`
(chunk count). Request/response/stream bodies are omitted before persistence.
Append failures are swallowed at the call site — diagnostics must
never break a send.

### The redact pipeline

Before anything reaches the file, `Append` marshals the entry to JSON and
unmarshals it back into `any` — normalizing means `json.RawMessage` or
struct payloads cannot bypass the scrubber — then applies a `Redact`
function per entry. The default is `redact.Value`
(`infrastructure/redact/redact.go`), which deep-walks maps/slices/strings
and replaces matches with `"***"`:

- map keys matching a credential pattern (case-insensitive, tolerant of
  separators): `api[-_]?key`, `access[-_]?token`, `auth[-_]?token`,
  `authorization`, `bearer`, `credential`, `password`, `secret`,
  `x-api-key`, `set-cookie`, `cookie`;
- secret-shaped strings anywhere: `Bearer …` header values, `sk-…` and
  `AIza…` API keys, and `-----BEGIN … PRIVATE KEY-----` PEM blocks.

On top of that, `cmd/nusallm/wire.go` installs a stricter redactor —
`redact.WithoutBodies`: `provider.request` entries are omitted entirely
(the normalized payload has no `body` key but would leak `messages`
content), `redact.Value` runs over the rest, then the `body`
key is deleted from map payloads, chunk lists collapse to `{"chunks": n}`, and
anything else becomes `{"omitted": true}`. The log retains event kinds
and counts without duplicating conversation content.

### Rotation and resume

`log.max_lines` (`LogConfig.MaxLines`, default 2000) rotates
`active.jsonl` once it holds that many records: the file is compressed to
the next numbered `.jsonl.gz` archive (`max+1`) and a fresh active file is opened.
`log.max_files` (`MaxFiles`, default 9) caps sealed archives — the oldest
are deleted past the cap. Zero selects the defaults; `Normalize` clamps
configured values to these maxima.

`New` resumes across restarts: it enumerates existing numbered segments so
numbering continues, and `prepareActive` scans the existing
`active.jsonl` before it is reopened — fail-stop, unlike the
conversation stream's torn-tail-only tolerance. The file is truncated at
the *first* line that is not a complete valid record, so a torn or
corrupt line drops itself and everything after it; the surviving prefix
is then counted so the rotation threshold accounts for prior runs. The
truncation is what lets `O_APPEND` reopen safely — a new record can
never be glued onto partial bytes — and for a diagnostic sink a lost
tail is cheaper than a misread stream. If rotation itself fails, the
logger reopens the active file rather than going permanently dead;
sealed segments stay intact.

## CLI state

`cli/` holds terminal-local state — currently a single `history` file for
the REPL's input history (`infrastructure/repository/history/history.go`).
It is kept deliberately private: the directory must be a real directory
(symlinks rejected) created `0700`, and the file must be a regular file
forced to `0600`, since submitted input can contain anything the user
typed.

The format is one entry per line, oldest first, with `\` → `\\` and
newline → `\n` escaping (unescaping scans pairs left-to-right so a literal
backslash before an `n` round-trips). Each submitted input is appended on
submit; loading is best-effort — malformed or unreadable content yields
whatever parsed, because history is a nicety and never a boot blocker.

## File permissions at a glance

| Path | Mode | Reason |
|---|---|---|
| `gateways/` | `0700` | records may carry credential references |
| `gateways/<id>.json` | `0600` | same |
| `conversations/<id>/` and its files | `0755` / `0644` | conversation content, no refs |
| `conversations/<id>/tool-output/*.log` | `0600` | conversation-owned full tool output |
| `logs/active.jsonl` | `0600` | may carry opted-in payloads |
| `cli/`, `cli/history` | `0700` / `0600` | private user input |
| `secrets.db` | `0600` under `0700` parent | key material, plaintext in a binary container |

## See also

- [Architecture](./architecture.md) — where repositories sit in the layering
- [Domain model](./domain-model.md) — the entities these records hydrate into
- [Adapters](./adapters.md) — who writes the diagnostic log
- [CLI guide](./cli-guide.md) — `gateway add/set`, `/compact`, `/edit` from the operator side
- [Extending](./extending.md) — implementing the repository ports against a different backend
- [README](../README.md)

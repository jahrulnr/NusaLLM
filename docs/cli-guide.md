# NusaLLM CLI Guide

`nusallm` is the terminal client for NusaLLM, a provider-agnostic LLM
conversation engine. One binary gives you an interactive REPL, a loopback
WebUI (`nusallm serve`), and admin subcommands for providers,
configuration, and offline conversation repair — no account system or telemetry.
The REPL and the WebUI are two transports over the same engine; the
subcommands exist to feed both providers and settings.

This guide covers installing the binary, wiring a provider, driving the
REPL, and the knobs the config file exposes. For what the engine is doing
underneath, see [Architecture](./architecture.md) and the
[Domain Model](./domain-model.md).

## Install

NusaLLM is a single Go module, `github.com/jahrulnr/nusallm`. It has no
cgo dependencies — the secret store uses a pure-Go SQLite
(`modernc.org/sqlite`), so `go build` produces the whole product. Use the
Go version declared in `go.mod` or newer.

```sh
# Build from source — binary lands in ./nusallm
go build -o nusallm ./cmd/nusallm

# Or via the Makefile — binary lands in ./bin/nusallm
make build

# Build and run in one step
make cli

# Loopback WebUI (HTTP API + browser UI + WebSocket turns)
make serve
# or: nusallm serve [--port N]
```

`serve` binds `127.0.0.1` only. The port comes from `server.port` in
config (default `10998`); `--port` overrides it for one process. Configured
`server.hosts` values are the accepted `Host`/`Origin` names for HTTP and
the WebSocket upgrade — see [WebUI API contract](./api/contract.md).

Running from source without building also works:

```sh
go run ./cmd/nusallm
```

Put the resulting binary anywhere on your `PATH`. There is no installer
and no system-level state — everything it writes lives under one data
directory, covered below.

Two soft dependencies are worth knowing about:

- `/diff` shells out to `git`; it only works in a workspace that is a git
  checkout.
- `/copy` uses the OS clipboard through `atotto/clipboard` — on Linux that
  means `xclip`, `xsel`, or `wl-clipboard` must be installed.

## Where things live

The client locates one config file and one data directory. Both resolve
relative to the directory you launch `nusallm` from, so running it from a
project directory keeps that project's state together.

- **Config file** — the `CONFIG_PATH` environment variable names the JSON
  config file. If it is unset, the process runs on built-in defaults and
  no file exists to edit (`nusallm config path` prints `(no config file —
  running on defaults)`). If it is set and the file is missing, the REPL
  writes a fully-default document there on first start and tells you:
  `nusallm: no config found; wrote defaults to <path>`. The REPL and
  `serve` write defaults on start (`admin.EnsureConfig`); `config set`
  writes the file too — other admin subcommands never create it.
- **`.env`** — before reading `CONFIG_PATH`, the process loads
  `KEY=VALUE` pairs from a `.env` file in the launch directory (comments
  with `#` allowed, quotes stripped). Real environment variables win over
  `.env` entries. The shipped `.env.example` contains exactly one line:
  `CONFIG_PATH=./data/config.json`.
- **Data directory** — `storage.dir` in the config file, default `./data`.
  Created on demand. Everything persistent lives under it:

| Path under `storage.dir` | Contents |
|---|---|
| `gateways/<id>.json` | one document per configured provider |
| `secrets.db` | SQLite secret store for vaulted API keys (mode `0600`) |
| `conversations/<id>/` | `meta.json` plus `active.jsonl` and sealed `N.jsonl` segments |
| `logs/` | provider diagnostics: `active.jsonl` plus rotated segments |
| `cli/history` | REPL input history (dir `0700`, file `0600`) |

The gateway files store credential *references* (`env:VAR` or
`secret:gateway/<id>/api_key`), never key material. Vaulted keys sit in
`secrets.db` as plaintext inside a SQLite table — it keeps them out of
`cat`/`grep` range, but it is not encryption. Protect the directory
accordingly.

## First run

The fastest path is the wizard:

```sh
nusallm onboard
```

`onboard` requires an interactive terminal; scripted setups use
`nusallm gateway add` directly. The wizard asks for a provider id
(default `openai`), a wire style picked by number, an endpoint (the
default is `https://api.openai.com/v1`, or `https://api.anthropic.com`
when you pick the `messages` style), a comma-separated model list, and an
API key typed on a hidden prompt. It writes exactly what
`gateway add` would and prints the `/route` and `/model` incantation for
your first session.

Then start the REPL:

```sh
nusallm
```

The welcome line reports how many gateways are configured and ready —
a gateway whose credential can't resolve (for example an `env:` reference
to an unset variable) counts as configured but not ready. If only one
healthy gateway exists, the REPL auto-selects it on your first message;
same for a gateway with exactly one listed model. Otherwise pick them:

```
/route openai
/model gpt-5
```

and type your message. Replies stream in as rendered markdown.

## Managing providers: `nusallm gateway`

A *gateway* is a named provider route: endpoint, wire style, credential,
and a catalog of model ids. Five subcommands manage them:

```text
nusallm gateway list                sanitized table of all gateways
nusallm gateway show <id>           detail plus the model catalog
nusallm gateway add <id> [flags]    register a new route
nusallm gateway set <id> [flags]    edit fields; key flags rotate the credential
nusallm gateway delete <id>         remove the route and its vaulted key
```

`list` prints one row per gateway — id, display name, style, model count,
key state, and a `broken` marker when the record can't be used. `show`
adds the endpoint, the stream flag, and one line per model. Neither ever
prints key material; the key column shows `key-set`, `no-key`, or the
stored reference (`env:VAR`, `secret:…`).

### `add` / `set` flags

The flag surface is identical for both commands (`admin/gateway.go` is
the source of truth):

| Flag | Meaning |
|---|---|
| `--style <s>` | wire style: `completion`, `responses`, `messages`, or `generatecontent` |
| `--endpoint <url>` | provider base URL — the adapter appends its path |
| `--name <name>` | display name; defaults to the id |
| `--models a,b,c` | replace the whole catalog; each entry's route equals its id |
| `--model <m>` | alias for `--models` with a single entry |
| `--stream` / `--no-stream` | toggle streaming (on by default) |
| `--key-env <VAR>` | store an `env:VAR` reference, resolved at send time |
| `--key <literal>` | vault a literal key — visible in `ps` and shell history |
| `--key-stdin` | read one key line from stdin, hidden on a TTY |
| `--clear-key` | drop the credential entirely (`set` only) |

Rules that bite:

- Exactly one key flag per invocation; combining `--key` with
  `--key-env`, for example, exits with a conflict error.
- `add` refuses an existing healthy id — use `set` to edit. It does
  accept an id whose stored record is corrupt, because re-adding is the
  repair path.
- `set` is read-modify-write: flags you pass replace fields, everything
  else keeps its stored value. `--models` swaps the entire catalog rather
  than appending.
- `set` refuses a *broken* record outright — repair it with a full `add`
  or `delete` it.
- On a TTY, `nusallm gateway add <id>` with missing fields drops into the
  same prompts the wizard uses, including a hidden API-key prompt.
  Piped stdin requires `--style` and `--endpoint`.
- `delete` also removes the canonical vault entry
  (`secret:gateway/<id>/api_key`) and warns when another gateway still
  references it.

Example — register OpenRouter with the key vaulted via stdin:

```sh
nusallm gateway add openrouter --style completion \
    --endpoint https://openrouter.ai/api/v1 \
    --models "anthropic/claude-sonnet-4,openai/gpt-5" \
    --key-stdin
```

Or keep the key in the environment instead:

```sh
nusallm gateway set openrouter --key-env OPENROUTER_API_KEY
```

An `env:` reference is resolved on every send, so rotating the variable
needs no CLI action — but a gateway pointing at an unset variable is
marked `broken` in `list` and cannot be selected in the REPL until the
variable exists.

### Styles, honestly

`--style` accepts `completion`, `responses`, `messages`, and
`generatecontent` — the enum values in
`infrastructure/domain/enum/style.go`, and all four have adapters wired in
`cmd/nusallm/wire.go`. Pick the wire your endpoint speaks:

- `completion` — OpenAI-shaped `POST <endpoint>/chat/completions`; covers
  OpenAI, OpenRouter, DeepSeek, Groq, xAI, Mistral, and the compatible
  long tail.
- `responses` — OpenAI's Responses API at `POST <endpoint>/responses`.
- `messages` — Anthropic at `POST <endpoint>/messages` (`x-api-key` auth).
- `generatecontent` — Gemini at
  `POST <endpoint>/models/{model}:generateContent` (`x-goog-api-key`).

See [Adapters](./adapters.md) for what each wire carries and where new
styles plug in.

## Managing settings: `nusallm config`

The config file holds tunables — never credentials — under four sections:

| Key | Type | Default | Effect |
|---|---|---|---|
| `conversation.mailbox_cap` | int | `128` | queued-job capacity per conversation |
| `conversation.max_turn_steps` | int | `512` | tool-call loop cap per turn |
| `conversation.compact_budget_pct` | int | `90` | percent of the model's input budget that triggers compaction |
| `conversation.compact_keep_tail` | int | `4` | messages kept live across a compaction |
| `service.header_timeout` | duration | `"30s"` | deadline for the provider's first response byte |
| `service.idle_timeout` | duration | `"90s"` | per-chunk deadline on a streaming body |
| `storage.dir` | string | `"./data"` | the data directory above |
| `log.max_lines` | int | `2000` | rotate `active.jsonl` past this many records |
| `log.max_files` | int | `9` | cap on compressed archives (10 files including active); oldest are pruned |

Durations decode with `time.ParseDuration` — `"30s"`, `"5m"`, `"1m30s"`
are all valid; a bare number is raw nanoseconds, which nobody wants, so
quote a string.

Subcommands:

```text
nusallm config show                 effective config — file values over defaults
nusallm config path                 resolved config file path (or "(no config file…)")
nusallm config set <key> <value>    patch one dotted leaf
nusallm config unset <key>          remove an override; defaulted keys reset to their default
nusallm config validate             decode check on the file
```

`set` takes a dotted leaf (`service.idle_timeout`), decodes the value as
a JSON literal (`5`, `true`, `"2m"`), and falls back to a plain string
for bare text — so `nusallm config set service.idle_timeout 2m` stores
`"2m"`. `null` is refused. `set` requires a real leaf in the schema —
typos fail instead of planting dead keys. `unset` removes an existing key
from the input document, including unknown keys left over from removed
fields or comment-as-field additions;
normalization restores its default when one exists. The file is rewritten
atomically as the full normalized document, so what
lands on disk is exactly what the loader computes (durations come back
canonical: `2m` becomes `"2m0s"`). Both `set` and `unset` print
"takes effect on next start"; in practice the REPL re-reads the
`conversation` section before each turn, while `service`, `storage`, and
`log` bind once at startup.

Strictness is split deliberately: the REPL refuses to boot on a file that
fails decode — malformed JSON, a mistyped known key, or trailing garbage
are errors, not warnings. Unknown keys are ignored so a stale field or a
comment-as-field never blocks boot. Gateway and onboarding commands use
lenient boot loading; `config` commands open the file directly so
`config unset` can clean keys a lenient load tolerates.

## Inside the REPL

The composer is the `›` prompt at the bottom of the screen. Enter submits
the buffer; Alt+Enter inserts a newline; the buffer grows to eight rows
before scrolling. Above it, a status line appears while a turn runs
(waiting → thinking → responding → `exec <tool>`), and the footer carries
badges: the workspace path, `route / model`, the conversation id, a
`~estimate/total` token gauge for the live context window, plus
`N attached` and `N queued` counts when relevant.

A turn works like this: your message persists first, then the agent loop
runs — provider call, tool calls if the model requests them, another
provider call — until the model stops calling tools or the step cap
(`conversation.max_turn_steps`) hits. Text you type while a turn runs is
queued and drains in order when the turn lands; `/steer <text>` injects
into the running turn instead — it lands as an ordinary user message at
the next safe point, after a tool result persists and before the
following provider call. A steer that misses the turn (it ended first)
queues as a new message rather than vanishing, and `Esc` recalls a
pending steer or queued line back into the composer. Only `/cancel`
interrupts outright. If the estimate reaches `compact_budget_pct` of the
model's input allowance, or the provider answers 413, the turn compacts
history once and retries rather than failing.

Every turn also records where the model is working: a `workspace`
context line — an application-written injection, not something you
typed — lands behind your message before each provider call so the
model sees the active path with the request itself. `/history` and
`/export` render it as a plain context line rather than an empty user
bubble, and the WebUI shows it the same way.

When you submit your first message in a session with no active
conversation, a fresh `c_<random>` conversation id is minted; the id
shows up in the footer and in `/status`.

### Keys

| Key | Effect |
|---|---|
| `Ctrl+C` | dismiss the palette → clear the draft → cancel the running turn → quit |
| `Esc` | dismiss the palette → recall a pending steer/queued line into the composer → cancel the running turn |
| `Ctrl+D` | quit when the composer is empty and no turn is running |
| `Tab` | accept the highlighted palette row |
| `Enter` | submit the buffer (or run the selected palette row) |
| `Alt+Enter` | newline inside the composer |
| `↑` / `↓` | navigate the palette; otherwise browse input history (from the first/last composer line) |

`Ctrl+C`'s ladder means each press peels one layer — you can't fat-finger
a quit while a draft or a turn exists, and a press on an idle, empty
composer exits. SIGTERM also shuts the process down cleanly.

### The slash palette

Type `/` and the palette opens under the composer, prefix-filtered as you
type. `↑`/`↓` move, `Tab` fills the command into the composer, `Enter`
runs it, `Esc` closes it until the buffer changes. Past the command word
the palette switches to argument completion for commands that provide
one — `/route` lists gateways, `/model` lists models on the current
route, `/resume` and `/delete` list saved conversations, `/edit` lists
editable messages, `/attach` walks the workspace file tree. Some rows are
marked "fill" (e.g. `/edit <id> ` or `/attach dir/`): they complete part
of the line instead of dispatching.

On a TTY, submitting a bare completable command — `/route`, `/model`,
`/resume`, `/delete`, `/edit` — parks the composer at the argument
position with the picker open, so you rarely need to type an id by hand.

### Command reference

Every command is registered in
`infrastructure/delivery/cli/commands/wire.go`; `/help` prints this same
table in-session.

| Command | What it does |
|---|---|
| `/attach [path]` | attach a workspace file to the next turn; bare lists pending attachments |
| `/cancel` | interrupt the active turn |
| `/clear` | clear the terminal screen (interactive only) |
| `/compact` | fold history into a checkpoint now |
| `/copy` | copy the last assistant answer to the clipboard |
| `/delete [id]` | delete a conversation — defaults to the active one |
| `/detach` | drop all pending attachments |
| `/diff` | show `git diff HEAD` for the workspace (64 KiB cap) |
| `/edit <id> <text>` | replace a user message and regenerate the turn |
| `/exit` | leave the REPL |
| `/export <file>` | write the transcript as markdown to a workspace file |
| `/help` | print the command list |
| `/history [count]` | show recent messages (default 20, max 200) |
| `/list` | list saved conversations |
| `/model [id]` | list or select a model on the current route |
| `/new` | start a fresh conversation, keeping route and model |
| `/pwd` | print the workspace path |
| `/quit` | alias for `/exit` |
| `/resume [id]` | resume a saved conversation and show its last 20 messages |
| `/route [id]` | list or select a gateway |
| `/status` | session state: conversation, counts, route, model, workspace |
| `/steer <text>` | inject into the running turn after the next tool result |
| `/title <text>` | rename the conversation |
| `/tools` | list the tools the model may call |
| `/usage` | cumulative token accounting for the conversation |

Notes on the less obvious ones:

- `/edit` targets a *message id* (`m_<random>`), and only real user
  messages are editable — compaction checkpoints and other synthetic
  entries are not. On a TTY, bare `/edit` opens a picker of the 50 most
  recent editable messages with content previews; outside it, ids are
  visible in `/export` headers (`## role · m_…`). Editing rewrites the
  message, drops everything after it, and regenerates the turn.
- `/compact` writes a model-generated handoff checkpoint and seals the
  older history into a segment; the most recent `compact_keep_tail`
  messages stay live. It fails cleanly ("nothing to compact") when there
  is nothing foldable.
- `/new` clears the active conversation id but keeps the selected route
  and model, so the next message starts a new room on the same provider.
- `/resume` clears your session route/model override so the
  conversation's own stored binding applies.
- `/model` lists only picker-visible catalog entries: a model whose
  declared context window is under 126K tokens is hidden — and refused if
  selected by id — while entries with no declared window stay visible.
  Gateways written through `gateway add --models` declare no window, so
  every CLI-managed model is selectable.
- `/export` writes `# conversation <id>` markdown with attachments and
  tool calls inlined as bracketed lines; workspace context records
  export under `## context · m_…` headings; the path is confined to the
  workspace.
- `/usage` counts tokens, not money — the CLI exposes no pricing fields,
  so the numbers are `input`, `cached read`, `cached write`, `output`,
  and `total` as reported by the provider.

Command names are case-insensitive and take their arguments as raw text.
A line starting with `//` is literal chat text, not a command. Typing
`exit` or `quit` bare prints a hint instead of sending it to the model.

### Attachments and the workspace

The workspace is the directory `nusallm` was launched from — `os.Root`
confines every relative path the client touches, and `/pwd` prints it.

`/attach <path>` stages a file for your next message:

- Paths are workspace-relative — `../x` and absolute paths fail with
  "path traversal is not allowed". Double-quoted paths work for names
  with spaces.
- Only non-text media attaches: images, audio, video, and other binary
  types are sniffed from content. A text file is refused with
  "…is text — use read_file instead" (the model can read text files
  itself through its tools).
- Size caps per modality: 10 MiB image, 50 MiB audio, 100 MiB video,
  20 MiB other (`wire.MaxAttachmentBytes`).
- Attached bytes are sha256-pinned at attach time and re-verified when the
  provider request is built, so editing the file between `/attach` and
  send fails rather than silently sending different content.
- If the selected model's catalog declares input modalities that exclude
  the attachment, a warning notes it will be omitted. Catalogs written by
  `gateway add --models` declare none, so nothing is filtered until the
  catalog grows modality data.

Attachments ride the next submitted turn — the pending list clears once
your message lands in history. `/detach` drops them manually, `/attach`
bare lists them, and the footer counts them.

### Conversations on disk

Conversations persist under `storage.dir/conversations/<id>/` — a
`meta.json` (title, route, model, usage counters) plus append-only JSONL
history. They survive restarts but nothing auto-loads them: a new REPL
starts detached. `/list` shows what's saved, `/resume <id>` reattaches
and replays the tail, `/history` pages the current room, `/delete`
removes it entirely, `/title` names it, `/export` dumps it as markdown.
`Ctrl+D` or `/exit` never loses anything — persistence is per-message,
not on exit. See [Persistence](./persistence.md) for the segment format
compaction produces.

If an interrupted compaction leaves one extra sealed segment, stop the app,
back up the affected conversation directory, then use
`nusallm conversation repair <id> --discard-duplicate` when its messages
still lead `active.jsonl`, or `--commit` when the active rewrite removed
them. The command checks the chosen case and recalculates counters when
committing. A missing segment needs restoration from backup; see the
[recovery procedure](./persistence.md#compact--sealing-history).

An edit interrupted mid-commit is a different window: the merged stream
stays staged as `active.jsonl.rewrite` and every read fails naming the
fix. A crash there can leave a mix of files — the sealed segments are
parked as `*.jsonl.rewrite-old` in ascending order from `0.jsonl`, so
the archive shows a parked prefix plus any numbered suffix, or, once the
publish landed, a payload already equal to `active.jsonl`. Only that
layout is diagnosed as an interrupted edit: stop the app and run
`nusallm conversation repair <id> --finish-rewrite`; it validates the
staged stream, publishes it, and removes the leftovers. Any other
mismatch — a parked name with a gap below it, a numbered segment the
parked set has passed, an extra or missing numbered file — keeps the
mismatch's own guidance instead, never the repair verb. The
[state machine](./persistence.md#rewritefrom--editing-a-message) covers
what the crash can and cannot leave behind.

### Tools the model can call

`/tools` lists what the model may invoke; the current wiring
(`cmd/nusallm/wire.go`) registers five, all at `agent` permission —
meaning they run without asking you first:

| Tool | Scope |
|---|---|
| `read_file` | read a UTF-8 text file under the workspace |
| `write_file` | create or overwrite a text file under the workspace (dirs `0700`, files `0600`) |
| `apply_patch` | literal search-and-replace inside a workspace file |
| `read_media` | load a non-text media file so the model can inspect it |
| `exec` | run a shell command, merged stdout/stderr — **unsandboxed** |

The file tools are confined to the workspace; `exec` is not — it runs in
the launch environment with a default 120 s timeout (the model may ask
for up to 600 s) and spills oversized output to
`<storage.dir>/conversations/<id>/tool-output/`. Spill files remain available
until the user deletes the conversation; deleting the conversation removes
them together with its history.
`/help` says it plainly: *exec is
unsandboxed — review command activity.* Watch the `exec <tool>` phase in
the status line.

## Non-interactive use

Pipe stdin and the REPL runs headless: each newline submits a line, slash
commands work, the palette is disabled, and EOF behaves as if you typed
`/exit` — queued work drains first.

```sh
printf '/route openai\n/model gpt-5\nSummarize this repo.\n' | nusallm
```

A bare `nusallm <token>` invocation is worth one caveat: a leading
argument that names an existing file, the binary name, or `core` is
stripped before dispatch (`cmd/nusallm/main.go`), so `nusallm notes.txt`
starts the REPL rather than complaining.

## Usage, logs, and diagnostics

Token accounting rides in three places: a dim `↳ N in · M out · T total`
line after each turn (cache counters appear when nonzero), the footer's
`~estimate/total` context gauge against the model's input allowance, and
`/usage` for cumulative per-conversation counters.

Provider diagnostics land in `storage.dir/logs/` as JSONL. `active.jsonl`
rotates every 2000 records into numbered `.jsonl.gz` archives, with at most
9 archives retained by default (10 log files total). Request, response, error, and stream bodies
are omitted; metadata and event counts remain without duplicating conversation
content in logs.

## Exit codes and errors

`0` clean, `1` an operation failed, `2` usage error (bad flags, unknown
subcommand, `onboard` without a TTY). Errors print the public message
plus the underlying cause — `invalid gateway` always says *why*. Inside
the REPL, failures land as `error: …` lines in scrollback; a failed turn
still leaves your user message in history, and the room keeps its
route/model binding so a retry needs no flags.

## See also

- [../README.md](../README.md) — the pitch and a sixty-second start
- [./architecture.md](./architecture.md) — layers and boundaries
- [./domain-model.md](./domain-model.md) — conversations, gateways, models, tools
- [./persistence.md](./persistence.md) — on-disk formats and compaction
- [./adapters.md](./adapters.md) — wire styles and provider translation
- [./extending.md](./extending.md) — adding commands, tools, and adapters

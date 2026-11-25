# ADR-0002: The workspace resolves what a path argument means

## Status

Accepted — ratified by PA in round 1 (proposed by SA in round 0).
Implementation is BE's T-01, against this ADR.

Citation errata (round 1): `write_file`'s raw-path use is
`write.go:74,80,87` — an earlier draft cited `169-183`, taken from a listing
that had `read.go` prepended to the file.

## Context

Every file-shaped tool takes a path argument written by a model, and today
three layers each decide a different part of what it means:

- `read_file` passes the string straight into the session's `os.Root`
  (`infrastructure/service/tool/file/read.go:73`) while its schema promises
  "Path relative to the workspace root" (`read.go:38`);
- `read_media` decides for itself that an absolute path is a normal call and
  opens it through the port's `OpenHost` (`read_media.go:89-95`);
- `write_file` uses the string as given for `Stat`, `MkdirAll` and `WriteFile`
  (`write.go:74,80,87` — corrected in round 1);
- the port already documents the asymmetry this produces — "read tools opt in
  when a call names an absolute path; writes never ride it"
  (`infrastructure/domain/repository/workspace.go:15-22`).

The observable results of that split (verified against this tree, see
`docs/audit/decisions.md` D-05): a leading `~` is a folder name, so
`write_file {"path":"~/notes.md"}` creates `./~/notes.md` inside the workspace;
an absolute read fails with `path escapes from parent` even though `read_media`
accepts the same path; a `file:` URL and a Windows `%VAR%` are nonsense paths.

The base's own ownership rules already say where the rule belongs:
`infrastructure/repository/AGENTS.md:28` gives `workspace/` "workspace-scoped
file access and path confinement", and `infrastructure/service/AGENTS.md:20`
forbids service adapters from opening, reading or writing host files directly.
What is missing is not a rule but a *statement* of the rule that all three
adapters and any future delivery route can obey.

ADR-0001 makes the workspace a per-conversation, per-driver binding. That is
the decisive constraint: `~`, `$HOME` and `%VAR%` mean something only relative
to the host that owns the workspace, so a local process helper applied before
the driver is asked is wrong for any non-local driver.

## Decision

1. **One owner.** The workspace repository — the driver's implementation of
   `repository.Workspace` — owns the whole meaning of a path argument: syntax,
   placement and confinement. Tool adapters and delivery routes pass the raw
   string and consume an opened handle, a resolved reference, or an error. They
   do not stat, clean, join, expand, or compare paths.

2. **Syntax is a pure function of host facts.** Decoding `file:` URLs, a
   leading `~` and Windows `%VAR%` lives in one mechanism package
   (`infrastructure/system/patharg`), with `(goos, sep, home, env)` injected so
   the Windows rules are testable on Linux. It knows nothing about workspaces,
   roots, `os.Root`, or file existence, and it is the local driver's
   implementation detail — a future `ssh`/`docker` driver decodes against its
   own host.

3. **Placement and confinement stay in the repository.** Given an expanded
   path, the repository decides the root of the operation: a relative path
   resolves in the turn's workspace directory (`dservice.WorkspaceDir`, else the
   driver's fallback); an absolute path inside that root resolves relative to
   it; an absolute path outside it is a **host read** for read-only tools
   (`OpenHost`, regular file, must exist) and an **error** for any tool that
   creates or replaces content, naming the workspace root. Everything reached
   by a relative name keeps going through `os.Root`.

4. **The asymmetry is the contract.** Reads may leave the workspace; writes may
   not. A room that must write elsewhere binds that directory as its workspace,
   or uses `exec`, which is deliberately unsandboxed and gated by permission
   (`docs/architecture.md:554`).

5. **Delivery resolves through the same capability.** A preview/download route
   takes the raw path, asks the workspace capability for a reader (plus size and
   mod time), and streams it; it never expands or stats on its own.

## Consequences

### Positive

- The base comes out of the fork's defect at the owner layer instead of copying
  NusaShell's adapter-side `placeOf`/`anchorOf`
  (`../NusaShell/infrastructure/service/tool/file/root.go:46-95`), which stats
  from a service adapter — a rule `infrastructure/service/AGENTS.md:20`
  forbids — and would be duplicated in the media tools.
- A non-local driver can implement the contract on its own host; nothing in
  `application/` or `infrastructure/service/` assumes the local filesystem.
- `read_file` and `read_media` stop disagreeing about the same argument.

### Negative

- `repository.Workspace` grows a resolve step, and its local implementation
  carries the anchor rule for a path whose parent chain is missing (needed only
  for reads of paths that do not exist yet — in practice, an error).
- `infrastructure/system/` gains a mechanism that is not a service manager;
  `infrastructure/AGENTS.md:31-33` describes `system/` as the service-manager
  port and should name the path-syntax reader in the same breath.
- Writes outside the workspace become an explicit error rather than a silent
  `./~` folder — a behaviour change for any caller that relied on the old
  literal-name interpretation (none in the tests).

## Alternatives considered

- **Adapter-side expansion (NusaShell's shape).** Smallest diff, but the folder
  rule is then written twice (file tools, media tools) and `os.Stat` runs in a
  service adapter; a remote driver would expand `~` against the wrong host.
- **Do nothing (keep strict confinement).** Rejected: `~` still silently
  creates a `./~` tree, and the same argument already works in `read_media`
  through `OpenHost`.
- **Expand inside `application/job` before the tool runs.** The use case would
  then know filesystem syntax and would still be wrong for a remote driver.
- **Allow writes outside the workspace too (NusaShell's `anchorOf`).** Rejected
  for the base: it contradicts the port's documented contract, and the
  anchor rule needs `os.Stat` per call in whatever layer implements it.

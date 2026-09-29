# ADR-0001: Select execution drivers per conversation

## Status

Accepted

## Context

Injecting one local workspace repository and runner in the composition root
removed direct storage I/O from tools, but still selected a single execution
environment for the whole process. A resumed conversation could not reliably
retain a Docker/SSH target, and mixing a remote runner with local file tools or
attachment resolution would produce inconsistent turns.

## Decision

Persist an `environment` identity on each conversation. The application
resolves `map[enum.Environment]EnvironmentDriver` for every Send, Edit, and
Compact. A driver supplies one workspace repository, a tool catalog (including
its command runner), and provider adapters whose media resolution uses that
workspace. The selected driver validates workspace overrides. If the stored
driver is unavailable, the turn fails; it never silently uses local. Changing
drivers without a workspace override clears the old driver's path.

Only the local driver is currently registered. `ssh` and `docker` are known
identities, not implemented backends. Conversation/config storage remains
application data, separate from the agent's execution environment.

## Consequences

### Positive

- Adding a backend requires implementing its ports and registering one bundle;
  tools and turn orchestration do not gain backend-specific branches.
- A conversation's target survives resume, edit, and compaction.
- Tests can register two fake drivers and verify isolated tool/provider use.

### Negative

- Provider adapters are instantiated per driver because attachment resolution
  is driver-specific.
- The local-only filesystem picker and CLI attachment session need their own
  driver-aware browsing/selection UX when a remote backend is introduced.
- Removing a registered driver makes its conversations unavailable until that
  driver is restored or the conversation is explicitly rebound.

## Alternatives Considered

- A single process-global driver: simple now, but cannot isolate conversations.
- Independent maps for workspace, runner, and provider adapters: allows one
  turn to accidentally mix local files with remote command execution.
- Per-tool `if ssh/docker/local` branches: duplicates routing logic and makes
  every new backend a tool-wide refactor.

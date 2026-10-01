# AGENTS.md — infrastructure/service

## Role

This tree implements **outbound adapters**: domain service and tool ports
translated into wire protocols and side effects. Persistent/local-storage I/O
goes through injected repository ports, including workspace files, media
resolution, and conversation-owned tool output. Process execution and network
transport remain service/delivery concerns. It normalizes
provider shapes into the domain model and back; it does not own
provider-independent business rules.

## Ownership

| Area | Owns | Does not own |
|------|------|----------------|
| Text completion adapter | One chat-completions-style request/response leg, streaming frames, finish reasons, provider-specific quirks on the wire | Route selection, retries, conversation mutations, compaction policy |
| Wire helpers | Reusable encoding and streaming mechanics shared by adapters | Model choice or session state |
| Tool implementations | Executing a named tool’s side effect (files, terminal, …) | Whether an agent may call a tool — decided in domain and enforced in the application turn |
| Workspace flow | Attachment selection and workspace-scoped tool behavior through `repository.Workspace` | Opening, reading, or writing host files directly |
| Slash commands | Per-command handlers, registry dispatch, shared completion helpers | Session use-case port — application command; input UX — delivery |

New external capability: add or extend an adapter here and keep the port
contract in domain (or the session port for slash commands). Wire instances
explicitly from composition roots, same spirit as tools in the main binary.

## Constraints

**Must**

- Reject malformed provider or transport data at the adapter; map failures to
  errors callers can handle.
- When behavior must hold for every provider, define it in domain or
  application and make all adapters follow that contract.
- Keep provider-specific replay or metadata in adapter-local namespacing —
  not as new fields on domain entities.
- Preserve a readable end-to-end flow for one request or one command; extract
  only for reuse, a testable algorithm, or a real protocol boundary.
- Inject storage repositories through constructors/options. Do not call
  `os.OpenRoot`, `os.ReadFile`, `os.WriteFile`, or similar host-storage APIs
  from service adapters; put those mechanics in `infrastructure/repository`.

**Must not**

- Encode conversation policy, editability, or tool permission inside tool or
  command handlers when domain or application already owns it.
- Let slash command handlers depend on concrete application structs — use the
  slash session port from the application command package.
- Split a single-use method into pass-through helpers only to shorten it.

## When you change behavior

1. Read root AGENTS.md and the domain layer guide before moving rules.
2. Trace from use case through port to adapter; place each decision at its
   owner and update siblings, not duplicate checks.
3. For slash commands, align help text and behavior with delivery when the
   REPL has a special path for the same user action.
4. After adapter changes, run tests that cover the port behavior, not
   internal helper names.

## Source of truth

Subpackages and constructors evolve. **Read the Go tree under this directory**
for current adapters, command packages, and wiring patterns. Do not treat
this file as a file list or API reference.

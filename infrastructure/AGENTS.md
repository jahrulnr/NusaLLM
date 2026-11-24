# AGENTS.md — infrastructure

`infrastructure/` contains the domain package plus implementations and
supporting mechanisms. The `domain/AGENTS.md` takes precedence for domain
files; `repository/AGENTS.md` and `service/AGENTS.md` add adapter rules.

Outside `domain/`, implement contracts and mechanics. Do not define new
conversation, model, tool-permission, or gateway business rules in config,
delivery, prompt loading, redaction, error mapping, or worker code. If a
mechanism exposes a missing rule, add the rule to its owning domain object
or use case, then make the mechanism obey that contract.

- `config/` parses and normalizes settings; defaults are configuration,
  not domain meaning.
- `worker/` schedules interruptible jobs without knowledge of conversations.
- `prompt/` loads prompt text; prompt content must not silently redefine a
  domain invariant.
- `error/` and `redact/` provide shared error and diagnostic mechanics.
- `system/` is the host-OS integration boundary — the service-manager
  port plus per-manager implementations selected by build tag and
  presence probing, and path-syntax decoding (`patharg`, added by T-01:
  `file:` URLs, a leading `~`, Windows `%VAR%`). Verbs are intent-shaped;
  manager vocabulary stays inside the implementation. Path **syntax** is
  all `system/` owns: which folder a path lands in, and whether it may be
  opened at all, is the workspace repository's decision (ADR-0002).
- `delivery/` is a transport boundary when implemented; parse requests,
  invoke application methods, and render responses there.

Keep adapters directional: domain ports do not import their implementations.
Avoid extracting one-use private helpers to satisfy a length target; follow
the root method-boundary rule.

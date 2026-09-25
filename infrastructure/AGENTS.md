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
- `delivery/` is a transport boundary when implemented; parse requests,
  invoke application methods, and render responses there.

Keep adapters directional: domain ports do not import their implementations.
Avoid extracting one-use private helpers to satisfy a length target; follow
the root method-boundary rule.

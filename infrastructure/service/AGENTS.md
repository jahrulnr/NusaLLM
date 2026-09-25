# AGENTS.md — infrastructure/service

Services implement domain service/tool ports using external protocols or
local I/O. They translate the normalized domain model; they do not become
the source of provider-independent business rules.

- `core/text/completion/` maps a Conversation, Gateway, and Model to the
  OpenAI-shaped chat-completions wire and decodes one assistant result.
  Request fields, streaming frames, finish reasons, and provider quirks
  belong with this adapter.
- `wire/` holds reusable SSE/media mechanics, without selecting models or
  changing conversation state.
- `tool/` performs the named tool's side effect. Permission to invoke it is
  decided from domain ToolSchema by the application turn, not inside the
  file/terminal/summary implementation.

Adapter code may reject malformed provider data and map transport failures.
If behavior is required regardless of provider, define it in the domain or
application owner and make every adapter follow that contract. Keep
provider-specific replay artifacts in namespaced `optionalContent` keys;
do not add provider fields to domain entities.

Keep one request/response flow legible. Extract reusable protocol parsing,
encoding, or a separately testable algorithm; do not split a method only
to reduce its line count.

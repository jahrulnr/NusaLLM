# Security Policy

## Reporting a vulnerability

Please don't open a public issue for a security problem. Report it privately through
GitHub's private vulnerability reporting: open the **Security** tab of this repository
and choose **Report a vulnerability**.

## Threat model

NusaLLM is a personal, single-user tool. It runs with your user's permissions, is built
for one person on one machine, and makes no security guarantees for shared or hosted use.

- **Agent actions run without approval.** Tool calls execute automatically during a turn
  you started. There is no confirmation step per call.
- **The agent acts only during a turn.** It never starts work on its own. The only
  background activity is a WebSocket keepalive and a model-list refresh per gateway. While
  `nusallm serve` runs, the refresh happens at startup and then hourly. It calls each
  provider's model-list endpoint with that gateway's key.
- **`exec` is not sandboxed.** It runs shell commands as your user, with your filesystem
  access and environment, and a default timeout of 120 seconds. The environment includes
  any keys exported to it, including keys referenced with `env:`.
- **Files outside the workspace.** `read_file`, `write_file`, and `apply_patch` are
  confined to the workspace. `read_media` accepts absolute host paths, and `exec` can reach
  anything your user can reach.
- **Prompt injection is best-effort.** Instructions can arrive through user messages, tool
  results, the system prompt, or the model provider or gateway in use. Tool results are
  wrapped in an `<untrusted_tool>` envelope before the model reads them. That reduces the
  risk but does not remove it. The consequences of injected instructions are outside this
  project's scope.
- **Providers and gateways are not trusted.** Requests go to the endpoint you configure,
  which may be a router or proxy. How it handles your data is outside this project's control.
- **No authentication on the local API.** The WebUI and API listen on `127.0.0.1` only.
  Host and Origin checks block cross-site browser requests, but any process on your machine
  that can reach the port can call the API.
- **Local data is not encrypted.** Keys you enter directly are stored as plaintext in a
  local SQLite file (mode 0600), and gateway records only reference them. Conversation
  records and the diagnostic log (`data/logs`) contain message content, including tool
  results.

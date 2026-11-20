# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.0] - 2026-11-21

### Added

- The WebUI is rebuilt as a small, fully vendored client in `web/`: Preact and htm components, Tailwind v4, marked, DOMPurify, highlight.js, Pele for diagrams, and slim-select. Nothing is loaded from a CDN, and there is no build step.
- Assistant replies render as streaming GitHub-flavored Markdown with footnotes and an allowlist of inline HTML (`<kbd>`, `<del>`, `<sup>`, `<dl>`, `<details>`). Rendering is block by block, so earlier blocks keep their DOM while a reply streams. Closed `mermaid` fences render as diagrams, and images are never fetched.
- Tool calls render as terminal-style cards. Shell commands show the command and its working directory, long input shows its head and long output its tail, both expand in place, and the trailing `(exit N)` becomes the card footer.
- Scrolling to the top of a thread loads older messages. A skeleton fills the gap, a failed page shows an inline retry, and the line being read stays in place.
- The provider editor sets each model's type and wire style, and the model picker groups models by gateway with a reasoning-effort choice.
- The README has a "The lighthouse: the WebUI" section with a streaming demo and screenshots (`docs/assets/webui/`), captured from a mock provider and demo data.

### Changed

- The WebUI replaces the old `web/widgets/*` tree with `web/components/*`, and the serve route for it is `/components/*`.
- `NUSALLM_WEB_DIR` serves the WebUI from a directory instead of the embedded copy, so edits show on reload. `make test-web` now syntax-checks the sources, and the `make e2e-web` target is removed.
- The README banner moved to `docs/assets/banner.png`.

### Fixed

- Code blocks no longer merge programming ligatures, so `>=` and `<=` render as typed.

- Hydration dedup no longer compares tool outputs verbatim: `EnvironmentDriver.ToolContexts` renders every enabled system-callable tool per pass and `appendToolContexts` anchors each record on the newest live (name, input) injection call, so a tool whose output carries volatile fields (a rendered timestamp) no longer re-records every step. A tool implements `job.ToolDeduper` (`DedupKey`) to attach its own output normalizer; changed records still land as injection calls on one system message.
- Duplicate tool-call ids are rejected at the entity (`Message.AddToolCall` returns an error) instead of silently making `SetToolOutput` ambiguous; all four wire decoders, hydration, and record decode propagate the failure.
- Gateway and model writes validate provider contracts up front: endpoints must be absolute http(s) URLs with a hostname, temperature stays within [0,2], TopP within [0,1], presence/frequency penalties within [-2,2], and a declared output limit cannot exceed the context window.
- A Responses API `incomplete` turn caused by `content_filter` reports `FinishReasonContentFilter` (a definitive blocked answer) instead of a retryable truncation.
- The chat model picker only offers chat-capable entries: `ModelListedInPicker` now filters non-chat model types alongside the context floor, the `/gateways` projection carries a `listed` flag each model, and the WebUI picker plus default selection honor it (absent stays eligible).
- Catalog imports pin each model's wire style from the models.dev `recommended_api` (chat-completions → completion, responses, messages) when it differs from the gateway's style, so opencode GPT entries run on Responses and Claude entries on Messages without manual pins. Google surfaces (generate-content, interactions) are not served under the gateway's base URL and stay unpinned; an explicit pin is never overridden.
- The picker rule is strict about text: `entity.Model.ServesChat` (replacing `ModelType.ChatCapable`) requires a declared type to be `text` and declared input/output modality lists to include text, so image generators, audio, decision, realtime (new `realtime` type), embedding, and rerank models no longer pass. The catalog adapter now reads the models.dev shape opencode zen/go serve (`type`, MIME-typed `modalities`, `limit`, `capabilities.reasoning.effort`, `canonical_id`), and imports backfill `modelType` — previously every opencode entry arrived without metadata and was listed.
- Focus no longer drops to the page body when a re-render replaces the control under it: the sidebar list restores focus to the same row (or its successor, skipping the disabled busy state), the conversation header maps focus to the rebuilt control, rename commit/cancel returns focus to the title button, the message editor focuses its textarea on open, and the desktop delete confirm opens on Keep.
- Leaving Settings through browser Back restores focus to its trigger, matching the close control.
- Provider manager: Save and Import lock each other in both directions, and an in-flight import is no longer released by closing the dialog.

## [0.0.0] - 2026-10-27

### Added

- `nusallm version` prints the build version; a build without a release version prints `dev`.
- The WebUI shows a newer GitHub release: a pill in the conversation header, one toast per release, and an About section in Settings with **Check for updates**. **Skip this version** hides the notice for that release.
- **Update now** installs the latest release from the WebUI. The binary is replaced only after its `SHA256SUMS` check passes, the service restarts, and a progress popup follows each step. Linux release builds only.
- Terminal REPL (`nusallm`) and a loopback WebUI (`nusallm serve`) over one conversation engine.
- Gateways for four wire styles (`completion`, `responses`, `messages`, `generatecontent`), set up with `nusallm onboard` or `nusallm gateway`.
- Conversations saved as they happen, with `/resume`, `/export`, `/edit`, and `/compact`.
- Tools the model can call: file tools (`read_file`, `write_file`, `apply_patch`, `read_media`), shell tools (`exec`, `exec_session`, `write_stdin`), web search and fetch (`web_search`, `web_fetch`), and skills (`skills`, `read_skill`). Custom executables on `PATH` run through `exec`.
- `nusallm serve install` registers the WebUI as a systemd user service where systemd is available; `nusallm uninstall` removes the binary and the service and keeps config and data.
- `scripts/install.sh` installs the latest Linux release (amd64 and arm64) after checking it against `SHA256SUMS`.
- Release automation: a new `VERSION` is tagged, its Linux binaries are built, and the release is published with the matching section of this file as its notes.

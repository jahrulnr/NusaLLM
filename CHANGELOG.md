# Changelog

All notable changes to this project will be documented in this file.
The current line (`0.0.x`) and `[Unreleased]` stay here; older minor lines
live under [`docs/changelogs/`](docs/changelogs/).

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

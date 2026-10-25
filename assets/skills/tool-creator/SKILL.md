---
name: tool-creator
description: Create, validate, and test user-owned executable tools invoked directly through NusaLLM terminal commands, with companion skills and no manifest registry.
---

# Custom tool creator

Create an executable for a reusable user-requested capability. Custom tools are
ordinary commands invoked through `exec` or `exec_session`; they are not
provider `tools[]` entries and need no Go registration or manifest.

## Choose the installation roots

Default to the active workspace:

- Executables: `<workspace>/.agents/tools/bin/`
- Companion skills: `<workspace>/.agents/skills/`

Use `--scope global` for `~/.agents/tools/bin/` and `~/.agents/skills/`.
Use `--tools-dir` and `--skills-dir` when the user specifies other roots.
Relative overrides resolve from the current working directory. NusaLLM reads
the executable directories from the `custom_tools` hydration record; the order
is workspace, global, configured `tools.paths`, application tools, managed
Python launchers, then the inherited host PATH.

## Initialize and customize

Resolve this skill's directory from `read_skill` and run:

```sh
python3 <tool-creator-directory>/scripts/init_tool.py json-echo
python3 <tool-creator-directory>/scripts/init_tool.py shared-report --scope global
python3 <tool-creator-directory>/scripts/init_tool.py custom-tool \
  --tools-dir /path/to/tools/bin --skills-dir /path/to/skills
```

The initializer creates a runnable `json-echo` wrapper, its implementation at
`json-echo.tool/main.py`, a `resources/` directory, and a companion skill with
`SKILL.md` plus `references/usage.md`. It refuses to overwrite an existing
wrapper, implementation directory, or companion skill. Choose a new name or
roots if any target already exists.

The generated Python tool accepts one JSON value as an optional positional
argument or on stdin, writes one JSON result to stdout, writes diagnostics to
stderr, and exits 0 on success, 2 for invalid input, or 1 for an unhandled
runtime failure. Replace `transform()` in `main.py` with the requested
behavior. Resolve tool-owned files from `RESOURCE_DIR`, not the caller's
working directory.

Python is not mandatory for custom tools. Use native binaries or executable
wrappers for shell, Node, Go, or another installed runtime when they better fit
the task. The initializer's shebang wrapper targets Unix and rejects Windows;
on Windows use a native executable or a `.cmd` wrapper. NusaLLM's `python` and
`python3` launchers reuse a suitable Python >=3.11 and <4 host interpreter or
provision a private CPython. First provisioning may require network access; a
cached runtime works offline. `nusallm python` provides the same interpreter
entrypoint when the binary is available. The launchers do not install
third-party packages.

## Validate and exercise the tool

Run the initializer's printed validation command, or invoke the validator
directly:

```sh
python3 <tool-creator-directory>/scripts/validate_tool.py json-echo
printf '{"message":"ready"}\n' | json-echo
```

The validator checks the generated paths, executable permission, companion
skill name/description, and local Markdown references. It runs `--help` and a
JSON probe with a 10-second timeout, so validation also verifies that the
wrapper, interpreter, and stdout result work from the current directory.

Test at least one representative input and a malformed-input case before
presenting a custom tool as ready. Inspect `stderr` and the process exit code;
keep successful results on stdout so callers can consume them.

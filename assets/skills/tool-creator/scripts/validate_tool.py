"""Validate a generated executable and its companion skill."""

import argparse
import json
import os
import subprocess
import sys
from pathlib import Path

sys.dont_write_bytecode = True

from tool_layout import (
    ToolLayout,
    missing_markdown_references,
    normalize_name,
    read_skill_metadata,
    resolve_layout,
)

PROBE_TIMEOUT_SECONDS = 10


def validate_layout(layout: ToolLayout) -> list[str]:
    """Check generated files, skill metadata, and local Markdown references."""
    errors = []
    if not layout.executable.is_file():
        errors.append(f"executable missing: {layout.executable}")
    elif os.name != "nt" and not os.access(layout.executable, os.X_OK):
        errors.append(f"executable is not marked runnable: {layout.executable}; run chmod +x")
    if not layout.tool_dir.is_dir():
        errors.append(f"implementation directory missing: {layout.tool_dir}")
    if not layout.implementation.is_file():
        errors.append(f"implementation missing: {layout.implementation}")
    if not layout.resources.is_dir():
        errors.append(f"resource directory missing: {layout.resources}")
    if not layout.skill_dir.is_dir():
        errors.append(f"companion skill directory missing: {layout.skill_dir}")
    if not layout.skill_file.is_file():
        errors.append(f"companion skill metadata missing: {layout.skill_file}")
    else:
        try:
            metadata = read_skill_metadata(layout.skill_file)
        except (OSError, ValueError) as exc:
            errors.append(f"invalid companion skill metadata in {layout.skill_file}: {exc}")
        else:
            if metadata.get("name") != layout.name:
                errors.append(
                    f"{layout.skill_file}: name must be {layout.name!r}, got {metadata.get('name')!r}"
                )
            description = metadata.get("description")
            if not isinstance(description, str) or not description.strip():
                errors.append(f"{layout.skill_file}: description must be a non-empty string")
    if not layout.usage_reference.is_file():
        errors.append(f"usage reference missing: {layout.usage_reference}")
    if layout.skill_dir.is_dir():
        try:
            errors.extend(missing_markdown_references(layout.skill_dir))
        except OSError as exc:
            errors.append(f"could not read companion skill references: {exc}")
    return errors


def validate_runtime(layout: ToolLayout) -> list[str]:
    """Run help and a JSON probe to verify the wrapper and interpreter."""
    errors = []
    try:
        help_result = subprocess.run(
            [str(layout.executable), "--help"],
            capture_output=True,
            text=True,
            timeout=PROBE_TIMEOUT_SECONDS,
        )
    except (OSError, subprocess.TimeoutExpired) as exc:
        return [f"could not run {layout.executable} --help: {exc}; check the Python 3 runtime and PATH"]
    if help_result.returncode != 0:
        errors.append(
            f"{layout.executable} --help exited {help_result.returncode}: "
            f"{help_result.stderr or help_result.stdout}"
        )

    try:
        result = subprocess.run(
            [str(layout.executable)],
            input=json.dumps({"probe": True}) + "\n",
            capture_output=True,
            text=True,
            timeout=PROBE_TIMEOUT_SECONDS,
        )
    except (OSError, subprocess.TimeoutExpired) as exc:
        return errors + [f"could not run the JSON probe for {layout.executable}: {exc}"]
    if result.returncode != 0:
        errors.append(
            f"JSON probe exited {result.returncode}: {result.stderr or result.stdout}"
        )
    else:
        try:
            json.loads(result.stdout)
        except json.JSONDecodeError as exc:
            errors.append(f"JSON probe did not write one JSON value to stdout: {exc}")
    return errors


def main(argv: list[str] | None = None) -> int:
    """Validate one tool layout and return a process status."""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("name", help="Tool name created by init_tool.py")
    parser.add_argument("--scope", choices=("workspace", "global"), default="workspace")
    parser.add_argument("--tools-dir", help="Override the executable directory")
    parser.add_argument("--skills-dir", help="Override the companion skill root")
    args = parser.parse_args(argv)
    try:
        name = normalize_name(args.name)
        layout = resolve_layout(name, args.scope, args.tools_dir, args.skills_dir)
    except ValueError as exc:
        parser.error(str(exc))
    if name != args.name.strip():
        parser.error(f"tool name must already be normalized as {name!r}")

    errors = validate_layout(layout)
    if not errors:
        errors.extend(validate_runtime(layout))
    if errors:
        for error in errors:
            print(f"ERROR: {error}", file=sys.stderr)
        return 1
    print(f"Valid custom tool: {name}")
    print(f"Command: {layout.executable}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

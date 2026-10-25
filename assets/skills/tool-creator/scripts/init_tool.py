"""Create a PATH executable, implementation template, and companion skill."""

import argparse
import json
import os
import shlex
import sys
from pathlib import Path

sys.dont_write_bytecode = True

from tool_layout import normalize_name, path_exists, resolve_layout

TEMPLATE_DIR = Path(__file__).resolve().parent.parent / "templates"


def render(template_name: str, values: dict[str, str]) -> str:
    """Render a bundled template using explicit placeholder values."""
    content = (TEMPLATE_DIR / template_name).read_text(encoding="utf-8")
    for key, value in values.items():
        content = content.replace(f"@@{key}@@", value)
    return content


def write_new(path: Path, content: str, mode: int = 0o644) -> None:
    """Write a new file exclusively and remove it if writing fails."""
    created = False
    try:
        with path.open("x", encoding="utf-8", newline="\n") as output:
            created = True
            output.write(content)
        path.chmod(mode)
    except OSError:
        if created:
            path.unlink(missing_ok=True)
        raise


def main(argv: list[str] | None = None) -> int:
    """Create the selected tool layout without replacing existing paths."""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("name", help="Tool name; normalized to lowercase hyphen-case")
    parser.add_argument("--scope", choices=("workspace", "global"), default="workspace")
    parser.add_argument("--tools-dir", help="Override the executable directory")
    parser.add_argument("--skills-dir", help="Override the companion skill root")
    parser.add_argument("--description", help="Companion skill description")
    args = parser.parse_args(argv)
    if os.name == "nt":
        parser.error("the generated shebang launcher targets Unix; use a native executable or .cmd wrapper on Windows")
    try:
        name = normalize_name(args.name)
        layout = resolve_layout(name, args.scope, args.tools_dir, args.skills_dir)
    except ValueError as exc:
        parser.error(str(exc))
    if name != args.name.strip():
        print(f"Normalized tool name to {name}.", file=sys.stderr)
    if layout.executable == layout.skill_dir:
        parser.error("tool and skill roots resolve to the same output path")

    conflicts = [
        path
        for path in (layout.executable, layout.tool_dir, layout.skill_dir)
        if path_exists(path)
    ]
    if conflicts:
        print("Cannot initialize without overwriting existing paths:", file=sys.stderr)
        for path in conflicts:
            print(f"  {path}", file=sys.stderr)
        return 2

    description = (
        args.description
        if args.description is not None
        else f"Use the {name} executable for its documented custom workflow."
    ).strip()
    if not description:
        parser.error("--description must not be empty")
    values = {
        "TOOL_NAME": name,
        "TITLE": name.replace("-", " ").title(),
        "DESCRIPTION": json.dumps(description, ensure_ascii=False),
    }
    created_dirs: list[Path] = []
    created_files: list[Path] = []
    try:
        layout.tools_root.mkdir(parents=True, exist_ok=True)
        layout.skills_root.mkdir(parents=True, exist_ok=True)
        conflicts = [
            path
            for path in (layout.executable, layout.tool_dir, layout.skill_dir)
            if path_exists(path)
        ]
        if conflicts:
            raise FileExistsError(", ".join(str(path) for path in conflicts))

        layout.tool_dir.mkdir()
        created_dirs.append(layout.tool_dir)
        layout.resources.mkdir()
        created_dirs.append(layout.resources)
        layout.skill_dir.mkdir()
        created_dirs.append(layout.skill_dir)
        references = layout.skill_dir / "references"
        references.mkdir()
        created_dirs.append(references)

        for path, template in (
            (layout.executable, "launcher.py.tmpl"),
            (layout.implementation, "main.py.tmpl"),
            (layout.skill_file, "SKILL.md.tmpl"),
            (layout.usage_reference, "usage.md.tmpl"),
        ):
            write_new(path, render(template, values), 0o755 if path == layout.executable else 0o644)
            created_files.append(path)
    except (OSError, ValueError) as exc:
        for path in reversed(created_files):
            path.unlink(missing_ok=True)
        for path in reversed(created_dirs):
            try:
                path.rmdir()
            except OSError:
                pass
        print(f"Could not initialize {name}: {exc}", file=sys.stderr)
        return 1

    print(f"Created executable: {layout.executable}")
    print(f"Implementation: {layout.implementation}")
    print(f"Companion skill: {layout.skill_file}")
    validate_command = [
        "python3",
        str(Path(__file__).resolve().with_name("validate_tool.py")),
        name,
        "--scope",
        args.scope,
    ]
    if args.tools_dir:
        validate_command.extend(("--tools-dir", args.tools_dir))
    if args.skills_dir:
        validate_command.extend(("--skills-dir", args.skills_dir))
    print(f"Validate with: {shlex.join(validate_command)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

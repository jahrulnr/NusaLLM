"""Shared paths and metadata checks for the tool-creator helpers."""

import json
import os
import re
from dataclasses import dataclass
from pathlib import Path
from urllib.parse import unquote, urlsplit

MAX_NAME_LENGTH = 64
_NAME = re.compile(r"^[a-z0-9]+(?:-[a-z0-9]+)*$")
_NUMBER = re.compile(r"^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$")
_FRONTMATTER = re.compile(r"\A---\r?\n(.*?)\r?\n---(?:\r?\n|\Z)", re.DOTALL)
_BLOCK_MARKER = re.compile(r"^([|>])([1-9]?)([+-]?)$")
_LINK = re.compile(r"\[[^\]]+\]\((<[^>]+>|[^)]+)\)")


@dataclass(frozen=True)
class ToolLayout:
    """Paths created and checked for one custom executable."""

    name: str
    tools_root: Path
    skills_root: Path

    @property
    def executable(self) -> Path:
        return self.tools_root / self.name

    @property
    def tool_dir(self) -> Path:
        return self.tools_root / f"{self.name}.tool"

    @property
    def implementation(self) -> Path:
        return self.tool_dir / "main.py"

    @property
    def resources(self) -> Path:
        return self.tool_dir / "resources"

    @property
    def skill_dir(self) -> Path:
        return self.skills_root / self.name

    @property
    def skill_file(self) -> Path:
        return self.skill_dir / "SKILL.md"

    @property
    def usage_reference(self) -> Path:
        return self.skill_dir / "references" / "usage.md"


def normalize_name(raw: str) -> str:
    """Normalize a requested tool name to a valid skill and executable name."""
    name = re.sub(r"[^a-z0-9]+", "-", raw.strip().lower()).strip("-")
    if not name:
        raise ValueError("tool name must include at least one letter or digit")
    if len(name) > MAX_NAME_LENGTH:
        raise ValueError(f"tool name exceeds {MAX_NAME_LENGTH} characters")
    if not _NAME.fullmatch(name):
        raise ValueError("tool name must use lowercase letters, digits, and single hyphens")
    return name


def resolve_layout(
    name: str,
    scope: str = "workspace",
    tools_dir: str | Path | None = None,
    skills_dir: str | Path | None = None,
) -> ToolLayout:
    """Resolve workspace/global defaults and any explicit root overrides."""
    if scope not in ("workspace", "global"):
        raise ValueError("scope must be 'workspace' or 'global'")
    base = Path.cwd() if scope == "workspace" else Path.home()
    default_tools = base / ".agents" / "tools" / "bin"
    default_skills = base / ".agents" / "skills"

    def absolute(raw: str | Path | None, fallback: Path) -> Path:
        path = Path(raw).expanduser() if raw is not None else fallback
        if not path.is_absolute():
            path = Path.cwd() / path
        return path.resolve()

    return ToolLayout(name, absolute(tools_dir, default_tools), absolute(skills_dir, default_skills))


def path_exists(path: Path) -> bool:
    """Return true for files, directories, and even dangling symlinks."""
    return os.path.lexists(path)


def _scalar(raw: str) -> object:
    raw = raw.strip()
    if not raw:
        return ""
    if raw.startswith('"'):
        try:
            return json.loads(raw)
        except json.JSONDecodeError as exc:
            raise ValueError(f"invalid double-quoted scalar: {exc}") from exc
    if raw.startswith("'"):
        if len(raw) < 2 or not raw.endswith("'"):
            raise ValueError("unterminated single-quoted scalar")
        return raw[1:-1].replace("''", "'")
    if " #" in raw:
        raw = raw.split(" #", 1)[0].rstrip()
    if raw.lower() in {"null", "~", "true", "false", "yes", "no"} or _NUMBER.fullmatch(raw):
        return None
    if raw.startswith(("[", "{")) or raw in {"|", "|-", "|+", ">", ">-", ">+"}:
        return None
    return raw


def _block_scalar(lines: list[str], index: int, marker: re.Match) -> tuple[str, int]:
    """Read one literal or folded YAML block and return its next line index."""
    style, explicit_indent, chomping = marker.groups()
    indentation = int(explicit_indent) if explicit_indent else None
    content = []
    while index < len(lines):
        line = lines[index]
        if line.strip() and not line[0].isspace():
            break
        if line.strip():
            leading = len(line) - len(line.lstrip())
            if indentation is None:
                indentation = leading
            if leading < indentation:
                break
            content.append(line[indentation:])
        else:
            content.append("")
        index += 1

    if style == ">":
        folded = []
        for line in content:
            if not line:
                folded.append("\n")
            else:
                if folded and not folded[-1].endswith("\n"):
                    folded.append(" ")
                folded.append(line)
        value = "".join(folded)
    else:
        value = "\n".join(content)
    if chomping == "-":
        value = value.rstrip("\n")
    elif chomping != "+":
        value = value.rstrip("\n") + "\n" if content else ""
    return value, index


def read_skill_metadata(path: Path) -> dict[str, object]:
    """Read top-level name and description scalars from companion frontmatter."""
    content = path.read_text(encoding="utf-8")
    match = _FRONTMATTER.match(content)
    if not match:
        raise ValueError("SKILL.md must start with a closed YAML frontmatter block")
    fields = {}
    lines = match.group(1).splitlines()
    index = 0
    while index < len(lines):
        line = lines[index]
        if not line.strip() or line.lstrip().startswith("#") or line[0].isspace():
            index += 1
            continue
        key, separator, raw = line.partition(":")
        key = key.strip()
        if not separator or not key:
            raise ValueError(f"invalid top-level frontmatter line {index + 1}")
        if key in fields:
            raise ValueError(f"duplicate frontmatter field {key!r}")
        stripped = raw.strip()
        if not stripped.startswith(('"', "'")) and " #" in stripped:
            stripped = stripped.split(" #", 1)[0].rstrip()
        marker = _BLOCK_MARKER.fullmatch(stripped)
        if marker:
            fields[key], index = _block_scalar(lines, index + 1, marker)
        elif stripped.startswith(("|", ">")):
            raise ValueError(f"invalid block scalar indicator on frontmatter line {index + 1}")
        else:
            fields[key] = _scalar(raw)
            index += 1
    return fields


def missing_markdown_references(root: Path) -> list[str]:
    """Return relative Markdown link targets that do not exist."""
    errors = []
    for source in sorted(root.rglob("*.md")):
        content = source.read_text(encoding="utf-8")
        for match in _LINK.finditer(content):
            target = match.group(1).strip()
            if target.startswith("<") and target.endswith(">"):
                target = target[1:-1]
            else:
                target = target.split(maxsplit=1)[0]
            parsed = urlsplit(target)
            if parsed.scheme or parsed.netloc or not parsed.path:
                continue
            local = source.parent / unquote(parsed.path)
            if not local.exists():
                errors.append(f"{source.relative_to(root)}: missing link target {parsed.path!r}")
    return errors

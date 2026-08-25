"""Reject functions that lack the project-required short purpose comment or docstring."""

from pathlib import Path
import re
import sys


def previous_nonempty(lines: list[str], index: int) -> str:
    """Return the closest preceding nonempty source line."""
    for line in reversed(lines[:index]):
        if line.strip():
            return line.strip()
    return ""


def check_go(path: Path) -> list[str]:
    """Return Go function declarations without an immediately preceding line comment."""
    errors: list[str] = []
    lines = path.read_text().splitlines()
    for index, line in enumerate(lines):
        if re.match(r"^func(?: \([^)]*\))? [A-Za-z_]", line.strip()) and not previous_nonempty(lines, index).startswith("//"):
            errors.append(f"{path}:{index + 1}: missing function comment")
    return errors


def check_python(path: Path) -> list[str]:
    """Return Python function declarations that lack a following docstring line."""
    errors: list[str] = []
    lines = path.read_text().splitlines()
    for index, line in enumerate(lines):
        if re.match(r"^\s*def [A-Za-z_]", line):
            following = next((candidate.strip() for candidate in lines[index + 1:] if candidate.strip()), "")
            if not following.startswith(('"""', "'''")):
                errors.append(f"{path}:{index + 1}: missing function docstring")
    return errors


def check_php(path: Path) -> list[str]:
    """Return PHP function declarations that lack a preceding documentation comment."""
    errors: list[str] = []
    lines = path.read_text().splitlines()
    for index, line in enumerate(lines):
        if re.match(r"^function [A-Za-z_]", line.strip()) and not previous_nonempty(lines, index).endswith("*/"):
            errors.append(f"{path}:{index + 1}: missing function documentation comment")
    return errors


def main() -> int:
    """Check all first-party function definitions and return a CI-friendly status code."""
    errors: list[str] = []
    for path in Path(".").glob("**/*"):
        if not path.is_file() or any(part in {".git", ".terraform", "docs"} for part in path.parts):
            continue
        if path.suffix == ".go":
            errors.extend(check_go(path))
        elif path.suffix == ".py":
            errors.extend(check_python(path))
        elif path.suffix == ".php":
            errors.extend(check_php(path))
    print("\n".join(errors))
    return 1 if errors else 0


if __name__ == "__main__":
    raise SystemExit(main())

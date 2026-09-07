#!/usr/bin/env python3
"""Validate documentation paths mentioned from source comments."""
from __future__ import annotations

import argparse
import re
import subprocess
import sys
from pathlib import Path

from agentic_os.config import is_enabled, is_excluded, load_excludes
from agentic_os.pre_commit.tree import is_repo_content

HOOK_ID = "source-doc-refs"
REPO_ROOT = Path.cwd()

LINE_COMMENT_PREFIXES = {
    ".bash": ("#",),
    ".c": ("//",),
    ".cc": ("//",),
    ".cpp": ("//",),
    ".cs": ("//",),
    ".go": ("//",),
    ".h": ("//",),
    ".hpp": ("//",),
    ".java": ("//",),
    ".js": ("//",),
    ".jsx": ("//",),
    ".kdl": ("//",),
    ".kt": ("//",),
    ".kts": ("//",),
    ".lua": ("--",),
    ".mjs": ("//",),
    ".py": ("#",),
    ".rb": ("#",),
    ".rs": ("//",),
    ".sh": ("#",),
    ".toml": ("#",),
    ".ts": ("//",),
    ".tsx": ("//",),
    ".yaml": ("#",),
    ".yml": ("#",),
    ".zsh": ("#",),
}

BLOCK_COMMENT_EXTS = {
    ".c",
    ".cc",
    ".cpp",
    ".cs",
    ".css",
    ".go",
    ".h",
    ".hpp",
    ".java",
    ".js",
    ".jsx",
    ".kdl",
    ".kt",
    ".kts",
    ".mjs",
    ".rs",
    ".ts",
    ".tsx",
}

ROOT_DOCS = {"AGENTS.md", "CODE-REVIEW.md", "README.md", "SSM.md"}
SKILL_DOC = (
    r"\.agents/(?:skills/[A-Za-z0-9_.-]+/SKILL"
    r"|composed/[A-Za-z0-9_.-]+/COMPOSED)\.md"
)
# Both narrative shelves, so a comment pointing at a moved or deleted guide
# fails the way a docs/ pointer does instead of going unread.
DOC_DIR = r"(?:docs|guides)"
EXPLICIT_DOC = rf"(?:/?(?:{DOC_DIR}/[A-Za-z0-9][A-Za-z0-9_.-]*\.md|{SKILL_DOC}))"
RELATIVE_DOC = rf"(?:\.\./)+(?:{DOC_DIR}/[A-Za-z0-9][A-Za-z0-9_.-]*\.md|{SKILL_DOC})"
ROOT_DOC = r"/?(?:AGENTS|CODE-REVIEW|README|SSM)\.md"
BARE_DOC = r"[a-z0-9]+(?:-[a-z0-9]+)+\.md"
DOC_REF_RE = re.compile(
    rf"(?<![\w./-])(?P<path>{EXPLICIT_DOC}|{RELATIVE_DOC}|{ROOT_DOC}|{BARE_DOC})"
    rf"(?P<anchor>#[A-Za-z0-9_.-]+)?(?![\w/-])"
)


def _git(args: list[str]) -> str:
    try:
        return subprocess.run(
            ["git", *args],
            capture_output=True,
            check=True,
            cwd=REPO_ROOT,
            text=True,
        ).stdout
    except (OSError, subprocess.CalledProcessError):
        return ""


def _rel_or_none(path: Path) -> Path | None:
    try:
        return path.resolve().relative_to(REPO_ROOT.resolve())
    except ValueError:
        return None


def _should_skip(rel: Path, excludes: list[str]) -> bool:
    return not is_repo_content(rel, REPO_ROOT) or is_excluded(rel, excludes)


def _is_source_file(rel: Path) -> bool:
    if rel.suffix in LINE_COMMENT_PREFIXES or rel.suffix in BLOCK_COMMENT_EXTS:
        return True
    return rel.name == "Dockerfile" or rel.name.endswith(".Dockerfile")


def iter_source_files(roots: list[Path]) -> list[Path]:
    excludes = load_excludes(HOOK_ID)
    out: list[Path] = []

    def add(path: Path) -> None:
        rel = _rel_or_none(path)
        if rel is None or not path.is_file():
            return
        if _should_skip(rel, excludes) or not _is_source_file(rel):
            return
        out.append(rel)

    for root in roots:
        if root.is_file():
            add(root.resolve())
            continue
        if root.is_dir():
            for path in sorted(root.rglob("*")):
                add(path)

    return sorted(set(out))


def tracked_source_files(paths: list[str]) -> list[Path]:
    if paths:
        return iter_source_files([Path(p).resolve() for p in paths])

    tracked = _git(["ls-files", "-z"])
    if not tracked:
        return iter_source_files([REPO_ROOT])

    excludes = load_excludes(HOOK_ID)
    out: list[Path] = []
    for entry in tracked.split("\0"):
        if not entry:
            continue
        rel = Path(entry)
        if _should_skip(rel, excludes) or not _is_source_file(rel):
            continue
        if (REPO_ROOT / rel).is_file():
            out.append(rel)
    return sorted(out)


def _comment_text(line: str, suffix: str, name: str) -> str | None:
    stripped = line.lstrip()
    if not stripped:
        return None
    if name == "Dockerfile" or name.endswith(".Dockerfile"):
        if stripped.startswith("#"):
            return stripped[1:].strip()
    for prefix in LINE_COMMENT_PREFIXES.get(suffix, ()):
        if stripped.startswith(prefix):
            return stripped[len(prefix) :].strip()
    if suffix in BLOCK_COMMENT_EXTS:
        for prefix in ("/*", "*", "*/"):
            if stripped.startswith(prefix):
                return stripped[len(prefix) :].strip()
    return None


def _candidate_paths(source: Path, ref: str) -> list[Path]:
    target = ref.split("#", 1)[0]
    if target.startswith("/"):
        return [REPO_ROOT / target.lstrip("/")]
    if target.startswith(("docs/", "guides/")) or target.startswith(
        (".agents/skills/", ".agents/composed/")
    ):
        return [REPO_ROOT / target]
    if target in ROOT_DOCS:
        return [REPO_ROOT / target]
    if target.startswith("../"):
        return [(REPO_ROOT / source.parent / target).resolve()]
    return [
        (REPO_ROOT / source.parent / target).resolve(),
        REPO_ROOT / "docs" / target,
        REPO_ROOT / "guides" / target,
    ]


def _format_target(path: Path) -> str:
    rel = _rel_or_none(path)
    return rel.as_posix() if rel is not None else str(path)


def check_file(rel: Path) -> list[str]:
    path = REPO_ROOT / rel
    violations: list[str] = []
    for line_no, line in enumerate(path.read_text(errors="replace").splitlines(), start=1):
        comment = _comment_text(line, path.suffix, path.name)
        if comment is None:
            continue
        for match in DOC_REF_RE.finditer(comment):
            ref = match.group("path")
            candidates = _candidate_paths(rel, ref)
            in_repo = [_rel_or_none(candidate) is not None for candidate in candidates]
            if not all(in_repo):
                violations.append(
                    f"{rel.as_posix()}:{line_no}: repo-escaping doc reference "
                    f"{ref} -> {', '.join(_format_target(c) for c in candidates)}"
                )
                continue
            if not any(candidate.exists() for candidate in candidates):
                violations.append(
                    f"{rel.as_posix()}:{line_no}: dead source doc reference "
                    f"{ref} -> {', '.join(_format_target(c) for c in candidates)}"
                )
    return violations


def main(argv: list[str] | None = None) -> int:
    if not is_enabled(HOOK_ID):
        print(f"{HOOK_ID}: disabled by repo config")
        return 0
    if argv is None:
        argv = sys.argv

    parser = argparse.ArgumentParser(
        prog="check-source-doc-refs",
        description="Find dead documentation paths referenced from source comments.",
    )
    parser.add_argument(
        "paths",
        nargs="*",
        help="Optional source files or directories to scan. Defaults to tracked files.",
    )
    ns = parser.parse_args(argv[1:])

    violations: list[str] = []
    for rel in tracked_source_files(ns.paths):
        violations.extend(check_file(rel))

    if not violations:
        print("source-doc-refs check: OK")
        return 0

    for violation in violations:
        sys.stderr.write(f"FAIL: {violation}\n")
    sys.stderr.write(f"\n{len(violations)} source doc reference violation(s).\n")
    return 1


if __name__ == "__main__":
    sys.exit(main())

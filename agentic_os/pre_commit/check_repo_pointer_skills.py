#!/usr/bin/env python3
"""pre-commit hook: assert every `repo-<name>` pointer skill is auto-generated.

Repo-pointer skills (`.agents/skills/repo-<name>/SKILL.md`) are fully generated
by `agentic_os.generators.generate_repo_pointer_skill` from a repo's GitHub description and
topics. They must never be hand-edited. This hook scans the current repo for any
`repo-*` skill and regenerates it offline from its own frontmatter, failing on
any drift in the pointer body or frontmatter and on a description that skipped
the cleaning step (emoji, em/en dash, missing `Triggers -` line).

Prefix-driven and categories.yaml-independent, so it fires in every consumer
repo, not just the ones that ship a skill categories spec. No-ops when the repo
has no skills surface.

Schema and rollout: see docs/features-agents.md.
"""

from __future__ import annotations

import sys
from pathlib import Path
from typing import NoReturn

from agentic_os.config import get_str_option, is_enabled
from agentic_os.generators.generate_repo_pointer_skill import (
    DEFAULT_ORG,
    SKILL_PREFIX,
    check_drift,
)
from agentic_os.pre_commit.tree import carries_content

HOOK_ID = "repo-pointer-skills"
TRACKER = "docs/features-agents.md"

SKILLS_DIR_CANDIDATES = (".agents/skills", ".claude/skills", "skills")

REGEN_HINT = (
    "  regenerate: aosguard ops forgejo repo get <owner> <name> "
    "| python -m agentic_os.generators.generate_repo_pointer_skill <name> --from-json - --repo-root <repo>"
)


def fail(msgs: list[str]) -> NoReturn:
    for m in msgs:
        print(f"check-{HOOK_ID}: {m}", file=sys.stderr)
    print(REGEN_HINT, file=sys.stderr)
    print(f"  see {TRACKER} for schema", file=sys.stderr)
    sys.exit(1)


def find_skills_dir(root: Path) -> Path | None:
    for candidate in SKILLS_DIR_CANDIDATES:
        d = root / candidate
        if d.is_dir():
            return d
    return None


def main() -> int:
    if not is_enabled(HOOK_ID):
        print(f"{HOOK_ID}: disabled by repo config")
        return 0

    root = Path.cwd()
    skills_dir = find_skills_dir(root)
    if skills_dir is None:
        return 0

    org = get_str_option(HOOK_ID, "org", DEFAULT_ORG)
    failures: list[str] = []
    for d in sorted(skills_dir.glob(f"{SKILL_PREFIX}*")):
        if not d.is_dir() or d.is_symlink():
            continue
        if not carries_content(d.relative_to(root), root):
            continue
        skill_md = d / "SKILL.md"
        if not skill_md.is_file():
            failures.append(f"{d.name} has no SKILL.md")
            continue
        failures.extend(check_drift(d.name, skill_md.read_text(), org))

    if failures:
        fail(failures)
    return 0


if __name__ == "__main__":
    sys.exit(main())

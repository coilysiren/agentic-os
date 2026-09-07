#!/usr/bin/env python3
"""Run the documentation-layout check across every git repo in the workspace.

Read-only sweep: reports which workspace repos pass, fail, or have the hook
disabled, plus per-repo violation detail. Honours each repo's own
[tool.agentic-os.documentation-layout] config (enabled flag + excludes).

Spans every git working tree under ~/projects/<org>/* via
agentic_os.config.iter_workspace_repos, not just the org dir the running
agentic-os checkout sits in. Override the root with $PROJECTS_ROOT.
See scripts/sweep-precommit.py.
"""
from __future__ import annotations

import sys
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

from agentic_os.pre_commit import check_documentation_layout as chk  # noqa: E402
from agentic_os import config as cfg  # noqa: E402


def run_for_repo(repo: Path) -> dict:
    chk.REPO_ROOT = repo
    cfg.REPO_ROOT = repo
    if not cfg.is_enabled(chk.HOOK_ID, repo):
        return {"status": "disabled", "violations": []}
    violations = (
        chk.check_docs_flatness()
        + chk.check_guides_flatness()
        + chk.check_markdown_locations()
        + chk.check_markdown_sizes()
        + chk.check_skill_flatness()
    )
    return {"status": "ok" if not violations else "fail", "violations": violations}


def main() -> int:
    repos = cfg.iter_workspace_repos()
    results = {}
    for repo in repos:
        try:
            results[repo.name] = run_for_repo(repo)
        except Exception as e:  # noqa: BLE001
            results[repo.name] = {"status": "error", "violations": [repr(e)]}

    passing = [n for n, r in results.items() if r["status"] == "ok"]
    disabled = [n for n, r in results.items() if r["status"] == "disabled"]
    failing = [(n, r["violations"]) for n, r in results.items() if r["status"] == "fail"]
    errored = [(n, r["violations"]) for n, r in results.items() if r["status"] == "error"]

    print("=" * 70)
    print(f"documentation-layout sweep: {len(repos)} git repos")
    print("=" * 70)
    print(f"\nPASSING ({len(passing)}):")
    for n in passing:
        print(f"  OK   {n}")
    print(f"\nDISABLED ({len(disabled)}):")
    for n in disabled:
        print(f"  --   {n}")
    print(f"\nFAILING ({len(failing)}), fewest violations first:")
    for n, v in sorted(failing, key=lambda x: len(x[1])):
        print(f"  FAIL {n}  ({len(v)} violations)")
    if errored:
        print(f"\nERRORED ({len(errored)}):")
        for n, v in errored:
            print(f"  ERR  {n}: {v}")

    print("\n" + "=" * 70)
    print("VIOLATION DETAIL (failing repos, fewest first)")
    print("=" * 70)
    for n, v in sorted(failing, key=lambda x: len(x[1])):
        print(f"\n### {n} ({len(v)} violations)")
        for line in v:
            print(f"  - {line}")
    return 0


if __name__ == "__main__":
    sys.exit(main())

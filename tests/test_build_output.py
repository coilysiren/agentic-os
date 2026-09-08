"""Tests for build output staying invisible to tree-walking hooks.

A gitignored bake was being read as the consuming repository's own content, so
`compose-bundles` followed by the commit gate failed on skills the repository
does not own. See sirens-echo#800 and docs/build-output-is-not-content.md.
"""
from __future__ import annotations

import yaml

import subprocess
from pathlib import Path

import pytest

from agentic_os import config
from agentic_os.pre_commit import check_dead_links as cdl
from agentic_os.pre_commit import check_documentation_layout as cdocs

# A misplaced skill under a path documentation-layout rejects, carrying a
# relative link that resolves in the catalogue and not in a bake.
BAKED = "agent/bundles/qa/content/skills/roster/role-qa/SKILL.md"
BAKED_BODY = "See [Shows](../personal-preference-shows/COMPOSED.md).\n"


def _write(root: Path, rel: str, body: str) -> None:
    path = root / rel
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(body, encoding="utf-8")


def _git(root: Path, *args: str) -> None:
    subprocess.run(
        ["git", *args],
        cwd=root,
        check=True,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )


def _checkout(root: Path, ignore: str | None) -> None:
    """A real git checkout, because the answer here comes from git itself."""
    _git(root, "init", "-q")
    _write(root, "README.md", "# Repo\n")
    # Every repo declares a band, a fixture repo included.
    _write(
        root,
        "pyproject.toml",
        '[tool.agentic-os.documentation-layout]\nband = "small"\n',
    )
    if ignore is not None:
        _write(root, ".gitignore", f"{ignore}\n")
    _write(root, BAKED, BAKED_BODY)
    _git(root, "add", "-A")
    _git(root, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "seed")


@pytest.fixture(autouse=True)
def _fresh_cache() -> None:
    config.reset_build_output_cache()


def _run_links(monkeypatch: pytest.MonkeyPatch, root: Path) -> int:
    monkeypatch.setattr(cdl, "REPO_ROOT", root)
    monkeypatch.setattr(config, "REPO_ROOT", root)
    return cdl.main(["check-dead-links"])


def _run_layout(monkeypatch: pytest.MonkeyPatch, root: Path) -> int:
    monkeypatch.setattr(cdocs, "REPO_ROOT", root)
    monkeypatch.setattr(config, "REPO_ROOT", root)
    # Ratify the fixture's own band: this file tests the tree walk, not the
    # band gate, which carries its own tests.
    contract = root / "_ratified_fixture.yaml"
    declared = config.get_str_option(cdocs.HOOK_ID, "band", "", root)
    contract.write_text(
        yaml.safe_dump({"repos": {root.name: {"band": declared}}}), encoding="utf-8"
    )
    monkeypatch.setattr(config, "_RATIFIED_PATH", contract)
    config._RATIFIED_CACHE.clear()
    return max(cdocs.main_placement(), cdocs.main_size())


def test_a_gitignored_bake_is_not_this_repos_markdown(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    _checkout(tmp_path, ignore="agent/bundles/")
    assert _run_layout(monkeypatch, tmp_path) == 0


def test_a_gitignored_bake_carries_no_dead_links(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    _checkout(tmp_path, ignore="agent/bundles/")
    assert _run_links(monkeypatch, tmp_path) == 0


# The controls. Without these the two above pass on a hook that checks nothing.


def test_the_same_tree_tracked_still_fails_layout(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    _checkout(tmp_path, ignore=None)
    assert _run_layout(monkeypatch, tmp_path) == 1


def test_the_same_tree_tracked_still_fails_links(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    _checkout(tmp_path, ignore=None)
    assert _run_links(monkeypatch, tmp_path) == 1


def test_a_tree_git_cannot_read_is_checked_as_before(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    # No checkout at all. Failing open here would silently stop every hook on a
    # tarball, a vendored copy, or a machine with no git.
    _write(tmp_path, "README.md", "# Repo\n")
    _write(tmp_path, BAKED, BAKED_BODY)
    assert config.is_build_output(BAKED, tmp_path) is False
    assert _run_layout(monkeypatch, tmp_path) == 1


def test_a_directory_holding_tracked_files_is_content(tmp_path: Path) -> None:
    # docs/ flatness reads directories, and a directory is never in git's file
    # list. Reading one as build output would retire that rule.
    _checkout(tmp_path, ignore="agent/bundles/")
    _write(tmp_path, "docs/nested/x.md", "")
    _git(tmp_path, "add", "-A")
    config.reset_build_output_cache()
    assert config.is_build_output("docs/nested", tmp_path) is False
    assert config.is_build_output("agent/bundles/qa", tmp_path) is True


def test_an_untracked_source_file_is_still_content(tmp_path: Path) -> None:
    # Untracked but not ignored is a file the author has not staged yet, not
    # build output, and a hook that skipped it would report a clean tree.
    _checkout(tmp_path, ignore="agent/bundles/")
    _write(tmp_path, "docs/new.md", "")
    config.reset_build_output_cache()
    assert config.is_build_output("docs/new.md", tmp_path) is False


# The other eleven walkers (agentic-os#1062). Each pair is the same shape as
# above: the bake is invisible, and the identical tracked tree still fails.

COMPOSE_SRC = "agent/bundles/qa/content/composed/role-qa/AGENTS.COMPOSE.md"
LOAD_POINT = "agent/bundles/qa/content/CLAUDE.md"


def _compose_checkout(root: Path, ignore: str | None, body: str) -> None:
    _git(root, "init", "-q")
    _write(root, "README.md", "# Repo\n")
    _write(root, "pyproject.toml", "")
    if ignore is not None:
        _write(root, ".gitignore", f"{ignore}\n")
    _write(root, COMPOSE_SRC, body)
    _git(root, "add", "-A")
    _git(root, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "seed")


def test_a_bake_is_not_an_agent_compose_source(tmp_path: Path) -> None:
    # A bundle carries copies of sources already measured, so counting one
    # charges the budget twice for the same prose.
    from agentic_os.pre_commit import check_agent_compose_size as size

    _compose_checkout(tmp_path, "agent/bundles/", "x" * 8_000)
    config.reset_build_output_cache()
    assert size.find_violations(tmp_path) == []


def test_the_same_compose_source_tracked_still_fails(tmp_path: Path) -> None:
    from agentic_os.pre_commit import check_agent_compose_size as size

    _compose_checkout(tmp_path, None, "x" * 8_000)
    config.reset_build_output_cache()
    assert size.find_violations(tmp_path) != []


def test_a_bake_is_not_a_dedup_source(tmp_path: Path) -> None:
    from agentic_os.pre_commit import check_agent_compose_dedup as dedup

    _compose_checkout(tmp_path, "agent/bundles/", "# Heading\n\nshared line\n")
    config.reset_build_output_cache()
    assert dedup.source_files(tmp_path) == []


def test_a_bake_carries_no_context_load_point(tmp_path: Path) -> None:
    # rglob for CLAUDE.md reaches straight into a bundle, which carries one.
    from agentic_os.pre_commit import check_context_load_points as clp

    _checkout(tmp_path, ignore="agent/bundles/")
    _write(tmp_path, LOAD_POINT, "not a pure pointer, this is prose\n")
    config.reset_build_output_cache()
    assert clp.load_point_files(tmp_path, []) == []


def test_the_same_load_point_tracked_is_seen(tmp_path: Path) -> None:
    from agentic_os.pre_commit import check_context_load_points as clp

    _checkout(tmp_path, ignore=None)
    _write(tmp_path, LOAD_POINT, "not a pure pointer, this is prose\n")
    _git(tmp_path, "add", "-A")
    config.reset_build_output_cache()
    assert Path(LOAD_POINT) in clp.load_point_files(tmp_path, [])


def test_every_walker_shares_one_skip_set() -> None:
    # Five drifted copies were how one fix reached two hooks and missed eleven.
    from agentic_os.pre_commit import tree

    for module in (
        "check_actions_run_one_line",
        "check_code_comments",
        "check_dead_links",
        "check_documentation_layout",
        "check_seed_skills",
        "check_source_doc_refs",
        "check_yaml_strict",
    ):
        mod = __import__(f"agentic_os.pre_commit.{module}", fromlist=["_"])
        own = getattr(mod, "SKIP_DIR_NAMES", None)
        assert own is None or own is tree.SKIP_DIR_NAMES, module


def test_a_hook_walking_into_a_skip_dir_still_checks_it(tmp_path: Path) -> None:
    # `.claude` is in SKIP_DIR_NAMES, so a hook rooted at .claude/skills got a
    # veto rather than a filter and exited clean having read nothing. #1183.
    from agentic_os.pre_commit import tree

    _checkout(tmp_path, ignore="agent/bundles/")
    _write(tmp_path, ".claude/skills/repo-x/SKILL.md", "")
    _git(tmp_path, "add", "-A")
    config.reset_build_output_cache()

    assert tree.is_repo_content(".claude/skills/repo-x", tmp_path) is False
    assert tree.carries_content(".claude/skills/repo-x", tmp_path) is True


def test_the_skipped_walk_root_gate_still_excludes_a_bake(tmp_path: Path) -> None:
    # The control. Dropping should_skip must not drop the build-output half.
    from agentic_os.pre_commit import tree

    _checkout(tmp_path, ignore="agent/bundles/")
    config.reset_build_output_cache()

    assert tree.carries_content("agent/bundles/qa", tmp_path) is False

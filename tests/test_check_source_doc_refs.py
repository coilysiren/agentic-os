"""Tests for source-comment documentation reference validation."""
from __future__ import annotations

import subprocess
from pathlib import Path

import agentic_os.config as cfg
from agentic_os.pre_commit import check_source_doc_refs as sdr


def _git(root: Path, *args: str) -> None:
    subprocess.run(["git", *args], cwd=root, check=True, capture_output=True)


def _repo(tmp_path: Path) -> Path:
    _git(tmp_path, "init")
    _git(tmp_path, "config", "user.email", "t@t")
    _git(tmp_path, "config", "user.name", "t")
    return tmp_path


def _write(repo: Path, rel: str, text: str) -> None:
    path = repo / rel
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text, encoding="utf-8")


def _run(monkeypatch, repo: Path) -> int:
    monkeypatch.chdir(repo)
    monkeypatch.setattr(sdr, "REPO_ROOT", repo, raising=True)
    monkeypatch.setattr(cfg, "REPO_ROOT", repo, raising=True)
    return sdr.main(["check-source-doc-refs"])


def test_missing_docs_path_in_source_comment_fails(
    monkeypatch, tmp_path: Path, capsys
) -> None:
    repo = _repo(tmp_path)
    _write(repo, "cmd/tool.go", "// See docs/missing.md.\npackage main\n")
    _git(repo, "add", "-A")

    assert _run(monkeypatch, repo) == 1
    err = capsys.readouterr().err
    assert "dead source doc reference" in err
    assert "docs/missing.md" in err


def test_existing_root_relative_doc_passes(monkeypatch, tmp_path: Path) -> None:
    repo = _repo(tmp_path)
    _write(repo, "docs/live.md", "# Live\n")
    _write(repo, "scripts/tool.sh", "# See docs/live.md.\n")
    _git(repo, "add", "-A")

    assert _run(monkeypatch, repo) == 0


def test_composed_entrypoint_reference_is_validated(
    monkeypatch, tmp_path: Path, capsys
) -> None:
    repo = _repo(tmp_path)
    _write(repo, ".agents/composed/live/COMPOSED.md", "# Live\n")
    _write(
        repo,
        "scripts/tool.sh",
        "# See .agents/composed/live/COMPOSED.md.\n"
        "# See .agents/composed/missing/COMPOSED.md.\n",
    )
    _git(repo, "add", "-A")

    assert _run(monkeypatch, repo) == 1
    err = capsys.readouterr().err
    assert ".agents/composed/missing/COMPOSED.md" in err
    assert ".agents/composed/live/COMPOSED.md" not in err


def test_yaml_embedded_shell_comment_is_scanned(
    monkeypatch, tmp_path: Path, capsys
) -> None:
    repo = _repo(tmp_path)
    _write(
        repo,
        ".forgejo/workflows/test.yml",
        "jobs:\n  test:\n    steps:\n      - run: |\n          # See docs/gone.md.\n",
    )
    _git(repo, "add", "-A")

    assert _run(monkeypatch, repo) == 1
    assert "docs/gone.md" in capsys.readouterr().err


def test_bare_hyphenated_doc_name_resolves_in_docs(
    monkeypatch, tmp_path: Path
) -> None:
    repo = _repo(tmp_path)
    _write(repo, "docs/agent-lifecycle.md", "# Agent lifecycle\n")
    _write(repo, "cmd/agent.go", "// See agent-lifecycle.md.\npackage main\n")
    _git(repo, "add", "-A")

    assert _run(monkeypatch, repo) == 0


def test_generic_markdown_names_are_not_path_intent(
    monkeypatch, tmp_path: Path
) -> None:
    repo = _repo(tmp_path)
    _write(
        repo,
        "agentic_os/pre_commit/check.py",
        "# SKILL.md and AGENTS.COMPOSE.md are schema names here.\n",
    )
    _git(repo, "add", "-A")

    assert _run(monkeypatch, repo) == 0


def test_excludes_skip_generated_source(monkeypatch, tmp_path: Path) -> None:
    repo = _repo(tmp_path)
    _write(
        repo,
        "pyproject.toml",
        '[tool.agentic-os.source-doc-refs]\nexcludes = ["generated/"]\n',
    )
    _write(repo, "generated/tool.go", "// See docs/missing.md.\npackage main\n")
    _git(repo, "add", "-A")

    assert _run(monkeypatch, repo) == 0


def test_a_guide_reference_resolves_like_a_docs_reference(
    monkeypatch, tmp_path: Path
) -> None:
    # guides/ is the second narrative shelf, so a pointer into it is checked
    # rather than skipped as an unrecognised path.
    repo = _repo(tmp_path)
    _write(repo, "guides/role-divergence.md", "# Guide\n")
    _write(repo, "cmd/tool.go", "// See guides/role-divergence.md.\npackage main\n")
    _git(repo, "add", "-A")

    assert _run(monkeypatch, repo) == 0


def test_a_dead_guide_reference_fails(monkeypatch, tmp_path: Path, capsys) -> None:
    repo = _repo(tmp_path)
    _write(repo, "cmd/tool.go", "// See guides/missing.md.\npackage main\n")
    _git(repo, "add", "-A")

    assert _run(monkeypatch, repo) == 1
    assert "guides/missing.md" in capsys.readouterr().err

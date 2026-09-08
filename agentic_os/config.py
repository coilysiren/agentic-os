"""Consumer-side exclude loading for tree-walking hooks.

Hooks in this package use `always_run: true` + `pass_filenames: false` and
do their own filesystem walks, which means pre-commit's framework-level
`exclude:` directive is bypassed. This module reads a per-repo config so
consumers can opt specific paths out of specific hooks.

Config search order:
    1. pyproject.toml at REPO_ROOT, key path [tool.agentic-os.<hook_id>]
    2. .agentic-os.toml at REPO_ROOT, key path [<hook_id>]

Schema per hook:
    excludes = ["src/pages/", "src/pages/**", "*.guardfile.md"]

Path semantics (gitignore-style globs over repo-relative POSIX paths):
    "dir/"        directory prefix - excludes everything under dir/
    "dir/**"      same, spelled as a recursive glob
    "*"           any run of characters except "/"
    "**"          any run of characters including "/" (crosses dirs)
    "?"           a single character except "/"
A pattern containing a "/" is anchored to the repo root ("docs/*.md"
matches docs/x.md but not docs/sub/x.md). A pattern with no "/" matches
the file's basename at any depth, so one wildcard covers a generated file
wherever it lands. Patterns and paths use forward slashes on every platform.

`is_build_output` covers the case no per-repo exclude should have to: a path
git already ignores is not repository content, so a tree-walking hook skips it
without being told. See docs/build-output-is-not-content.md.
"""
from __future__ import annotations

import json
import os
import re
import subprocess
import sys
from pathlib import Path, PurePosixPath
from typing import Iterable

if sys.version_info >= (3, 11):
    import tomllib
else:
    import tomli as tomllib

REPO_ROOT = Path.cwd()
HUMAN_WORKDIR_SUFFIX = "-workdir"


# Workspace enumeration: one shared walk of ~/projects/<org>/* git trees, so
# cross-repo tooling spans every org dir. See scripts/sweep-precommit.py.


def projects_root(root: Path | None = None) -> Path:
    """Root holding the per-org checkout dirs. Override with $PROJECTS_ROOT.

    ~/projects now holds per-org checkout dirs (coilysiren/, coilyco-bridge/,
    coilyco-flight-deck/), each a plain dir of git working trees rather than a
    repo itself, mirroring the GitHub org migration. This defaults to that
    ~/projects (also the global default cwd). An explicit `root` argument wins
    over the env var (used by tests).
    """
    if root is not None:
        return root
    env = os.environ.get("PROJECTS_ROOT")
    if env:
        return Path(env).expanduser()
    return Path.home() / "projects"


ORG_PROFILE_REPO = ".github"


def is_agent_managed_checkout(path: Path) -> bool:
    """Whether fleet tooling may treat ``path`` as an agent-managed checkout.

    A repo basename ending in ``-workdir`` reserves that checkout for manual
    human work. Agent automation must leave it invisible even when it carries
    a valid ``.git`` directory.
    """
    return not path.name.endswith(HUMAN_WORKDIR_SUFFIX)


def iter_workspace_repos(root: Path | None = None) -> list[Path]:
    """Every git working tree in the workspace, owner-agnostic.

    Walks each immediate child of `projects_root()`:
      * a child that is itself a git working tree (carries .git) is yielded
        directly - this is the single-org-root layout (root already points at
        an org dir like ~/projects/coilysiren).
      * otherwise the child is a dir-of-checkouts (an org dir like coilysiren/,
        coilyco-bridge/, coilyco-flight-deck/) and its own git-working-tree
        children are yielded.

    Handling both shapes means the default ~/projects root covers every org
    dir, while a $PROJECTS_ROOT pointed straight at one org dir still works (it
    mirrors the old single-root behaviour). Hidden dirs (.dispatch-worktrees
    and other scaffolding) are skipped at both levels, except the org-profile
    repo literally named ``.github``: every org owns one, and the plain dotfile
    skip made all three invisible to every fleet rollout. Human-only
    ``*-workdir`` checkouts are skipped wherever they appear. Returns repo
    directory Paths sorted by (org dir, repo name).
    """
    base = projects_root(root)
    if not base.is_dir():
        return []

    def visible(path: Path) -> bool:
        return not path.name.startswith(".") or path.name == ORG_PROFILE_REPO

    def is_checkout(path: Path) -> bool:
        return (
            path.is_dir()
            and visible(path)
            and (path / ".git").exists()
            and is_agent_managed_checkout(path)
        )

    repos: list[Path] = []
    for child in sorted(base.iterdir()):
        if not child.is_dir():
            continue
        if is_checkout(child):
            repos.append(child)
            continue
        if child.name.startswith("."):
            continue
        for grandchild in sorted(child.iterdir()):
            if is_checkout(grandchild):
                repos.append(grandchild)
    return repos


def _load_hook_section(hook_id: str, repo_root: Path | None = None) -> dict:
    root = repo_root or REPO_ROOT
    candidates = [
        (root / "pyproject.toml", ("tool", "agentic-os", hook_id)),
        (root / ".agentic-os.toml", (hook_id,)),
    ]
    for path, key_path in candidates:
        if not path.is_file():
            continue
        try:
            with open(path, "rb") as fh:
                data = tomllib.load(fh)
        except (tomllib.TOMLDecodeError, OSError):
            continue
        section: object = data
        for key in key_path:
            if not isinstance(section, dict) or key not in section:
                section = None
                break
            section = section[key]
        if isinstance(section, dict):
            return section
    return {}


def load_str_list(
    hook_id: str, key: str, repo_root: Path | None = None
) -> list[str]:
    """Return a list-of-strings option for a hook, or [] if unset/non-list."""
    section = _load_hook_section(hook_id, repo_root)
    value = section.get(key)
    if isinstance(value, list):
        return [str(p) for p in value if isinstance(p, str)]
    return []


def load_excludes(hook_id: str, repo_root: Path | None = None) -> list[str]:
    """Return exclude patterns for a hook, or [] if none configured."""
    return load_str_list(hook_id, "excludes", repo_root)


def get_str_option(
    hook_id: str, key: str, default: str, repo_root: Path | None = None
) -> str:
    """Return a string option for a hook, or `default` if unset or non-str."""
    section = _load_hook_section(hook_id, repo_root)
    value = section.get(key, default)
    return value if isinstance(value, str) else default


def is_enabled(hook_id: str, repo_root: Path | None = None) -> bool:
    """Return False only if `enabled = false` is set in the hook's config."""
    section = _load_hook_section(hook_id, repo_root)
    value = section.get("enabled", True)
    return bool(value)


def has_hook_config(hook_id: str, repo_root: Path | None = None) -> bool:
    """Return True if a `[tool.agentic-os.<hook_id>]` section is present.

    The opt-in signal for hooks that default off and only enforce once a repo
    declares them (e.g. seed-skills, enabled fleet-wide by Ansible).
    """
    return bool(_load_hook_section(hook_id, repo_root))


def get_int_option(
    hook_id: str, key: str, default: int, repo_root: Path | None = None
) -> int:
    """Return an int option for a hook, or `default` if unset or non-int.

    `bool` is rejected (it is an int subclass but never a meaningful cap).
    """
    section = _load_hook_section(hook_id, repo_root)
    value = section.get(key, default)
    if isinstance(value, bool) or not isinstance(value, int):
        return default
    return value


def get_bool_option(
    hook_id: str, key: str, default: bool, repo_root: Path | None = None
) -> bool:
    """Return a bool option for a hook, or `default` if unset or non-bool."""
    section = _load_hook_section(hook_id, repo_root)
    value = section.get(key, default)
    if not isinstance(value, bool):
        return default
    return value


def _glob_to_regex(pattern: str) -> re.Pattern[str]:
    """Convert a gitignore-style glob to a regex.

    Semantics:
        * matches any chars except /
        ** matches any chars including /
        ? matches a single char except /
        Other characters are matched literally.
    """
    out: list[str] = []
    i = 0
    n = len(pattern)
    while i < n:
        c = pattern[i]
        if c == "*":
            if i + 1 < n and pattern[i + 1] == "*":
                out.append(".*")
                i += 2
            else:
                out.append("[^/]*")
                i += 1
        elif c == "?":
            out.append("[^/]")
            i += 1
        else:
            out.append(re.escape(c))
            i += 1
    return re.compile("^" + "".join(out) + "$")


def is_excluded(rel_path: Path | str, patterns: Iterable[str]) -> bool:
    """Match a repo-relative path against the exclude pattern list.

    A pattern containing a "/" is anchored to the repo root; a slash-less
    pattern matches the path's basename at any depth (the gitignore
    convention), so one wildcard covers a generated file wherever it is
    emitted. See the module docstring for the full glob grammar.
    """
    s = str(PurePosixPath(str(rel_path).replace("\\", "/")))
    basename = s.rsplit("/", 1)[-1]
    for raw in patterns:
        pattern = raw.replace("\\", "/")
        if pattern.endswith("/**"):
            prefix = pattern[:-3]
            if s == prefix or s.startswith(prefix + "/"):
                return True
            continue
        if pattern.endswith("/"):
            if s.startswith(pattern):
                return True
            continue
        target = s if "/" in pattern else basename
        if _glob_to_regex(pattern).match(target):
            return True
    return False


# Build output is never repository content. See docs/build-output-is-not-content.md.

_UNSET = object()
_TREE_CACHE: dict[Path, object] = {}


def _git_tree(repo_root: Path) -> tuple[frozenset[str], frozenset[str]] | None:
    """Files git would carry, plus their ancestor dirs, or None for no answer."""
    try:
        completed = subprocess.run(
            ["git", "ls-files", "-z", "--cached", "--others", "--exclude-standard"],
            cwd=repo_root,
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
            check=False,
        )
    except (OSError, ValueError):
        return None
    if completed.returncode != 0:
        return None
    listed = completed.stdout.decode("utf-8", "surrogateescape").split("\0")
    files = frozenset(entry for entry in listed if entry)
    # An empty answer is indistinguishable from a checkout git declined to read,
    # and reading it as "nothing is content" would silently stop every hook.
    if not files:
        return None
    dirs = set()
    for entry in files:
        parent = PurePosixPath(entry).parent
        while str(parent) != ".":
            dirs.add(str(parent))
            parent = parent.parent
    return files, frozenset(dirs)


def reset_build_output_cache() -> None:
    """Drop the cached git answer. One hook run reads one tree, so only a test
    that rewrites a checkout between calls needs this."""
    _TREE_CACHE.clear()


def is_build_output(rel_path: Path | str, repo_root: Path | None = None) -> bool:
    """Whether git would not carry this path, so no hook should read it.

    Tracked plus untracked-but-not-ignored is git's own definition of what the
    repository holds. These hooks walk the filesystem rather than git's file
    list, so a baked tree that `git status` shows as absent was still being read
    as this repository's own content. See sirens-echo#800.

    Returns False whenever git cannot answer - no checkout, no git, a failed
    call - so a hook never silently stops checking. Directories count as content
    when anything under them does, which keeps a directory-shaped rule such as
    docs/ flatness working.
    """
    root = repo_root or REPO_ROOT
    tree = _TREE_CACHE.get(root, _UNSET)
    if tree is _UNSET:
        tree = _git_tree(root)
        _TREE_CACHE[root] = tree
    if tree is None:
        return False
    files, dirs = tree  # type: ignore[misc]
    s = str(PurePosixPath(str(rel_path).replace("\\", "/")))
    return s not in files and s not in dirs


# Central ratification of documentation exclusions, so the escape hatch does not
# live in the repo under pressure. See guides/ratifying-an-exclusion.md.

_RATIFIED_PATH = Path(__file__).with_name("documentation_exclusions.json")
_RATIFIED_CACHE: dict[str, object] = {}


def current_repo_name(repo_root: Path | None = None) -> str:
    """Repo slug from origin (worktree-safe), falling back to the toplevel dir.

    A linked worktree's directory name is the task branch rather than the repo,
    so the remote is read first and the directory is only the fallback.
    """
    root = repo_root or REPO_ROOT
    try:
        url = subprocess.run(
            ["git", "-C", str(root), "config", "--get", "remote.origin.url"],
            capture_output=True,
            text=True,
            check=True,
        ).stdout.strip()
    except (subprocess.CalledProcessError, OSError):
        url = ""
    if url:
        slug = url.rstrip("/").rsplit("/", 1)[-1]
        return slug[:-4] if slug.endswith(".git") else slug
    try:
        top = subprocess.run(
            ["git", "-C", str(root), "rev-parse", "--show-toplevel"],
            capture_output=True,
            text=True,
            check=True,
        ).stdout.strip()
    except (subprocess.CalledProcessError, OSError):
        top = ""
    return Path(top).name if top else root.name


def load_ratification(path: Path | None = None) -> dict:
    """The parsed ratification contract, cached per path."""
    target = path or _RATIFIED_PATH
    key = str(target)
    cached = _RATIFIED_CACHE.get(key)
    if cached is None:
        try:
            with open(target, "rb") as fh:
                cached = json.load(fh)
        except (OSError, ValueError):
            cached = {}
        _RATIFIED_CACHE[key] = cached
    return cached if isinstance(cached, dict) else {}


def ratified_patterns(
    key: str,
    declared: Iterable[str],
    repo_root: Path | None = None,
    path: Path | None = None,
) -> tuple[list[str], list[str]]:
    """Split declared patterns into (effective, unratified) for this repo.

    Effective is the intersection with the central list, so a pattern present
    in only one of the two places grants nothing. Unratified is returned rather
    than dropped: a declaration that quietly stops working is the silent pass
    this mechanism exists to prevent, so the caller reports it.

    An absent or unreadable contract ratifies nothing, which fails closed.
    """
    repos = load_ratification(path).get("repos")
    entry = repos.get(current_repo_name(repo_root)) if isinstance(repos, dict) else None
    allowed = entry.get(key) if isinstance(entry, dict) else None
    allowed_set = set(allowed) if isinstance(allowed, list) else set()
    effective: list[str] = []
    unratified: list[str] = []
    for pattern in declared:
        (effective if pattern in allowed_set else unratified).append(pattern)
    return effective, unratified

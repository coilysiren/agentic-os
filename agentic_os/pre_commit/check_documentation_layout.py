#!/usr/bin/env python3
"""Enforce repo documentation placement and size.

This module is the single source of truth for Markdown size caps across the
agentic-os ecosystem. Docs (AGENTS.md, SKILL.md.template, handbook.md, etc.)
should point here by reference rather than restating numbers, so the caps
can never drift between code and prose.

Markdown documentation may live only in:
    1. the repo root, with a small universal filename allow-list;
    2. docs/*.md, with no docs subdirectories;
    3. guides/*.md, with no guides subdirectories (see below);
    4. skill folders (.agents/skills, .agents/composed, .claude/skills, or
       skills), which may carry support subdirs. No nested skill entry point
       may hide below the top-level skill dir;
    5. anywhere under an `examples/` directory at any depth, any .md filename;
    6. a co-located module README.md, but only in one of two tightly-capped
       shapes (see below). Any other co-located Markdown is still a violation.

guides/ is narrative, docs/ is reference
----------------------------------------
`docs/` answers "how does X work" and is capped short and flat so the set
stays scannable. `guides/` answers "how do I do Y, end to end", and its value
is the sequence, the worked example and the failure modes - none of which
survive being cut to reference length. A guide truncated into a reference page
is just a worse reference page, so the two shelves take different caps rather
than sharing one.

Guides do not count toward the docs/ count cap. A repo sitting at its docs cap
can still add a guide, which is the case that forced the type: a walkthrough
had nowhere legal to live because the reference shelf was full, and splitting
or merging reference pages answers neither.

The guide shelf carries no count cap of its own. Guide count tracks what a repo
ships rather than how much narrative it invented, so a repo adding a seat or a
target earns a guide with it, and a count cap there fails the repo while naming
a fix (fold into docs/) that a full reference shelf makes impossible.

The type is opt-in by directory: a repo with no guides/ is unaffected and
needs no config. See docs/documentation-bands.md for the caps and the
derivation behind them.

Module README.md shapes
-----------------------
A README.md anywhere below the root (outside docs/, skill, examples) is the
one co-located doc the layout permits, because deep docs are invisible to
anyone not running `rg`. To keep substance in the central, browsable docs/
index rather than scattered through the tree, the co-located README may only
be one of two tightly-capped shapes - the cap is the forcing function, since
nothing worth hiding fits in 3 lines:

    * outpost  - a pure redirect into docs/. <= 3 non-blank lines: a heading,
      an optional one-sentence summary, and exactly one link to a single
      docs/*.md file. Reciprocal: that docs file must link back to this exact
      README path (file-to-file). One docs file may be the target of many
      outposts (each gets its own back-link); one outpost points at one doc.
    * homestead - self-contained signage that points nowhere. <= 3 non-blank
      lines (heading + up to 2 content lines), no docs/ pointer.

Both cap prose lines at README_MAX_PROSE_CHARS. The pointer line of an
outpost is exempt from that cap - a deep relative path is not prose. Blank
lines never count toward the line ceiling. The discriminator is simple: a
README that links a docs/*.md file is an outpost, otherwise it is a
homestead.

Most Markdown shares one size cap, and how big it is depends on the repo's
declared band. Every repo sets `band = "small"` or `band = "large"` under the
hook config and gets that band's line, char, and docs-count caps. Declaring
is mandatory in both directions: silence is a missing decision rather than a
small repo. There is no per-file escape from a size or count cap: `excludes`
still govern placement
and flatness, and no longer reach either cap. docs/FEATURES.md is not special.
CLAUDE.md is expected to be a one-line `@AGENTS.md` pointer.

Placement and size ship as two hooks, `documentation-placement` and
`documentation-size`, so a repo whose Markdown is site content rather than
documentation drops the caps and keeps the placement rules (#1111). Both read
their tunables from the one `documentation-layout` config section, and
`enabled = false` there still disables both.

SKILL.md and COMPOSED.md take no size cap from here. check-skills owns them
through categories.yaml, and a skill overflows into its own references/ rather
than into docs/.

A repo may declare `vendored` path prefixes whose Markdown takes no size cap,
for a tree whose shape it does not own: an SDK it vendors, or copy an external
surface renders. Prefixes only, and placement still applies.

The count cap exists because a per-doc cap on its own does not bound a docs
folder, it reshapes it. A repo that caps length and not count answers every
over-long doc by splitting it, which is how one reached 156 files with not a
single one over the char cap.

The root README.md and AGENTS.md carry each project's living overview (intro
and operating doctrine), so both get more room than ordinary Markdown.
README.md uses TRIFECTA_MAX_LINES / TRIFECTA_MAX_CHARS. AGENTS.md gets a larger
default because universal person and operating context must fire eagerly while
selective capability detail moves into ordinary and composed skills.
README.md only at repo root; a co-located module README stays on the tight
outpost / homestead shape.

AGENTS.md sets its own cap, per-repo, via config keys `agents_md_max_lines` /
`agents_md_max_chars` under the documentation-layout hook section. The declared
value replaces the default outright, in either direction: a loader-bound
AGENTS.md holding universal-fire doctrine that can't split into docs/*.md may
buy headroom, and a repo whose role-scoped doctrine has already moved out to
the sources that own it may set a tighter cap as deliberate back-pressure.
Repos that don't set them get the shared AGENTS.md default.

The root README.md gets the same per-repo override, via `readme_max_lines` /
`readme_max_chars`. It is the launch-grade front page a cold visitor (human or
agent) reads to decide in under a minute whether the tool is for them, so a
release repo may need room to say who it's for and what it requires, show a
denial, and route into docs/ - more than the trifecta default gives. Repos
that don't set the keys keep the trifecta cap. Only the root README.md opts up;
a co-located module README stays on the tight outpost / homestead shape.
"""
from __future__ import annotations

import re
import sys
from pathlib import Path, PurePosixPath

from agentic_os.config import (
    get_int_option,
    get_str_option,
    is_build_output,
    is_enabled,
    is_excluded,
    load_excludes,
    load_str_list,
    ratified_patterns,
)
from agentic_os.pre_commit.tree import should_skip

REPO_ROOT = Path.cwd()
HOOK_ID = "documentation-layout"
PLACEMENT_HOOK_ID = "documentation-placement"
SIZE_HOOK_ID = "documentation-size"
# Per-repo size bands: lines, chars, and docs count. Rationale and the
# measured chars-per-line behind the pairs: docs/documentation-bands.md.
BAND_CAPS = {
    "small": (40, 3_000, 20),
    "large": (120, 8_000, 40),
}
# Declaring is mandatory in both directions (docs/documentation-bands.md).
# This is only what the caps resolve to while a repo is red for not declaring.
UNDECLARED_BAND = "small"

DOCS_DIRNAME = "docs"
GUIDES_DIRNAME = "guides"
# Mechanism, worked example, failure story - a reference page each, so a guide
# takes twice the band rather than a hand-set number (documentation-bands.md).
GUIDE_SIZE_FACTOR = 2

# Co-located module README.md caps (outpost / homestead shapes; see docstring).
# Non-blank lines per README, and prose chars per line (pointer line exempt).
README_MAX_LINES = 3
README_MAX_PROSE_CHARS = 90

# Inline Markdown link: [text](target). Reference-style links are not matched -
# outposts and back-links use inline links.
MD_LINK_RE = re.compile(r"\[[^\]]*\]\(([^)\s]+)\)")

# The broad overview files get more room than a focused docs/ inventory page -
# bounded, not infinite. See the module docstring.
TRIFECTA_MAX_LINES = 160
TRIFECTA_MAX_CHARS = 12_500

# AGENTS.md carries universal-fire context after selective capability detail
# moves into ordinary and composed skills. Repos may set their own via config.
AGENTS_DEFAULT_MAX_LINES = TRIFECTA_MAX_LINES * 2
AGENTS_DEFAULT_MAX_CHARS = TRIFECTA_MAX_CHARS * 2

# The root README.md - the launch-grade front page - defaults to the trifecta
# cap and opts higher per-repo via readme_max_lines / readme_max_chars.
README_DEFAULT_MAX_LINES = TRIFECTA_MAX_LINES
README_DEFAULT_MAX_CHARS = TRIFECTA_MAX_CHARS

# Exempt by basename. CODE_OF_CONDUCT.md is verbatim upstream, and the skill
# entrypoints belong to check-skills, as does `references/` below.
SIZE_CAP_EXEMPT_BASENAMES = {
    "CODE_OF_CONDUCT.md",
    "SKILL.md",
    "COMPOSED.md",
}

# The org landing page, whose shape belongs to Forgejo and GitHub rather than
# to this layout. See docs/documentation-bands.md.
ORG_PROFILE_README = Path("profile") / "README.md"


def vendored_trees(repo_root: Path | None = None) -> list[str]:
    """Path prefixes whose Markdown shape this repo does not own."""
    return load_str_list(HOOK_ID, "vendored", repo_root)


def size_excluded_trees(repo_root: Path | None = None) -> list[str]:
    """Ratified path prefixes this repo owns but sizes differently.

    `excludes` governs placement and deliberately does not reach the size caps,
    and `vendored` says the Markdown is not ours. A monorepo that co-locates a
    README and docs/ under each component fits neither: those docs are ours,
    and the root-plus-flat-docs shape the caps assume does not describe them.

    Both exclusion keys are ratified centrally, so what a repo declares here is
    a request rather than a grant. An unratified pattern is reported by
    `check_exclusion_ratification` instead of silently applying.
    """
    declared = load_str_list(HOOK_ID, "size_excludes", repo_root)
    effective, _ = ratified_patterns("size_excludes", declared, repo_root)
    return effective


def placement_excludes(repo_root: Path | None = None) -> list[str]:
    """Ratified placement excludes, the intersection of local and central."""
    effective, _ = ratified_patterns(
        "excludes", load_excludes(HOOK_ID, repo_root), repo_root
    )
    return effective


def check_exclusion_ratification(key: str, repo_root: Path | None = None) -> list[str]:
    """Every declared exclusion with no central entry, as a violation.

    Dropping an unratified pattern quietly would trade one silent escape for
    another: the repo would read as excluded and the hook would disagree. The
    remedy is the second pull request, so the message names it.
    """
    declared = (
        load_excludes(HOOK_ID, repo_root)
        if key == "excludes"
        else load_str_list(HOOK_ID, key, repo_root)
    )
    _, unratified = ratified_patterns(key, declared, repo_root)
    return [
        f"{key} pattern {pattern!r} is not ratified for this repo. Add it to "
        f"agentic_os/documentation_exclusions.json in agentic-os with a written "
        f"reason, release the hook, and bump this repo's pin. Until then it "
        f"grants nothing."
        for pattern in unratified
    ]


def is_vendored(rel: Path, patterns: list[str]) -> bool:
    """A file under a declared vendored tree takes no size cap.

    Prefixes only, never a bare basename: a basename would exempt one filename
    everywhere, which is the per-file escape hatch the count cap removed.
    """
    if not patterns:
        return False
    s = rel.as_posix()
    return any(
        s.startswith(prefix if prefix.endswith("/") else prefix + "/")
        for prefix in patterns
    )

ROOT_MARKDOWN_ALLOWLIST = {
    "AGENTS.md",
    # agent-compose's disjoint root source. Has its own size/dedup hooks
    # (check-agent-compose-*), and no AGENTS.md/CLAUDE.md cascade loads it.
    "AGENTS.COMPOSE.md",
    "CLAUDE.md",
    "CODE_OF_CONDUCT.md",
    "CODE-REVIEW.md",
    "CONTRIBUTING.md",
    "GOVERNANCE.md",
    "LICENSE.md",
    "README.md",
    "SECURITY.md",
    "SUPPORT.md",
}

# agent-compose per-harness override AGENTS.<harness>.md sits beside its base.
# Lowercase token mirrors a harness slug, so uppercase AGENTS.COMPOSE.md misses.
HARNESS_OVERRIDE_RE = re.compile(r"^AGENTS\.[a-z][a-z0-9-]*\.md$")


def is_harness_override(name: str) -> bool:
    return bool(HARNESS_OVERRIDE_RE.match(name))


SKILL_PATHS = (
    (".agents", "skills"),
    (".agents", "composed"),
    (".claude", "skills"),
    ("skills",),
)


def is_under_skill_path(rel: Path) -> bool:
    parts = rel.parts
    for skill_parts in SKILL_PATHS:
        n = len(skill_parts)
        if len(parts) >= n and parts[:n] == skill_parts:
            return True
    return False


def is_skill_reference(rel: Path) -> bool:
    """A file under a skill's `references/` takes no size cap.

    check-skills caps SKILL.md and answers an over-long one with "move detail
    into a sibling references/ file", capping nothing there itself, and
    docs/skill-discipline.md states outright that reference files are not
    capped. A band cap on that sibling fails the author for following the
    remedy the suite handed them, which is the same two-hooks-disagree defect
    agentic-os#1110 fixed for the entrypoints and did not carry to references.
    """
    return is_under_skill_path(rel) and "references" in rel.parts[:-1]


def markdown_files(apply_excludes: bool = True) -> list[Path]:
    excludes = placement_excludes() if apply_excludes else []
    out: list[Path] = []
    for path in REPO_ROOT.rglob("*.md"):
        rel = path.relative_to(REPO_ROOT)
        if should_skip(rel):
            continue
        if is_excluded(rel, excludes) or is_build_output(rel, REPO_ROOT):
            continue
        out.append(rel)
    return sorted(out)


def _check_flatness(dirname: str) -> list[str]:
    root = REPO_ROOT / dirname
    if not root.is_dir():
        return []
    excludes = placement_excludes()
    violations: list[str] = []
    for path in sorted(root.rglob("*")):
        rel = path.relative_to(REPO_ROOT)
        if should_skip(rel):
            continue
        if is_excluded(rel, excludes) or is_build_output(rel, REPO_ROOT):
            continue
        if path.is_dir() and path != root:
            violations.append(
                f"{rel.as_posix()}: {dirname}/ must stay flat. Use filename prefixes "
                f"instead of {dirname} subdirectories."
            )
    return violations


def check_docs_flatness() -> list[str]:
    return _check_flatness(DOCS_DIRNAME)


def check_guides_flatness() -> list[str]:
    return _check_flatness(GUIDES_DIRNAME)


def is_guide(rel: Path) -> bool:
    """A flat guides/<name>.md. Anything deeper is a placement violation."""
    return len(rel.parts) == 2 and rel.parts[0] == GUIDES_DIRNAME


def is_under_examples(rel: Path) -> bool:
    # Go/Rust examples/<name>/... is idiomatic at any depth and may contain
    # .md files of any name, not just README.md.
    return "examples" in rel.parts


def _normalize_link_target(target: str, source_rel: Path) -> str | None:
    """Resolve a Markdown link to a repo-root-relative POSIX path.

    `source_rel` is the repo-relative path of the file the link lives in.
    Anchors/queries are stripped, `..`/`.` are collapsed, and external or
    in-page links (http(s):, mailto:, bare #anchor) return None. The result is
    suitable for comparing two links for reciprocity regardless of whether
    they were written root-absolute (/docs/x.md) or relative (../docs/x.md).
    """
    target = target.strip().split("#", 1)[0].split("?", 1)[0]
    if not target:
        return None
    if target.startswith(("http://", "https://", "mailto:", "//")):
        return None
    if target.startswith("/"):
        raw = PurePosixPath(target.lstrip("/"))
    else:
        raw = PurePosixPath(source_rel.parent.as_posix()) / target
    parts: list[str] = []
    for part in raw.parts:
        if part in (".", ""):
            continue
        if part == "..":
            if parts:
                parts.pop()
            continue
        parts.append(part)
    return "/".join(parts) if parts else None


def _is_docs_md(norm: str) -> bool:
    parts = norm.split("/")
    return len(parts) == 2 and parts[0] == "docs" and parts[1].endswith(".md")


def _check_reciprocity(
    readme_rel: Path, docs_target: str, repo_root: Path
) -> list[str]:
    """The docs file an outpost points at must link back to that outpost."""
    docs_path = repo_root / docs_target
    if not docs_path.is_file():
        return [
            f"{readme_rel.as_posix()}: outpost points at {docs_target}, which "
            f"does not exist."
        ]
    docs_rel = Path(docs_target)
    readme_posix = readme_rel.as_posix()
    text = docs_path.read_text(encoding="utf-8", errors="replace")
    for match in MD_LINK_RE.finditer(text):
        if _normalize_link_target(match.group(1), docs_rel) == readme_posix:
            return []
    return [
        f"{readme_posix}: outpost <-> {docs_target} link is not reciprocal. "
        f"{docs_target} must link back to {readme_posix} (file-to-file)."
    ]


def validate_module_readme(rel: Path, repo_root: Path) -> list[str]:
    """Validate a co-located README.md as an outpost or a homestead.

    Outpost: <= 3 non-blank lines, exactly one docs/*.md link, reciprocal.
    Homestead: <= 3 non-blank lines, no docs/*.md link. Prose lines (every
    non-blank line except an outpost's pointer line) cap at 90 chars; blank
    lines do not count toward the line ceiling.
    """
    path = repo_root / rel
    text = path.read_text(encoding="utf-8", errors="replace")
    nonblank = [ln.strip() for ln in text.splitlines() if ln.strip()]
    rel_posix = rel.as_posix()

    docs_targets: list[str] = []
    pointer_lines: set[int] = set()
    for i, line in enumerate(nonblank):
        for match in MD_LINK_RE.finditer(line):
            norm = _normalize_link_target(match.group(1), rel)
            if norm and _is_docs_md(norm):
                docs_targets.append(norm)
                pointer_lines.add(i)

    shape = "outpost" if docs_targets else "homestead"
    violations: list[str] = []

    if len(nonblank) > README_MAX_LINES:
        violations.append(
            f"{rel_posix}: module README ({shape}) has {len(nonblank)} "
            f"non-blank lines, max {README_MAX_LINES}. Move the body into a "
            f"docs/*.md file and leave a pointer, or trim to signage."
        )

    if nonblank and not nonblank[0].startswith("#"):
        violations.append(
            f"{rel_posix}: module README ({shape}) first line must be a "
            f"Markdown heading."
        )

    for i, line in enumerate(nonblank):
        if i in pointer_lines:
            continue
        if len(line) > README_MAX_PROSE_CHARS:
            violations.append(
                f"{rel_posix}: prose line {i + 1} is {len(line)} chars, max "
                f"{README_MAX_PROSE_CHARS}."
            )

    if docs_targets:
        distinct = sorted(set(docs_targets))
        if len(distinct) > 1:
            violations.append(
                f"{rel_posix}: outpost points at {len(distinct)} docs files "
                f"({', '.join(distinct)}); it may point at exactly one."
            )
        for target in distinct:
            violations += _check_reciprocity(rel, target, repo_root)

    return violations


def check_markdown_locations() -> list[str]:
    violations: list[str] = []
    for rel in markdown_files():
        if len(rel.parts) == 1:
            if rel.name not in ROOT_MARKDOWN_ALLOWLIST and not is_harness_override(
                rel.name
            ):
                allowed = ", ".join(sorted(ROOT_MARKDOWN_ALLOWLIST))
                violations.append(
                    f"{rel.as_posix()}: top-level Markdown filename is not allowed. "
                    f"Allowed root Markdown files: {allowed}. Move one-off "
                    f"docs into docs/."
                )
            continue
        if rel.parts[0] == DOCS_DIRNAME and len(rel.parts) == 2:
            continue
        if is_guide(rel):
            continue
        if is_under_skill_path(rel):
            continue
        if is_under_examples(rel):
            continue
        if rel == ORG_PROFILE_README:
            continue
        if rel.name == "README.md":
            # The one co-located doc the layout allows, held to the tight
            # outpost / homestead shape instead of banned outright.
            violations += validate_module_readme(rel, REPO_ROOT)
            continue
        violations.append(
            f"{rel.as_posix()}: Markdown files may live only at repo root, docs/*.md, "
            f"guides/*.md, a skill folder, or a capped module README.md."
        )
    return violations


def check_skill_flatness(repo_root: Path | None = None) -> list[str]:
    """Flag nested sub-skills, not support material.

    The skill loader only sees top-level skill dirs. SKILL.md or COMPOSED.md
    nested below the top level is invisible and must move up. Support subdirs
    are fine because the rule targets hidden sub-skills.
    """
    root = repo_root or REPO_ROOT
    excludes = placement_excludes(root)
    violations: list[str] = []
    for skill_parts in SKILL_PATHS:
        skill_root = root.joinpath(*skill_parts)
        if not skill_root.is_dir():
            continue
        for skill_dir in sorted(skill_root.iterdir()):
            if not skill_dir.is_dir():
                continue
            if should_skip(skill_dir.relative_to(root)):
                continue
            for entrypoint in ("SKILL.md", "COMPOSED.md"):
                for nested in sorted(skill_dir.rglob(entrypoint)):
                    if nested.parent == skill_dir:
                        continue
                    rel = nested.relative_to(root)
                    if should_skip(rel):
                        continue
                    if is_excluded(rel, excludes) or is_build_output(rel, root):
                        continue
                    violations.append(
                        f"{rel.as_posix()}: nested {entrypoint} must not hide below "
                        f"the top-level skill dir. Move this sub-skill beside the others."
                    )
    return violations


def band(repo_root: Path | None = None) -> str:
    """Return the repo's declared band, or the tight one when it has none."""
    declared = get_str_option(HOOK_ID, "band", "", repo_root)
    return declared if declared in BAND_CAPS else UNDECLARED_BAND


def markdown_caps(repo_root: Path | None = None) -> tuple[int, int]:
    lines, chars, _ = BAND_CAPS[band(repo_root)]
    return lines, chars


def docs_cap(repo_root: Path | None = None) -> int:
    return BAND_CAPS[band(repo_root)][2]


def guide_caps(repo_root: Path | None = None) -> tuple[int, int]:
    lines, chars = markdown_caps(repo_root)
    return lines * GUIDE_SIZE_FACTOR, chars * GUIDE_SIZE_FACTOR


def check_band_declaration() -> list[str]:
    declared = get_str_option(HOOK_ID, "band", "")
    if declared in BAND_CAPS:
        return []
    names = ", ".join(sorted(BAND_CAPS))
    if not declared:
        return [
            f"no documentation band declared. Every repo declares one of "
            f"{names}, small included: band under "
            f"[tool.agentic-os.{HOOK_ID}] in pyproject.toml, or under "
            f"[{HOOK_ID}] in .agentic-os.toml."
        ]
    return [f'band = "{declared}" is not a band. Declare one of {names}.']


def check_docs_count() -> list[str]:
    """Cap how many docs/*.md a repo carries.

    Excludes do not apply. A per-doc size cap with no count cap turns every
    over-long doc into two docs, which is how a docs/ folder reaches 156 files
    with none of them over the cap.
    """
    present = _count_markdown(DOCS_DIRNAME)
    cap = docs_cap()
    if present is None or present <= cap:
        return []
    return [
        f"docs/: {present} docs exceeds the {cap}-doc cap for the "
        f"{band()} band. Merge related pages; splitting one doc into two to "
        f"clear the size cap trades one violation for another."
    ]


def _count_markdown(dirname: str) -> int | None:
    """Flat *.md in `dirname`, or None when the directory is absent."""
    root = REPO_ROOT / dirname
    if not root.is_dir():
        return None
    return sum(
        1
        for p in root.glob("*.md")
        if not should_skip(p.relative_to(REPO_ROOT))
        and not is_build_output(p.relative_to(REPO_ROOT), REPO_ROOT)
    )


def caps_for(rel: Path) -> tuple[int, int]:
    if rel.name == "AGENTS.md":
        max_lines = get_int_option(
            HOOK_ID, "agents_md_max_lines", AGENTS_DEFAULT_MAX_LINES
        )
        max_chars = get_int_option(
            HOOK_ID, "agents_md_max_chars", AGENTS_DEFAULT_MAX_CHARS
        )
        return max_lines, max_chars
    if rel.as_posix() == "README.md":
        max_lines = get_int_option(
            HOOK_ID, "readme_max_lines", README_DEFAULT_MAX_LINES
        )
        max_chars = get_int_option(
            HOOK_ID, "readme_max_chars", README_DEFAULT_MAX_CHARS
        )
        return max_lines, max_chars
    if is_guide(rel):
        return guide_caps()
    return markdown_caps()


def strip_frontmatter(text: str) -> str:
    """Drop a leading YAML frontmatter block.

    The caps bound what a reader loads, and frontmatter is machine-read
    config rather than prose, so it does not spend a doc's budget.
    """
    if not text.startswith("---\n"):
        return text
    end = text.find("\n---\n", 3)
    if end == -1:
        return text
    return text[end + len("\n---\n") :]


def _oversize_remedy(rel: Path) -> str:
    """What to do about an over-long file, by what kind of file it is.

    Telling a guide author to split into docs/*.md is the remedy that sent the
    walkthrough back to the shelf it did not fit on. A guide that overruns is
    carrying reference material, and that is the half that moves.
    """
    if is_guide(rel):
        return (
            "A guide is one journey; move the reference material it carries "
            "into docs/*.md rather than splitting the walkthrough."
        )
    return "Split large docs into smaller docs/*.md files."


def check_markdown_sizes() -> list[str]:
    violations: list[str] = []
    vendored = vendored_trees()
    size_excludes = size_excluded_trees()
    for rel in markdown_files(apply_excludes=False):
        if (
            rel.name in SIZE_CAP_EXEMPT_BASENAMES
            or is_skill_reference(rel)
            or rel == ORG_PROFILE_README
            or is_vendored(rel, vendored)
            or is_excluded(rel, size_excludes)
        ):
            continue
        path = REPO_ROOT / rel
        text = strip_frontmatter(path.read_text(encoding="utf-8", errors="replace"))
        n_lines = len(text.splitlines())
        n_chars = len(text)
        max_lines, max_chars = caps_for(rel)
        remedy = _oversize_remedy(rel)
        if n_lines > max_lines:
            violations.append(
                f"{rel.as_posix()}: {n_lines} lines exceeds the {max_lines}-line "
                f"cap. {remedy}"
            )
        if n_chars > max_chars:
            violations.append(
                f"{rel.as_posix()}: {n_chars} chars exceeds the {max_chars}-char "
                f"cap. {remedy}"
            )
    return violations


def _enabled(hook_id: str) -> bool:
    """The combined id still disables both halves.

    It stays the config namespace for band and every cap, so splitting the
    entry points churns no consumer pyproject (#1111).
    """
    return is_enabled(HOOK_ID) and is_enabled(hook_id)


def _report(hook_id: str, violations: list[str]) -> int:
    if not violations:
        print(f"{hook_id} check: OK")
        return 0
    for violation in violations:
        sys.stderr.write(f"FAIL: {violation}\n")
    sys.stderr.write(f"\n{len(violations)} {hook_id} violation(s).\n")
    return 1


def main_placement() -> int:
    if not _enabled(PLACEMENT_HOOK_ID):
        print(f"{PLACEMENT_HOOK_ID}: disabled by repo config")
        return 0
    return _report(
        PLACEMENT_HOOK_ID,
        check_docs_flatness()
        + check_guides_flatness()
        + check_markdown_locations()
        + check_skill_flatness()
        + check_exclusion_ratification("excludes"),
    )


def main_size() -> int:
    if not _enabled(SIZE_HOOK_ID):
        print(f"{SIZE_HOOK_ID}: disabled by repo config")
        return 0
    return _report(
        SIZE_HOOK_ID,
        check_band_declaration()
        + check_docs_count()
        + check_markdown_sizes()
        + check_exclusion_ratification("size_excludes"),
    )


if __name__ == "__main__":
    sys.exit(max(main_placement(), main_size()))

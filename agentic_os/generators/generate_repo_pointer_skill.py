#!/usr/bin/env python3
"""Generate a repo-pointer skill (`repo-<name>`) from a repo's GitHub metadata.

A repo-pointer skill is a thin, fully-generated SKILL.md that points an agent at
a repo's foundational trifecta (README.md / AGENTS.md / docs/FEATURES.md). It is
deliberately not hand-authored: the directory name carries the repo name, the
description is the literal GitHub description (emoji stripped, dashes normalized)
plus a `Triggers -` line built from the repo's GitHub topics, and the body is a
fixed three-bullet pointer block. The matching validator (check_repo_pointer_skills)
regenerates the file and fails on any drift, so the generator is the single source
of truth for the shape.

Keeping this module pure (it never calls the network itself) lets the caller
supply repo metadata however it likes. The canonical source is Forgejo, whose
`repo get` returns both the description and the topics array in one
payload. The CLI reads that JSON from stdin:

    aosguard ops forgejo repo get <owner> <name> \
        | python -m agentic_os.generators.generate_repo_pointer_skill <name> --from-json - --repo-root <repo>

`--from-json` also accepts the GitHub `gh repo view --json
description,repositoryTopics` shape, so non-Forgejo callers still work.

Schema and rollout: see docs/features-agents.md.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import sys
from pathlib import Path

from agentic_os.config import get_str_option

try:
    import yaml
except ModuleNotFoundError:  # pragma: no cover
    sys.stderr.write(
        "generate_repo_pointer_skill.py: PyYAML is required. "
        "Install with: pip install pyyaml\n"
    )
    sys.exit(2)

SKILL_PREFIX = "repo-"
_SKILL_COMPONENT_RE = re.compile(r"^[a-z0-9][a-z0-9-]*$")

# Strip leading badge emoji from GitHub descriptions: they carry no routing
# signal and burn description bytes. Covers pictographs, VS, and ZWJ.
_EMOJI_RE = re.compile(
    "["
    "\U0001f000-\U0001faff"  # symbols & pictographs, supplemental, extended-A
    "\U00002600-\U000027bf"  # misc symbols + dingbats
    "\U0001f1e6-\U0001f1ff"  # regional indicators
    "\U00002b00-\U00002bff"  # misc symbols & arrows
    "\U0000fe00-\U0000fe0f"  # variation selectors
    "\U0000200d"  # zero-width joiner
    "\U000024c2"
    "\U0001f900-\U0001f9ff"
    "]+"
)

# Em-dash and en-dash are banned by Kai's voice tooling and the em-dash
# pre-commit hook. Normalize to the spaced hyphen the rest of the fleet uses.
_DASH_RE = re.compile(r"\s*[—–]\s*")


def clean_description(raw: str) -> str:
    """Strip emoji, normalize dashes, collapse whitespace, drop trailing period."""
    text = _EMOJI_RE.sub("", raw or "")
    text = _DASH_RE.sub(" - ", text)
    text = re.sub(r"\s+", " ", text).strip()
    return text.rstrip(". ").strip()


def build_description(raw_description: str, name: str, topics: list[str]) -> str:
    """Compose the frontmatter description: cleaned GitHub desc + Triggers line.

    The repo name is always the first trigger (the skill routes on its own name);
    GitHub topics supply the curated aliases. Topics equal to the name are
    de-duplicated.
    """
    desc = clean_description(raw_description)
    triggers = [name]
    for topic in topics:
        topic = (topic or "").strip()
        if topic and topic not in triggers:
            triggers.append(topic)
    trigger_line = f"Triggers - {', '.join(triggers)}"
    return f"{desc}. {trigger_line}" if desc else trigger_line


DEFAULT_ORG = "coilyco-bridge"


def repo_skill_name(name: str, org: str = DEFAULT_ORG) -> str:
    """Return the canonical globally unique skill name for one repository.

    Ordinary repository names retain the compact ``repo-<name>`` contract.
    Names outside the skill-component grammar use an owner-qualified,
    content-addressed fallback. The digest prevents punctuation-normalization
    collisions, while the owner keeps identically named special repositories
    such as each organization's ``.github`` source distinct.
    """
    if _SKILL_COMPONENT_RE.fullmatch(name):
        return f"{SKILL_PREFIX}{name}"

    owner_slug = re.sub(r"[^a-z0-9]+", "-", org.lower()).strip("-") or "owner"
    repo_slug = re.sub(r"[^a-z0-9]+", "-", name.lower()).strip("-") or "repository"
    digest = hashlib.sha256(f"{org}/{name}".encode()).hexdigest()[:8]
    return f"{SKILL_PREFIX}{owner_slug}-{repo_slug}-{digest}"


def render_skill(
    name: str,
    description: str,
    org: str = DEFAULT_ORG,
) -> str:
    """Render the full SKILL.md text for `repo-<name>` from a final description.

    Pure and deterministic: the validator re-renders from the committed file's
    own frontmatter and asserts a byte-identical match, so this is the one place
    the shape is defined. `name` is the bare repo name; the `repo-` prefix is
    applied here for the directory/frontmatter name and the H1.

    `org` is the workspace org dir the repo lives under (`~/projects/<org>/`).
    Defaults to `coilyco-bridge`, the fleet's home org; a repo living elsewhere
    sets `[tool.agentic-os.repo-pointer-skills] org` so the pointer path tracks
    the real checkout. Every repo enabling the hook sets it today, so the
    default is the guess for a new repo rather than a live path. See
    scripts/sweep-precommit.py.

    Ordinary names render as `repo-<name>`. A repository name outside the
    skill grammar receives an owner-qualified, content-addressed fallback so
    repeated special names such as `.github` remain valid and globally unique.
    """
    skill_name = repo_skill_name(name, org)
    frontmatter_fields = {"name": skill_name, "description": description}
    frontmatter = yaml.safe_dump(
        frontmatter_fields,
        sort_keys=False,
        allow_unicode=True,
        width=1_000_000,
    )
    body = (
        f"# {skill_name}\n"
        "\n"
        f"Repository `{org}/{name}`. Checkout at `~/projects/{org}/{name}/` when resident.\n"
        "\n"
        "- [`README.md`](../../../README.md) - what it is, quickstart, layout.\n"
        "- [`AGENTS.md`](../../../AGENTS.md) - agent-facing context for the repo.\n"
        "- [`docs/FEATURES.md`](../../../docs/FEATURES.md) - what ships today.\n"
        "\n"
        f"Read those before answering large questions about {name}.\n"
    )
    return f"---\n{frontmatter}---\n\n{body}"


def skill_path(repo_root: Path, name: str, skills_dir: str) -> Path:
    org = get_str_option(
        "repo-pointer-skills", "org", DEFAULT_ORG, repo_root=repo_root
    )
    return repo_root / skills_dir / repo_skill_name(name, org) / "SKILL.md"


_FRONTMATTER_RE = re.compile(r"\A---\n(?P<fm>.*?\n)---\n", re.DOTALL)
# Names the repository first, because the checkout is only conditionally there.
_POINTER_RE = re.compile(
    r"^Repository `(?P<org>[^/`]+)/(?P<name>[^/`]+)`\. "
    r"Checkout at `~/projects/[^`]+/` when resident\.$",
    re.MULTILINE,
)


def parse_frontmatter(text: str) -> dict:
    m = _FRONTMATTER_RE.match(text)
    if not m:
        raise ValueError("missing or malformed YAML frontmatter")
    data = yaml.safe_load(m.group("fm")) or {}
    if not isinstance(data, dict):
        raise ValueError("frontmatter is not a YAML mapping")
    return data


def check_drift(skill_dir_name: str, text: str, org: str = DEFAULT_ORG) -> list[str]:
    """Return human-readable defects for a committed repo-pointer SKILL.md.

    Enforces the auto-generation guarantee offline (no network): the body and
    frontmatter must be byte-identical to what `render_skill` produces from the
    file's own description, and the description must already be cleaned (no
    emoji, no em/en dash) and carry a `Triggers -` line. The byte-diff catches
    hand-edits to the pointer body; the hygiene checks catch a description that
    skipped `build_description`.
    """
    if not skill_dir_name.startswith(SKILL_PREFIX):
        return [f"{skill_dir_name}: not a repo-pointer skill (no {SKILL_PREFIX!r} prefix)"]
    try:
        fm = parse_frontmatter(text)
    except ValueError as exc:
        return [f"{skill_dir_name}/SKILL.md: {exc}"]

    problems: list[str] = []
    fm_name = fm.get("name")
    if fm_name != skill_dir_name:
        problems.append(
            f"{skill_dir_name}/SKILL.md: frontmatter name {fm_name!r} does not "
            f"match directory name {skill_dir_name!r}"
        )
    description = str(fm.get("description") or "")
    if _EMOJI_RE.search(description):
        problems.append(
            f"{skill_dir_name}/SKILL.md: description contains emoji. Regenerate."
        )
    if re.search(r"[—–]", description):
        problems.append(
            f"{skill_dir_name}/SKILL.md: description contains an em/en dash. Regenerate."
        )
    if "Triggers - " not in description:
        problems.append(
            f"{skill_dir_name}/SKILL.md: description has no 'Triggers - ' line. Regenerate."
        )
    pointer = _POINTER_RE.search(text)
    if pointer is None:
        problems.append(
            f"{skill_dir_name}/SKILL.md: missing or malformed repository pointer."
        )
        return problems

    repo_name = pointer.group("name")
    pointer_org = pointer.group("org")
    expected_skill_name = repo_skill_name(repo_name, org)
    if skill_dir_name != expected_skill_name:
        problems.append(
            f"{skill_dir_name}/SKILL.md: canonical name for {org}/{repo_name} is "
            f"{expected_skill_name!r}"
        )
    if pointer_org != org:
        problems.append(
            f"{skill_dir_name}/SKILL.md: pointer owner {pointer_org!r} does not "
            f"match configured owner {org!r}"
        )

    expected = render_skill(repo_name, description, org)
    if text != expected:
        problems.append(
            f"{skill_dir_name}/SKILL.md: body or frontmatter drifted from generator "
            f"output. These skills are auto-generated; do not hand-edit. Regenerate."
        )
    return problems


def _extract_topics(payload: dict) -> list[str]:
    """Pull topics from either the REST shape (`topics`) or the gh `--json`
    shape (`repositoryTopics`)."""
    if isinstance(payload.get("topics"), list):
        return [str(t) for t in payload["topics"]]
    repo_topics = payload.get("repositoryTopics")
    if isinstance(repo_topics, list):
        out = []
        for entry in repo_topics:
            if isinstance(entry, dict):
                topic = entry.get("topic")
                if isinstance(topic, dict):
                    name = topic.get("name")
                    if name:
                        out.append(str(name))
                elif isinstance(entry.get("name"), str):
                    out.append(entry["name"])
        return out
    return []


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        prog="generate-repo-pointer-skill",
        description="Generate a repo-pointer skill from GitHub metadata.",
    )
    parser.add_argument("name", help="Bare repo name, e.g. 'website'.")
    parser.add_argument(
        "--repo-root",
        default=".",
        help="Repo root the skill is written under (default: cwd).",
    )
    parser.add_argument(
        "--skills-dir",
        default=".agents/skills",
        help="Skills directory relative to repo root (default: .agents/skills).",
    )
    src = parser.add_mutually_exclusive_group(required=True)
    src.add_argument(
        "--from-json",
        metavar="PATH",
        help="Read repo metadata JSON (description + topics) from PATH, or '-' "
        "for stdin. Accepts the gh REST view or `gh --json` shape.",
    )
    src.add_argument(
        "--description",
        help="Literal GitHub description (use with repeated --topic).",
    )
    parser.add_argument(
        "--topic",
        action="append",
        default=[],
        help="A GitHub topic. Repeatable. Used with --description.",
    )
    parser.add_argument(
        "--print",
        action="store_true",
        help="Print to stdout instead of writing the SKILL.md file.",
    )
    ns = parser.parse_args(argv if argv is not None else sys.argv[1:])

    if ns.from_json is not None:
        raw = sys.stdin.read() if ns.from_json == "-" else Path(ns.from_json).read_text()
        payload = json.loads(raw)
        description = payload.get("description") or ""
        topics = _extract_topics(payload)
    else:
        description = ns.description or ""
        topics = ns.topic

    org = get_str_option(
        "repo-pointer-skills", "org", DEFAULT_ORG, repo_root=Path(ns.repo_root)
    )
    final_description = build_description(description, ns.name, topics)
    content = render_skill(ns.name, final_description, org)

    if ns.print:
        sys.stdout.write(content)
        return 0

    out = skill_path(Path(ns.repo_root).resolve(), ns.name, ns.skills_dir)
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(content)
    print(f"wrote {out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())

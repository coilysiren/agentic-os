"""Tests for agentic_os.pre_commit.check_documentation_layout skill flatness rule.

The flatness rule targets nested sub-skills (a SKILL.md the loader can't see),
not support material that legitimately sits beside SKILL.md.
"""
from __future__ import annotations

from pathlib import Path

import agentic_os.config as config
import agentic_os.pre_commit.check_documentation_layout as docs_layout
from agentic_os.pre_commit.check_documentation_layout import (
    AGENTS_DEFAULT_MAX_CHARS,
    AGENTS_DEFAULT_MAX_LINES,
    ROOT_MARKDOWN_ALLOWLIST,
    TRIFECTA_MAX_CHARS,
    TRIFECTA_MAX_LINES,
    caps_for,
    check_markdown_sizes,
    is_skill_reference,
    check_skill_flatness,
    is_harness_override,
    validate_module_readme,
)


def write(path: Path, text: str = "x\n") -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text, encoding="utf-8")


def _point_repo_root_at(tmp_path: Path, monkeypatch) -> None:
    # Reaches both REPO_ROOT (the tree walk) and config.REPO_ROOT (the options
    # and excludes a fixture repo declares in its own pyproject.toml).
    monkeypatch.setattr(docs_layout, "REPO_ROOT", tmp_path)
    monkeypatch.setattr(config, "REPO_ROOT", tmp_path)


def test_features_rides_the_band_like_any_docs_page(tmp_path: Path, monkeypatch) -> None:
    # FEATURES.md carried its own constant that happened to equal the ordinary
    # cap, so it was a special case in name only. It now takes the band.
    _point_repo_root_at(tmp_path, monkeypatch)
    assert caps_for(Path("docs/FEATURES.md")) == docs_layout.markdown_caps()


def test_overview_caps_fall_back_to_the_shared_defaults(
    tmp_path: Path, monkeypatch
) -> None:
    # A repo that declares no keys rides the shared defaults: README.md on the
    # overview cap, AGENTS.md on its larger one.
    _point_repo_root_at(tmp_path, monkeypatch)
    assert caps_for(Path("README.md")) == (TRIFECTA_MAX_LINES, TRIFECTA_MAX_CHARS)
    assert caps_for(Path("AGENTS.md")) == (
        AGENTS_DEFAULT_MAX_LINES,
        AGENTS_DEFAULT_MAX_CHARS,
    )
    assert AGENTS_DEFAULT_MAX_LINES > TRIFECTA_MAX_LINES
    assert AGENTS_DEFAULT_MAX_CHARS > TRIFECTA_MAX_CHARS


def test_overview_caps_take_the_declared_value_in_either_direction(
    tmp_path: Path, monkeypatch
) -> None:
    # The keys replace the default outright: one repo may buy README headroom
    # and tighten AGENTS.md as back-pressure in the same declaration.
    write(
        tmp_path / "pyproject.toml",
        "[tool.agentic-os.documentation-layout]\n"
        "readme_max_lines = 400\n"
        "readme_max_chars = 30000\n"
        "agents_md_max_lines = 40\n"
        "agents_md_max_chars = 3000\n",
    )
    _point_repo_root_at(tmp_path, monkeypatch)
    assert caps_for(Path("README.md")) == (400, 30_000)
    assert caps_for(Path("AGENTS.md")) == (40, 3_000)


def test_non_trifecta_markdown_keeps_the_standard_cap() -> None:
    # Only the root README breathes; a co-located module README and ordinary
    # docs/*.md stay on the tight cap.
    assert caps_for(Path("docs/o11y.md")) == docs_layout.markdown_caps()
    assert caps_for(Path("services/x/README.md")) == docs_layout.markdown_caps()


def test_code_review_md_keeps_the_standard_cap() -> None:
    assert caps_for(Path("CODE-REVIEW.md")) == docs_layout.markdown_caps()


def test_agents_compose_md_is_an_allowed_root_file() -> None:
    # agent-compose's disjoint source is a repo-root convention; the layout
    # rule must not reject it the way it rejects one-off root Markdown.
    assert "AGENTS.COMPOSE.md" in ROOT_MARKDOWN_ALLOWLIST


def test_code_review_md_is_an_allowed_root_file() -> None:
    # CODE-REVIEW.md is a root contract doc, not a docs/ file.
    assert "CODE-REVIEW.md" in ROOT_MARKDOWN_ALLOWLIST


def test_harness_override_filenames_are_recognized() -> None:
    # AGENTS.<harness>.md overrides sit at repo root beside AGENTS.md.
    assert is_harness_override("AGENTS.codex.md")
    assert is_harness_override("AGENTS.claude.md")
    # not overrides: the uppercase disjoint source, the base, one-off docs.
    assert not is_harness_override("AGENTS.COMPOSE.md")
    assert not is_harness_override("AGENTS.md")
    assert not is_harness_override("notes.md")


def test_support_subdirs_are_allowed(tmp_path: Path) -> None:
    skill = tmp_path / ".agents" / "skills" / "my-skill"
    write(skill / "SKILL.md")
    write(skill / "scripts" / "run.sh")
    write(skill / "assets" / "logo.png")
    write(skill / "agents" / "openai.yaml")
    write(skill / "references" / "deep.md")
    assert check_skill_flatness(tmp_path) == []


def test_nested_skill_md_is_flagged(tmp_path: Path) -> None:
    skill = tmp_path / ".agents" / "skills" / "my-skill"
    write(skill / "SKILL.md")
    write(skill / "sub-skill" / "SKILL.md")
    problems = check_skill_flatness(tmp_path)
    assert len(problems) == 1
    assert "sub-skill/SKILL.md" in problems[0]
    assert "nested SKILL.md" in problems[0]


def test_top_level_skill_md_is_clean(tmp_path: Path) -> None:
    for name in ("a", "b", "c"):
        write(tmp_path / ".agents" / "skills" / name / "SKILL.md")
    assert check_skill_flatness(tmp_path) == []


def test_top_level_composed_md_is_clean(tmp_path: Path) -> None:
    write(tmp_path / ".agents" / "composed" / "my-skill" / "COMPOSED.md")
    assert check_skill_flatness(tmp_path) == []


def test_nested_composed_md_is_flagged(tmp_path: Path) -> None:
    skill = tmp_path / ".agents" / "composed" / "my-skill"
    write(skill / "COMPOSED.md")
    write(skill / "sub-skill" / "COMPOSED.md")
    problems = check_skill_flatness(tmp_path)
    assert len(problems) == 1
    assert "nested COMPOSED.md" in problems[0]


def test_nested_skill_md_can_be_excluded(tmp_path: Path) -> None:
    skill = tmp_path / ".agents" / "skills" / "my-skill"
    write(skill / "SKILL.md")
    write(skill / "vendor" / "SKILL.md")
    write(
        tmp_path / "pyproject.toml",
        '[tool.agentic-os.documentation-layout]\n'
        'excludes = [".agents/skills/my-skill/vendor/**"]\n',
    )
    assert check_skill_flatness(tmp_path) == []


# Module README.md: outpost / homestead shapes. validate_module_readme takes a
# repo-relative README path and the repo root, returning [] when valid.

def readme(text: str) -> str:
    return text


def test_valid_outpost_with_reciprocal_docs(tmp_path: Path) -> None:
    write(
        tmp_path / "ansible" / "README.md",
        "# Ansible\nConverges workstation state.\n"
        "Full runbook: [docs/ansible.md](../docs/ansible.md)\n",
    )
    write(tmp_path / "docs" / "ansible.md", "# Ansible\nSee [ansible/](../ansible/README.md).\n")
    assert validate_module_readme(Path("ansible/README.md"), tmp_path) == []


def test_outpost_without_back_link_is_flagged(tmp_path: Path) -> None:
    write(
        tmp_path / "ansible" / "README.md",
        "# Ansible\n[docs/ansible.md](../docs/ansible.md)\n",
    )
    write(tmp_path / "docs" / "ansible.md", "# Ansible\nNo link back here.\n")
    problems = validate_module_readme(Path("ansible/README.md"), tmp_path)
    assert len(problems) == 1
    assert "not reciprocal" in problems[0]


def test_outpost_pointing_at_missing_doc_is_flagged(tmp_path: Path) -> None:
    write(
        tmp_path / "ansible" / "README.md",
        "# Ansible\n[gone](../docs/ansible.md)\n",
    )
    problems = validate_module_readme(Path("ansible/README.md"), tmp_path)
    assert len(problems) == 1
    assert "does not exist" in problems[0]


def test_outpost_with_two_docs_targets_is_flagged(tmp_path: Path) -> None:
    write(
        tmp_path / "ansible" / "README.md",
        "# Ansible\n[a](../docs/a.md) and [b](../docs/b.md)\n",
    )
    write(tmp_path / "docs" / "a.md", "[x](../ansible/README.md)\n")
    write(tmp_path / "docs" / "b.md", "[x](../ansible/README.md)\n")
    problems = validate_module_readme(Path("ansible/README.md"), tmp_path)
    assert any("exactly one" in p for p in problems)


def test_outpost_pointer_line_exempt_from_char_cap(tmp_path: Path) -> None:
    # A long relative path on the pointer line must not trip the prose cap.
    deep = "deploy/some/very/deeply/nested/module"
    target = "../" * 6 + "docs/deploy-some-very-deeply-nested-module.md"
    write(
        tmp_path / deep / "README.md",
        f"# Module\n[full runbook with a long path]({target})\n",
    )
    write(
        tmp_path / "docs" / "deploy-some-very-deeply-nested-module.md",
        f"# Module\n[x](/{deep}/README.md)\n",
    )
    assert validate_module_readme(Path(f"{deep}/README.md"), tmp_path) == []


def test_valid_homestead(tmp_path: Path) -> None:
    write(
        tmp_path / "eco-server" / "README.md",
        "# Eco server\nVendored game-server tree.\nDo not edit by hand.\n",
    )
    assert validate_module_readme(Path("eco-server/README.md"), tmp_path) == []


def test_homestead_over_line_cap_is_flagged(tmp_path: Path) -> None:
    write(
        tmp_path / "mod" / "README.md",
        "# Mod\nline one\nline two\nline three\n",
    )
    problems = validate_module_readme(Path("mod/README.md"), tmp_path)
    assert any("non-blank lines" in p for p in problems)


def test_blank_lines_do_not_count_toward_line_cap(tmp_path: Path) -> None:
    write(
        tmp_path / "mod" / "README.md",
        "# Mod\n\nVendored tree.\n\nDo not edit.\n",
    )
    assert validate_module_readme(Path("mod/README.md"), tmp_path) == []


def test_homestead_prose_over_char_cap_is_flagged(tmp_path: Path) -> None:
    write(
        tmp_path / "mod" / "README.md",
        "# Mod\n" + "x" * 91 + "\n",
    )
    problems = validate_module_readme(Path("mod/README.md"), tmp_path)
    assert any("chars, max 90" in p for p in problems)


def test_readme_must_lead_with_heading(tmp_path: Path) -> None:
    write(tmp_path / "mod" / "README.md", "just text, no heading\n")
    problems = validate_module_readme(Path("mod/README.md"), tmp_path)
    assert any("heading" in p for p in problems)


def test_root_absolute_back_link_is_reciprocal(tmp_path: Path) -> None:
    # docs file may link back root-absolute (/ansible/README.md), not relative.
    write(
        tmp_path / "ansible" / "README.md",
        "# Ansible\n[runbook](/docs/ansible.md)\n",
    )
    write(tmp_path / "docs" / "ansible.md", "# Ansible\n[home](/ansible/README.md)\n")
    assert validate_module_readme(Path("ansible/README.md"), tmp_path) == []


# Generated-document exclusion checks that a wildcard reaches both REPO_ROOT
# (the tree walk) and config.REPO_ROOT (the excludes).

def _write_generated_docs(tmp_path: Path) -> None:
    # Oversized (> 80-line cap) generated docs emitted at two locations.
    big = "# Generated reference\n" + "\n".join(f"verb {i}" for i in range(120))
    for gen in ("aws", "open-webui", "forgejo"):
        write(tmp_path / "docs" / f"generated.{gen}.md", big)
        write(tmp_path / "cmd" / "generated" / f"generated.{gen}.md", big)


def test_wildcard_exclude_clears_placement_but_never_size(
    tmp_path: Path, monkeypatch
) -> None:
    _write_generated_docs(tmp_path)
    write(
        tmp_path / "pyproject.toml",
        "[tool.agentic-os.documentation-layout]\n"
        'excludes = ["generated.*.md"]\n',
    )
    _point_repo_root_at(tmp_path, monkeypatch)
    # A wildcard still silences the location rule for the cmd/ copies, because
    # where a generated file lands is a layout decision.
    assert docs_layout.check_markdown_locations() == []
    # It never reaches the size cap: an oversized generated doc is a generator
    # emitting too much, and the generator is the fix.
    assert docs_layout.check_markdown_sizes() != []


def test_generated_guardfiles_flagged_without_exclude(tmp_path: Path, monkeypatch) -> None:
    _write_generated_docs(tmp_path)
    _point_repo_root_at(tmp_path, monkeypatch)
    # Sanity check the exclude is doing the work: the cmd/ copies are mislocated
    # and every copy is oversized when nothing is excluded.
    locations = docs_layout.check_markdown_locations()
    sizes = docs_layout.check_markdown_sizes()
    assert any("cmd/generated/generated.aws.md" in v for v in locations)
    assert len(sizes) >= len(("aws", "open-webui", "forgejo")) * 2


# Size bands: a repo declares small or large and gets that band's three caps.

def test_no_declaration_is_a_violation(tmp_path: Path, monkeypatch) -> None:
    # Small is not the silent default. An undeclared repo is a repo whose band
    # nobody decided, which reads identically to a deliberate small.
    _point_repo_root_at(tmp_path, monkeypatch)
    assert docs_layout.check_band_declaration() != []
    # Caps still resolve to the tight band while the repo is red, so the size
    # checks stay meaningful rather than crashing or passing everything.
    assert docs_layout.band() == "small"
    assert docs_layout.markdown_caps() == (40, 3_000)
    assert docs_layout.docs_cap() == 20


def test_declared_small_band_is_accepted(tmp_path: Path, monkeypatch) -> None:
    write(
        tmp_path / "pyproject.toml",
        '[tool.agentic-os.documentation-layout]\nband = "small"\n',
    )
    _point_repo_root_at(tmp_path, monkeypatch)
    assert docs_layout.check_band_declaration() == []
    assert docs_layout.markdown_caps() == (40, 3_000)
    assert docs_layout.docs_cap() == 20


def test_declared_large_band_raises_all_three_caps(tmp_path: Path, monkeypatch) -> None:
    write(
        tmp_path / "pyproject.toml",
        '[tool.agentic-os.documentation-layout]\nband = "large"\n',
    )
    _point_repo_root_at(tmp_path, monkeypatch)
    assert docs_layout.markdown_caps() == (120, 8_000)
    assert docs_layout.docs_cap() == 40


def test_unknown_band_is_a_violation_not_a_silent_default(
    tmp_path: Path, monkeypatch
) -> None:
    # A typo must fail loudly. Falling back to small silently would let a repo
    # believe it declared large while being measured against small.
    write(
        tmp_path / "pyproject.toml",
        '[tool.agentic-os.documentation-layout]\nband = "medium"\n',
    )
    _point_repo_root_at(tmp_path, monkeypatch)
    assert docs_layout.check_band_declaration() != []


def test_docs_count_cap_fires_past_the_band(tmp_path: Path, monkeypatch) -> None:
    for i in range(21):
        write(tmp_path / "docs" / f"page-{i}.md", "# Page\n")
    _point_repo_root_at(tmp_path, monkeypatch)
    assert docs_layout.check_docs_count() != []
    write(
        tmp_path / "pyproject.toml",
        '[tool.agentic-os.documentation-layout]\nband = "large"\n',
    )
    assert docs_layout.check_docs_count() == []


def test_excludes_cannot_shrink_the_docs_count(tmp_path: Path, monkeypatch) -> None:
    # An exclude that hid pages from the count would make the cap a formality.
    for i in range(21):
        write(tmp_path / "docs" / f"page-{i}.md", "# Page\n")
    write(
        tmp_path / "pyproject.toml",
        "[tool.agentic-os.documentation-layout]\n"
        'excludes = ["docs/page-*.md"]\n',
    )
    _point_repo_root_at(tmp_path, monkeypatch)
    assert docs_layout.check_docs_count() != []


def test_org_profile_readme_is_not_a_module_readme(
    tmp_path: Path, monkeypatch
) -> None:
    """profile/README.md in an <org>/.github repo is the rendered org page.

    Forgejo and GitHub both publish it as the organisation front page, so its
    shape is theirs. Holding it to the 3-line module-README signage rule would
    require moving the body into docs/, which blanks the page.
    """
    write(tmp_path / "pyproject.toml",
          '[tool.agentic-os.documentation-layout]\nband = "small"\n')
    body = "# coilyco-bridge\n\n" + "a real landing page paragraph.\n" * 60
    write(tmp_path / "profile" / "README.md", body)
    # An ordinary co-located README is still held to the signage shape.
    write(tmp_path / "svc" / "README.md", body)
    _point_repo_root_at(tmp_path, monkeypatch)

    offenders = {v.split(":")[0] for v in docs_layout.check_markdown_locations()}
    assert "svc/README.md" in offenders
    assert "profile/README.md" not in offenders

    sized = {v.split(":")[0] for v in docs_layout.check_markdown_sizes()}
    assert "profile/README.md" not in sized


def test_a_vendored_tree_takes_no_size_cap(tmp_path: Path, monkeypatch) -> None:
    """A repo may declare a tree whose Markdown shape it does not own.

    A vendored SDK's docs are upstream's to shape, and store listing copy is
    shaped by the surface that renders it. Neither is prose this repo can cut,
    and cutting it forks upstream or changes what a reader outside sees.
    """
    write(
        tmp_path / "pyproject.toml",
        '[tool.agentic-os.documentation-layout]\n'
        'band = "small"\n'
        'vendored = ["unity/Assets/EcoModKit/", "mods/Mods/UserCode/"]\n',
    )
    long_body = "# Upstream\n\n" + "line\n" * 400
    write(tmp_path / "unity" / "Assets" / "EcoModKit" / "Docs" / "README.md", long_body)
    write(tmp_path / "mods" / "Mods" / "UserCode" / "AMod" / "README.md", long_body)
    # An ordinary doc of the same size is still capped, so the declaration is
    # scoped to the named trees rather than disabling the check.
    write(tmp_path / "docs" / "ordinary.md", long_body)
    _point_repo_root_at(tmp_path, monkeypatch)

    offenders = {v.split(":")[0] for v in docs_layout.check_markdown_sizes()}
    assert offenders == {"docs/ordinary.md"}


def test_a_vendored_prefix_is_not_a_basename(tmp_path: Path, monkeypatch) -> None:
    """A bare basename would exempt one filename everywhere.

    That is the per-file escape hatch the count cap removed, so `vendored`
    matches a path prefix and nothing else.
    """
    write(
        tmp_path / "pyproject.toml",
        '[tool.agentic-os.documentation-layout]\n'
        'band = "small"\nvendored = ["README.md"]\n',
    )
    long_body = "# Doc\n\n" + "line\n" * 400
    write(tmp_path / "svc" / "README.md", long_body)
    _point_repo_root_at(tmp_path, monkeypatch)

    offenders = {v.split(":")[0] for v in docs_layout.check_markdown_sizes()}
    assert "svc/README.md" in offenders


def test_a_vendored_tree_still_takes_the_placement_rules(
    tmp_path: Path, monkeypatch
) -> None:
    """`vendored` answers the size cap alone.

    Placement and flatness stay with `excludes`, so declaring a tree vendored
    cannot quietly widen where Markdown may live.
    """
    write(
        tmp_path / "pyproject.toml",
        '[tool.agentic-os.documentation-layout]\n'
        'band = "small"\nvendored = ["sdk/"]\n',
    )
    write(tmp_path / "sdk" / "guide" / "notes.md", "# Notes\n\nbody.\n")
    _point_repo_root_at(tmp_path, monkeypatch)

    offenders = {v.split(":")[0] for v in docs_layout.check_markdown_locations()}
    assert "sdk/guide/notes.md" in offenders


def test_skill_entrypoints_take_no_size_cap_from_this_hook(
    tmp_path: Path, monkeypatch
) -> None:
    """check-skills owns SKILL.md and COMPOSED.md, and allows far more than a band.

    Both hooks ship in the same suite, so a shared cap here made a skill pass the
    validator that owns skills and fail the one that owns layout, with an error
    telling the author to split it into docs/. A skill overflows into its own
    references/ instead.
    """
    write(tmp_path / "pyproject.toml",
          '[tool.agentic-os.documentation-layout]\nband = "small"\n')
    long_body = "# Skill\n\n" + "line\n" * 400
    write(tmp_path / ".agents" / "skills" / "a-skill" / "SKILL.md", long_body)
    write(tmp_path / ".agents" / "composed" / "b-source" / "COMPOSED.md", long_body)
    # An ordinary doc of the same size is still capped, so the exemption is
    # scoped to the entrypoints rather than disabling the check.
    write(tmp_path / "docs" / "ordinary.md", long_body)
    _point_repo_root_at(tmp_path, monkeypatch)

    violations = docs_layout.check_markdown_sizes()
    offenders = {v.split(":")[0] for v in violations}
    assert "docs/ordinary.md" in offenders
    assert not [v for v in offenders if v.endswith(("SKILL.md", "COMPOSED.md"))]


# `size_excludes`: the honest key for docs a repo owns but sizes differently,
# distinct from `excludes` (placement only) and `vendored` (not ours).


def test_size_excludes_defaults_to_empty(tmp_path: Path) -> None:
    assert docs_layout.size_excluded_trees(tmp_path) == []


def test_size_excludes_reads_the_hook_section(tmp_path: Path) -> None:
    (tmp_path / "pyproject.toml").write_text(
        "[tool.agentic-os.documentation-layout]\n"
        'size_excludes = ["services/**", "charts/**"]\n',
        encoding="utf-8",
    )
    assert docs_layout.size_excluded_trees(tmp_path) == ["services/**", "charts/**"]


def test_size_excludes_matches_a_nested_doc() -> None:
    from agentic_os.config import is_excluded

    assert is_excluded(Path("services/a/docs/b.md"), ["services/**"]) is True
    assert is_excluded(Path("docs/b.md"), ["services/**"]) is False


def test_the_line_cap_counts_blank_lines_too(tmp_path: Path, monkeypatch) -> None:
    # The caps reference called these "non-blank lines" while the validator
    # measured every line: 73 against an actual 122 on one real file.
    _point_repo_root_at(tmp_path, monkeypatch)
    write(tmp_path / "pyproject.toml", '[tool.agentic-os.documentation-layout]\nband = "small"\n')
    body = "# Title\n" + "\n" * 60 + "one sentence.\n"
    write(tmp_path / "docs" / "airy.md", body)

    assert len([ln for ln in body.splitlines() if ln.strip()]) == 2
    problems = [v for v in check_markdown_sizes() if "airy.md" in v and "lines" in v]

    assert problems, "a 62-line file passed a 40-line cap, so blanks went uncounted"
    assert "62 lines exceeds the 40-line cap" in problems[0]


def test_a_skill_reference_file_takes_no_size_cap(tmp_path: Path, monkeypatch) -> None:
    # check-skills answers an over-long SKILL.md with "move detail into a
    # sibling references/ file" and caps nothing there. See #1110.
    _point_repo_root_at(tmp_path, monkeypatch)
    write(tmp_path / "pyproject.toml", '[tool.agentic-os.documentation-layout]\nband = "small"\n')
    ref = tmp_path / ".agents" / "skills" / "my-skill" / "references" / "deep.md"
    write(ref, "# Deep\n" + ("detail. " * 900))

    assert not [v for v in check_markdown_sizes() if "deep.md" in v]


def test_a_skill_page_outside_references_still_takes_the_cap(
    tmp_path: Path, monkeypatch
) -> None:
    # The exemption is the remedy check-skills names, not the whole skill tree.
    _point_repo_root_at(tmp_path, monkeypatch)
    write(tmp_path / "pyproject.toml", '[tool.agentic-os.documentation-layout]\nband = "small"\n')
    write(tmp_path / ".agents" / "skills" / "my-skill" / "notes.md", "# Notes\n" + ("x " * 3000))

    assert [v for v in check_markdown_sizes() if "notes.md" in v]


def test_references_outside_a_skill_tree_are_not_exempt() -> None:
    assert is_skill_reference(Path(".agents/skills/s/references/a.md"))
    assert is_skill_reference(Path(".claude/skills/s/references/a.md"))
    assert not is_skill_reference(Path("docs/references/a.md"))
    # The directory, never a file that merely happens to be named references.md.
    assert not is_skill_reference(Path(".agents/skills/s/references.md"))


def test_size_switches_off_without_taking_placement_with_it(
    tmp_path: Path, monkeypatch
) -> None:
    # A site repo drops the caps its blog posts cannot meet and keeps the
    # placement rules it still wants (#1111).
    _point_repo_root_at(tmp_path, monkeypatch)
    write(tmp_path / "src/pages/posts/blog.md", "# post\n" + "line\n" * 200)
    write(
        tmp_path / "pyproject.toml",
        '[tool.agentic-os.documentation-layout]\nband = "small"\n'
        "\n[tool.agentic-os.documentation-size]\nenabled = false\n",
    )
    assert docs_layout.main_size() == 0
    assert docs_layout.main_placement() == 1


def test_the_combined_id_still_disables_both_halves(
    tmp_path: Path, monkeypatch
) -> None:
    _point_repo_root_at(tmp_path, monkeypatch)
    write(tmp_path / "src/pages/posts/blog.md", "# post\n" + "line\n" * 200)
    write(
        tmp_path / "pyproject.toml",
        '[tool.agentic-os.documentation-layout]\nband = "small"\nenabled = false\n',
    )
    assert docs_layout.main_placement() == 0
    assert docs_layout.main_size() == 0


# guides/: the narrative shelf, separate from the docs/ reference shelf.

def _large_band(tmp_path: Path) -> None:
    write(
        tmp_path / "pyproject.toml",
        '[tool.agentic-os.documentation-layout]\nband = "large"\n',
    )


def test_a_guide_is_an_allowed_location(tmp_path: Path, monkeypatch) -> None:
    _large_band(tmp_path)
    write(tmp_path / "guides" / "role-divergence.md", "# Guide\n")
    _point_repo_root_at(tmp_path, monkeypatch)
    assert docs_layout.check_markdown_locations() == []
    assert docs_layout.check_guides_flatness() == []


def test_guides_must_stay_flat_like_docs(tmp_path: Path, monkeypatch) -> None:
    # A nested guide is invisible the same way a nested doc is, and the
    # remedy is the same: a filename prefix, not a subdirectory.
    _large_band(tmp_path)
    write(tmp_path / "guides" / "compose" / "walkthrough.md", "# Guide\n")
    _point_repo_root_at(tmp_path, monkeypatch)
    assert docs_layout.check_guides_flatness() != []
    assert docs_layout.check_markdown_locations() != []


def test_a_guide_takes_twice_its_band_per_doc_caps(
    tmp_path: Path, monkeypatch
) -> None:
    # Derived from the band rather than hand-set, so the two shelves cannot
    # drift apart when a band moves.
    _point_repo_root_at(tmp_path, monkeypatch)
    write(
        tmp_path / "pyproject.toml",
        '[tool.agentic-os.documentation-layout]\nband = "small"\n',
    )
    assert docs_layout.guide_caps() == (80, 6_000)
    assert docs_layout.guides_cap() == 3
    _large_band(tmp_path)
    assert docs_layout.guide_caps() == (240, 16_000)
    assert docs_layout.guides_cap() == 6
    assert docs_layout.guide_caps() > docs_layout.markdown_caps()


def test_caps_for_routes_a_guide_off_the_docs_cap(
    tmp_path: Path, monkeypatch
) -> None:
    _large_band(tmp_path)
    _point_repo_root_at(tmp_path, monkeypatch)
    assert caps_for(Path("guides/walkthrough.md")) == docs_layout.guide_caps()
    assert caps_for(Path("docs/reference.md")) == docs_layout.markdown_caps()
    # Only the flat shelf. A nested path is a placement violation, and giving
    # it the roomier cap would reward the shape the placement rule rejects.
    assert caps_for(Path("guides/sub/deep.md")) == docs_layout.markdown_caps()


def test_a_guide_still_has_a_ceiling(tmp_path: Path, monkeypatch) -> None:
    _large_band(tmp_path)
    write(tmp_path / "guides" / "long.md", "# Guide\n" + "line\n" * 300)
    _point_repo_root_at(tmp_path, monkeypatch)
    offenders = {v.split(":")[0] for v in docs_layout.check_markdown_sizes()}
    assert "guides/long.md" in offenders


def test_a_repo_at_its_docs_cap_can_still_add_a_guide(
    tmp_path: Path, monkeypatch
) -> None:
    # The case that forced the type: the reference shelf is full and the page
    # is not that kind of page (teable:coilyco-flight-deck/agentic-os#7077).
    _large_band(tmp_path)
    for i in range(40):
        write(tmp_path / "docs" / f"page-{i}.md", "# Page\n")
    write(tmp_path / "guides" / "walkthrough.md", "# Guide\n")
    _point_repo_root_at(tmp_path, monkeypatch)
    assert docs_layout.check_docs_count() == []
    assert docs_layout.check_guides_count() == []
    assert docs_layout.check_markdown_locations() == []


def test_guides_carry_their_own_scarce_count_cap(
    tmp_path: Path, monkeypatch
) -> None:
    _large_band(tmp_path)
    for i in range(7):
        write(tmp_path / "guides" / f"guide-{i}.md", "# Guide\n")
    _point_repo_root_at(tmp_path, monkeypatch)
    assert docs_layout.check_guides_count() != []
    # A full guide shelf never spends the docs budget, in either direction.
    assert docs_layout.check_docs_count() == []


def test_a_repo_with_no_guides_is_untouched(tmp_path: Path, monkeypatch) -> None:
    # Opt-in by directory: no config key, and no existing repo needs a change.
    _large_band(tmp_path)
    write(tmp_path / "docs" / "reference.md", "# Page\n")
    _point_repo_root_at(tmp_path, monkeypatch)
    assert docs_layout.check_guides_count() == []
    assert docs_layout.check_guides_flatness() == []
    assert docs_layout.main_placement() == 0
    assert docs_layout.main_size() == 0


def test_an_oversize_guide_is_not_told_to_split_into_docs(
    tmp_path: Path, monkeypatch
) -> None:
    # The remedy that sent the walkthrough back to the shelf it did not fit on
    # must not be the one the hook prints at a guide author.
    _large_band(tmp_path)
    write(tmp_path / "guides" / "long.md", "# Guide\n" + "line\n" * 300)
    write(tmp_path / "docs" / "long.md", "# Page\n" + "line\n" * 300)
    _point_repo_root_at(tmp_path, monkeypatch)
    said = {v.split(":")[0]: v for v in docs_layout.check_markdown_sizes()}
    assert "splitting the walkthrough" in said["guides/long.md"]
    assert "Split large docs" in said["docs/long.md"]

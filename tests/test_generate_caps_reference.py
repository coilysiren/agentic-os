"""Tests for agentic_os.generators.generate_caps_reference: render, generate, drift."""
from __future__ import annotations

from pathlib import Path

from agentic_os.generators import generate_caps_reference as caps
from agentic_os.pre_commit import check_code_comments as cc
from agentic_os.pre_commit import check_documentation_layout as dl


def test_render_reflects_live_constants() -> None:
    doc = caps.render_doc()
    # Every cap value comes straight from the validator constants, so the
    # render must contain each current value. This is the anti-drift property.
    for value in (
        cc.MAX_COMMENT_LINE_CHARS,
        cc.MAX_CONTIGUOUS_COMMENT_LINES,
        *dl.BAND_CAPS["small"],
        *dl.BAND_CAPS["large"],
        dl.BAND_CAPS["small"][0] * dl.GUIDE_SIZE_FACTOR,
        dl.BAND_CAPS["large"][1] * dl.GUIDE_SIZE_FACTOR,
        *dl.GUIDE_BAND_COUNTS.values(),
        dl.TRIFECTA_MAX_LINES,
        dl.TRIFECTA_MAX_CHARS,
        dl.AGENTS_DEFAULT_MAX_LINES,
        dl.AGENTS_DEFAULT_MAX_CHARS,
        dl.README_DEFAULT_MAX_LINES,
        dl.README_DEFAULT_MAX_CHARS,
        dl.README_MAX_LINES,
        dl.README_MAX_PROSE_CHARS,
    ):
        assert str(value) in doc


def test_render_is_deterministic() -> None:
    assert caps.render_doc() == caps.render_doc()


def test_generate_then_check_drift_passes(tmp_path: Path) -> None:
    doc_path = tmp_path / "catalog-caps-reference.md"
    assert caps.generate(doc_path) == 0
    assert caps.check_drift(doc_path) == 0


def test_check_drift_fails_when_stale(tmp_path: Path) -> None:
    doc_path = tmp_path / "catalog-caps-reference.md"
    doc_path.write_text("# stale\n", encoding="utf-8")
    assert caps.check_drift(doc_path) == 1


def test_check_drift_fails_when_missing(tmp_path: Path) -> None:
    assert caps.check_drift(tmp_path / "absent.md") == 1


def test_committed_render_is_in_sync() -> None:
    # The shipped docs/catalog-caps-reference.md must match a fresh render, the
    # same invariant the caps-reference-drift pre-commit hook enforces.
    assert caps.check_drift() == 0

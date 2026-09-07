"""Tests for agentic_os.link_liveness: report-only outbound URL probing."""
from __future__ import annotations

import urllib.error
from pathlib import Path

import pytest

from agentic_os import config
from agentic_os import link_liveness as ll
from agentic_os.pre_commit import check_outbound_links as col


def _write(root: Path, rel: str, body: str) -> None:
    path = root / rel
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(body, encoding="utf-8")


def _http_error(code: int) -> urllib.error.HTTPError:
    return urllib.error.HTTPError("https://x.test/", code, "boom", {}, None)


def _run(monkeypatch: pytest.MonkeyPatch, root: Path, verdicts: dict) -> int:
    monkeypatch.setattr(col, "REPO_ROOT", root)
    monkeypatch.setattr(ll, "REPO_ROOT", root)
    monkeypatch.setattr(config, "REPO_ROOT", root)
    config.reset_build_output_cache()
    monkeypatch.setattr(ll, "probe", lambda url, timeout=0: verdicts[url])
    return ll.main(["check-link-liveness"])


def test_a_reachable_url_passes(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    _write(tmp_path, "README.md", "[a](https://a.test/)\n")
    assert _run(monkeypatch, tmp_path, {"https://a.test/": ("ok", "200")}) == 0


def test_a_dead_url_names_every_place_that_carries_it(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]
) -> None:
    _write(tmp_path, "README.md", "[a](https://a.test/)\n")
    _write(tmp_path, "docs/b.md", "[a](https://a.test/)\n")
    assert _run(monkeypatch, tmp_path, {"https://a.test/": ("dead", "HTTP 404")}) == 1
    err = capsys.readouterr().err.replace("\\", "/")
    assert "README.md:1" in err
    assert "docs/b.md:1" in err


def test_rate_limiting_and_gated_reads_are_tolerated_statuses() -> None:
    assert {401, 403, 405, 429} <= ll.TOLERATED_STATUS


def test_probe_classifies_by_status(monkeypatch: pytest.MonkeyPatch) -> None:
    def raise_code(code: int):
        def opener(request, timeout=0, **_):
            raise _http_error(code)

        return opener

    monkeypatch.setattr(ll.urllib.request, "urlopen", raise_code(404))
    assert ll.probe("https://x.test/")[0] == "dead"
    monkeypatch.setattr(ll.urllib.request, "urlopen", raise_code(429))
    assert ll.probe("https://x.test/")[0] == "tolerated"


def test_transport_failure_is_tolerated_not_dead(monkeypatch: pytest.MonkeyPatch) -> None:
    def refuse(request, timeout=0, **_):
        raise urllib.error.URLError("connection refused")

    monkeypatch.setattr(ll.urllib.request, "urlopen", refuse)
    assert ll.probe("https://x.test/")[0] == "tolerated"


def test_fenced_urls_are_not_probed(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    _write(tmp_path, "README.md", "```\nhttps://never.test/\n```\n")
    assert _run(monkeypatch, tmp_path, {}) == 0

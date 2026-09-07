"""Trust-store repair in agentic_os/__init__.py, and never weakening it.

The defect this guards is silent in the wrong direction. An interpreter with
an empty CA store fails every HTTPS call with text naming a certificate, so
the reader chases the endpoint or the token while the cause is local.
"""
from __future__ import annotations

import ssl
from pathlib import Path

import pytest

import agentic_os as tls


def _pem_with_no_certs(tmp_path: Path) -> str:
    path = tmp_path / "empty.pem"
    path.write_text("# no certificates here\n", encoding="utf-8")
    return str(path)


def test_a_populated_interpreter_is_left_alone(monkeypatch) -> None:
    # The fallback must not fire on a healthy host, or it silently swaps the
    # platform trust store for whichever file happens to sit on disk first.
    sentinel = ssl.create_default_context()
    monkeypatch.setattr(
        sentinel, "get_ca_certs", lambda binary_form=False: [{"subject": ()}]
    )
    monkeypatch.setattr(ssl, "create_default_context", lambda *a, **k: sentinel)
    assert tls.build_ssl_context(candidates=("/nonexistent/cert.pem",)) is sentinel


def test_an_empty_interpreter_is_repaired_from_disk() -> None:
    # The real repair, against the real bundle this host carries. Skipped
    # rather than faked when no candidate exists, so it never passes vacuously.
    bundle = tls.found_ca_bundle()
    if bundle is None:
        pytest.skip("no system CA bundle on this host")
    context = tls.build_ssl_context()
    assert context.get_ca_certs(), f"{bundle} loaded no certificates"


def test_verification_is_never_disabled_to_get_past_the_failure() -> None:
    # The tempting wrong fix. An unrepairable context must still fail closed.
    context = tls.build_ssl_context(candidates=("/nonexistent/cert.pem",))
    assert context.verify_mode == ssl.CERT_REQUIRED
    assert context.check_hostname is True


def test_an_unusable_candidate_is_skipped_not_fatal(tmp_path: Path) -> None:
    # A file that exists but holds no certificate must not end the search or
    # raise; the next candidate is the one that saves the call.
    empty = _pem_with_no_certs(tmp_path)
    real = tls.found_ca_bundle()
    if real is None:
        pytest.skip("no system CA bundle on this host")
    context = tls.build_ssl_context(candidates=(empty, real))
    assert context.get_ca_certs()


def test_the_diagnosis_names_the_local_cause_only_when_trust_is_broken(
    tmp_path: Path,
) -> None:
    broken = tls.build_ssl_context(candidates=(_pem_with_no_certs(tmp_path),))
    message = tls.trust_diagnosis(broken)
    assert message is not None
    assert "empty CA trust store" in message
    assert "not a bad endpoint or credential" in message

    bundle = tls.found_ca_bundle()
    if bundle is None:
        pytest.skip("no system CA bundle on this host")
    assert tls.trust_diagnosis(tls.build_ssl_context()) is None


def test_the_shared_context_is_built_once() -> None:
    tls.shared_ssl_context.cache_clear()
    assert tls.shared_ssl_context() is tls.shared_ssl_context()

"""Cross-repo pre-commit hooks and operating utilities for coilysiren/* repos.

See pyproject.toml [project.scripts] for the entry-point names. Hook
declarations live in .pre-commit-hooks.yaml at the repo root.

This module also carries the TLS trust-store fallback, because it is the one
file the aosguard guardfile bundle always ships. A bundled module runs with no
package around it, so a helper in any sibling would be unreachable there, and
scripts/guardfile-python-modules.sh derives the bundle from what guardfiles
exec rather than from imports. Living here is what lets one copy serve bundled
modules, ordinary package code and scripts/ alike.

A python.org framework build carries no CA bundle until its bundled
`Install Certificates.command` is run, and a uv venv built on top of one
inherits the emptiness, so `ssl.create_default_context()` verifies against
nothing and every HTTPS call fails closed with CERTIFICATE_VERIFY_FAILED. The
text names a certificate, so it reads as a wrong endpoint or an expired
credential while the cause is entirely local.

Measured on kais-macbook-pro 2026-09-07: `/usr/local/bin/python3` and this
repo's own `.venv` both report `cafile=None` and load 0 CA certificates, while
`/etc/ssl/cert.pem` sits readable beside them carrying 128. Survey, and the two
properties to preserve when editing this:
.agents/skills/tooling-aosguard/references/tls-trust-store.md
"""
from __future__ import annotations

import functools
import ssl
import sys
from pathlib import Path

# macOS, Debian/Ubuntu, RHEL/Fedora, SUSE/Alpine, then the two Homebrew roots.
# Order is most-specific-first; the first readable bundle wins.
CA_BUNDLE_CANDIDATES = (
    "/etc/ssl/cert.pem",
    "/etc/ssl/certs/ca-certificates.crt",
    "/etc/pki/tls/certs/ca-bundle.crt",
    "/etc/ssl/ca-bundle.pem",
    "/opt/homebrew/etc/ca-certificates/cert.pem",
    "/usr/local/etc/ca-certificates/cert.pem",
)


def build_ssl_context(
    candidates: tuple[str, ...] = CA_BUNDLE_CANDIDATES,
) -> ssl.SSLContext:
    """A verifying context, repaired from disk when the interpreter has none.

    Verification is never weakened: an unrepairable context is returned as-is
    so the call still fails closed, and `trust_diagnosis` explains why.
    """
    context = ssl.create_default_context()
    if context.get_ca_certs():
        return context
    for candidate in candidates:
        try:
            context.load_verify_locations(cafile=candidate)
        except OSError:
            continue
        if context.get_ca_certs():
            return context
    return context


@functools.lru_cache(maxsize=1)
def shared_ssl_context() -> ssl.SSLContext:
    """Process-wide context, since loading a bundle costs real work."""
    return build_ssl_context()


def trust_diagnosis(context: ssl.SSLContext | None = None) -> str | None:
    """Why a TLS failure is local, or None when the trust store is fine.

    Call sites append this to their own error text, so a certificate failure
    stops reading as a bad endpoint or a stale token.
    """
    context = shared_ssl_context() if context is None else context
    if context.get_ca_certs():
        return None
    return (
        f"{sys.executable} has an empty CA trust store and no system bundle "
        f"was found at any of {len(CA_BUNDLE_CANDIDATES)} known paths. This is "
        f"a local trust problem, not a bad endpoint or credential. Run the "
        f"python.org 'Install Certificates.command' for this interpreter, or "
        f"point SSL_CERT_FILE at a bundle."
    )


def found_ca_bundle(
    candidates: tuple[str, ...] = CA_BUNDLE_CANDIDATES,
) -> str | None:
    """The bundle the fallback would load, for diagnostics and tests."""
    for candidate in candidates:
        if Path(candidate).is_file():
            return candidate
    return None

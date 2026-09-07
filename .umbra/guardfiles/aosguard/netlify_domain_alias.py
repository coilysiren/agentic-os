"""Read and extend a Netlify site's domain aliases without dropping any.

The API replaces `domain_aliases` wholesale on update, and the caller knows
only what it wants to add, so every write here is a read-modify-write of the
full list. See the tooling-aosguard skill.
"""

from __future__ import annotations

import argparse
import json
import os
import ssl
import sys
import urllib.error
import urllib.request

API = "https://api.netlify.com/api/v1"
TIMEOUT = 30
# Mirrors the fallback in agentic_os: an isolated `python3 -I` has no package
# to import. See tooling-aosguard references/tls-trust-store.md.
CA_BUNDLE_CANDIDATES = (
    "/etc/ssl/cert.pem",
    "/etc/ssl/certs/ca-certificates.crt",
    "/etc/pki/tls/certs/ca-bundle.crt",
    "/etc/ssl/ca-bundle.pem",
    "/opt/homebrew/etc/ca-certificates/cert.pem",
    "/usr/local/etc/ca-certificates/cert.pem",
)


def _ssl_context() -> ssl.SSLContext:
    """A verifying context, repaired from disk when the interpreter has none.

    Never weakened: an unrepairable context is returned as-is so the call
    still fails closed, and the handler below names the local cause.
    """
    context = ssl.create_default_context()
    if context.get_ca_certs():
        return context
    for candidate in CA_BUNDLE_CANDIDATES:
        try:
            context.load_verify_locations(cafile=candidate)
        except OSError:
            continue
        if context.get_ca_certs():
            return context
    return context


def _request(method: str, path: str, token: str, payload: dict | None = None) -> dict:
    body = None if payload is None else json.dumps(payload).encode()
    request = urllib.request.Request(
        f"{API}{path}",
        method=method,
        data=body,
        headers={
            "Authorization": f"Bearer {token}",
            "Content-Type": "application/json",
            "User-Agent": "aosguard-netlify",
        },
    )
    with urllib.request.urlopen(
        request, timeout=TIMEOUT, context=_ssl_context()
    ) as response:
        return json.load(response)


def _token() -> str:
    token = os.environ.get("NETLIFY_AUTH_TOKEN", "").strip()
    if not token:
        raise SystemExit("netlify: NETLIFY_AUTH_TOKEN is empty (fail-closed)")
    return token


def _render(site: dict) -> None:
    print(f"site={site.get('name')}")
    print(f"custom_domain={site.get('custom_domain')}")
    for alias in site.get("domain_aliases") or []:
        print(f"alias={alias}")


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="netlify")
    parser.add_argument("action", choices=("show", "add"))
    parser.add_argument("--site", required=True)
    parser.add_argument("--alias", action="append", default=[])
    parser.add_argument("--remove", action="append", default=[])
    args = parser.parse_args(argv)

    token = _token()
    site = _request("GET", f"/sites/{args.site}", token)
    if args.action == "show":
        _render(site)
        return 0

    if not args.alias and not args.remove:
        raise SystemExit("netlify: add needs at least one --alias or --remove (fail-closed)")
    both = set(args.alias) & set(args.remove)
    if both:
        raise SystemExit(f"netlify: {sorted(both)[0]} is both added and removed (fail-closed)")

    current = list(site.get("domain_aliases") or [])
    primary = {site.get("custom_domain"), site.get("name")}
    merged = list(current)
    for alias in args.alias:
        if alias in primary:
            raise SystemExit(f"netlify: {alias} is the site's primary domain (fail-closed)")
        if alias not in merged:
            merged.append(alias)
    for alias in args.remove:
        if alias in primary:
            raise SystemExit(f"netlify: {alias} is the site's primary domain (fail-closed)")
        if alias not in merged:
            # A silent success here would hide a mistyped domain, which is the
            # error most likely to go unnoticed on this surface.
            raise SystemExit(f"netlify: {alias} is not an alias of this site (fail-closed)")
        merged.remove(alias)
    if merged == current:
        print("no change; every alias is already as asked")
        _render(site)
        return 0

    # One update for every change: each write re-issues the certificate covering
    # the primary domain too, so a rename is one call rather than two.
    print(f"{len(current)} alias(es) -> {len(merged)}")
    updated = _request("PATCH", f"/sites/{args.site}", token, {"domain_aliases": merged})
    _render(updated)
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except urllib.error.HTTPError as error:
        print(f"netlify: {error.code} {error.reason}", file=sys.stderr)
        raise SystemExit(1) from error
    except urllib.error.URLError as error:
        # A traceback here reads as a script bug rather than an unreachable API.
        print(f"netlify: cannot reach {API}: {error.reason}", file=sys.stderr)
        if not _ssl_context().get_ca_certs():
            print(
                f"netlify: {sys.executable} has an empty CA trust store and no "
                f"system bundle was found. This is a local trust problem, not "
                f"a bad endpoint or credential. Run the python.org 'Install "
                f"Certificates.command' for this interpreter, or point "
                f"SSL_CERT_FILE at a bundle.",
                file=sys.stderr,
            )
        raise SystemExit(1) from error

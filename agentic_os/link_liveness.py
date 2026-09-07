#!/usr/bin/env python3
"""
Outbound link liveness. Network-dependent, so never a commit hook.

`outbound-link-hygiene` is the offline half and runs at commit time. Actually
fetching every outbound URL is slow, flaky, and needs the network, so this half
ships as a CLI for a scheduled job to invoke and is deliberately absent from
`.pre-commit-hooks.yaml`, alongside the other authored-but-not-hooked
validators listed there.

Three rules the job depends on:

* report only non-2xx and non-3xx, so a redirect is a pass rather than a diff
* tolerate rate limiting, so 429 and a transport error are noted and not failed
* report, never edit. A link checker that opens pull requests against prose is
  worse than one that files a report.

Reuses the offline extractor, so both halves see the same set of links.
Exits 0 when nothing is dead, 1 with a report on stderr otherwise.
"""

from __future__ import annotations

import argparse
import sys
import urllib.error
import urllib.request
from pathlib import Path
from urllib.parse import urlsplit

from agentic_os import shared_ssl_context
from agentic_os.config import load_excludes
from agentic_os.pre_commit.check_dead_links import strip_fenced_code
from agentic_os.pre_commit.check_outbound_links import (
    HOOK_ID,
    REPO_ROOT,
    extract_references,
    iter_files,
)

USER_AGENT = "aos-link-liveness (+https://forgejo.coilysiren.me/coilyco-flight-deck/agentic-os)"
TOLERATED_STATUS = {401, 403, 405, 429}
DEFAULT_TIMEOUT = 15


def collect_urls(roots: list[Path], excludes: list[str]) -> dict[str, list[str]]:
    """Every http(s) URL in the tree, mapped to the places that carry it."""
    found: dict[str, list[str]] = {}
    for path in iter_files(roots, excludes):
        try:
            rel = str(path.relative_to(REPO_ROOT))
        except ValueError:
            rel = str(path)
        text = strip_fenced_code(path.read_text(errors="replace"))
        for ref in extract_references(text):
            url = ref.url.strip()
            if urlsplit(url).scheme in {"http", "https"}:
                found.setdefault(url, []).append(f"{rel}:{ref.line}")
    return found


def probe(url: str, timeout: int = DEFAULT_TIMEOUT) -> tuple[str, str]:
    """Classify one URL as ok, tolerated, or dead, with a human reason."""
    request = urllib.request.Request(url, method="GET", headers={"User-Agent": USER_AGENT})
    try:
        with urllib.request.urlopen(
            request, timeout=timeout, context=shared_ssl_context()
        ) as response:
            return "ok", str(response.status)
    except urllib.error.HTTPError as exc:
        if exc.code in TOLERATED_STATUS:
            return "tolerated", f"HTTP {exc.code}"
        return "dead", f"HTTP {exc.code}"
    except (urllib.error.URLError, OSError, ValueError) as exc:
        return "tolerated", f"unreachable: {exc}"


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        prog="check-link-liveness",
        description="Fetch every outbound URL and report the dead ones.",
    )
    parser.add_argument("paths", nargs="*", help="Optional paths to scope the scan to.")
    parser.add_argument("--timeout", type=int, default=DEFAULT_TIMEOUT)
    ns = parser.parse_args((sys.argv if argv is None else argv)[1:])

    roots = [Path(p).resolve() for p in ns.paths] if ns.paths else [REPO_ROOT]
    urls = collect_urls(roots, load_excludes(HOOK_ID))
    dead: list[str] = []
    tolerated = 0
    for url in sorted(urls):
        verdict, reason = probe(url, ns.timeout)
        if verdict == "dead":
            dead.append(f"{url} - {reason} - carried by {', '.join(urls[url])}")
        elif verdict == "tolerated":
            tolerated += 1

    print(f"link liveness: {len(urls)} url(s), {len(dead)} dead, {tolerated} tolerated")
    if not dead:
        return 0
    for entry in dead:
        sys.stderr.write(f"DEAD: {entry}\n")
    sys.stderr.write(f"\n{len(dead)} dead link(s). Report only - fix them by hand.\n")
    return 1


if __name__ == "__main__":
    sys.exit(main())

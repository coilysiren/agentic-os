#!/usr/bin/env python3
"""Resolve the immutable tag for a product's promoted release branch."""
from __future__ import annotations

import argparse
import json
import re
import urllib.request
from collections.abc import Callable, Sequence
from dataclasses import dataclass

from agentic_os import shared_ssl_context


FORGEJO_API = "https://forgejo.coilysiren.me/api/v1"
_TIMEOUT = 30


@dataclass(frozen=True)
class Product:
    repository: str
    tag_pattern: re.Pattern[str]


_GUARD = Product(
    repository="umbra",
    tag_pattern=re.compile(r"^v(\d+\.\d+\.\d+)$"),
)
_AOS = Product(
    repository="agentic-os",
    tag_pattern=re.compile(r"^aos-v(\d+\.\d+\.\d+)$"),
)
PRODUCTS = {
    "guard": _GUARD,
    "umbra": _GUARD,
    "cli-guard": _GUARD,  # retained alias
    "specgen": _GUARD,  # retained alias: the driver's name until umbra v0.192.0
    "aos": _AOS,
    "agentic-os": _AOS,
}


def _get_json(url: str) -> object:
    request = urllib.request.Request(
        url,
        headers={"User-Agent": "aos-prod-install-ref", "Accept": "application/json"},
    )
    with urllib.request.urlopen(
        request, timeout=_TIMEOUT, context=shared_ssl_context()
    ) as response:
        return json.load(response)


def _semver(value: str) -> tuple[int, int, int]:
    major, minor, patch = value.split(".")
    return int(major), int(minor), int(patch)


def resolve_release_ref(
    product_name: str,
    *,
    fetch_json: Callable[[str], object] | None = None,
) -> str:
    """Return the version tag at `release`, or the branch name as a safe fallback."""
    product = PRODUCTS[product_name]
    fetch = fetch_json or _get_json
    base = f"{FORGEJO_API}/repos/coilyco-flight-deck/{product.repository}"
    try:
        branch = fetch(f"{base}/branches/release")
        release_sha = branch["commit"]["id"]  # type: ignore[index]
        tags = fetch(f"{base}/tags?limit=50&page=1")
    except (KeyError, TypeError, OSError, ValueError):
        return "release"

    matches: list[tuple[tuple[int, int, int], str]] = []
    for tag in tags if isinstance(tags, list) else []:
        if not isinstance(tag, dict):
            continue
        commit = tag.get("commit")
        if not isinstance(commit, dict) or commit.get("sha") != release_sha:
            continue
        name = tag.get("name")
        if not isinstance(name, str):
            continue
        match = product.tag_pattern.fullmatch(name)
        if match is not None:
            matches.append((_semver(match.group(1)), name))
    return max(matches)[1] if matches else "release"


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("product", choices=sorted(PRODUCTS))
    args = parser.parse_args(argv)
    print(resolve_release_ref(args.product))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

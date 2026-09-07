#!/usr/bin/env python3
"""Remint the least-privilege Forgejo registry pull token.

The Docker credential helper reads this token from SSM at pull time. Forgejo's
token endpoint requires the bot password over HTTP basic auth, so the complete
flow stays in this process: read the password from SSM, replace the fixed-name
token, mint read:package, verify it against the registry, then overwrite SSM.
Secret values never touch disk, argv, or stdout.
"""
from __future__ import annotations

import argparse
import base64
import json
import sys
import urllib.error
import urllib.request

import boto3

from agentic_os import shared_ssl_context

FORGEJO_API = "https://forgejo.coilysiren.me/api/v1"
REGISTRY_URL = "https://forgejo.coilysiren.me/v2/"
BOT_USER = "coilyco-ops"
TOKEN_NAME = "registry-read-token"
SCOPES = ["read:package"]
PASSWORD_PARAM = "/forgejo/coilyco-ops/password"
TARGET_PARAM = "/forgejo/coilyco-ops/registry-read-token"


def api(method: str, path: str, auth: str, body: dict | None = None) -> tuple[int, object]:
    """Call one Forgejo API endpoint with the supplied authorization header."""
    request = urllib.request.Request(
        f"{FORGEJO_API}{path}",
        data=json.dumps(body).encode("utf-8") if body is not None else None,
        method=method,
        headers={"Authorization": auth, "Content-Type": "application/json"},
    )
    try:
        with urllib.request.urlopen(
            request, timeout=30, context=shared_ssl_context()
        ) as response:
            raw = response.read()
            return response.status, json.loads(raw) if raw else None
    except urllib.error.HTTPError as error:
        detail = error.read().decode("utf-8", errors="replace")
        raise SystemExit(f"{method} {path} failed: {error.code} {detail}") from error


def verify_registry(token: str) -> None:
    """Require the minted token to authenticate against the OCI registry."""
    basic = base64.b64encode(f"{BOT_USER}:{token}".encode()).decode()
    request = urllib.request.Request(
        REGISTRY_URL,
        headers={"Authorization": f"Basic {basic}"},
    )
    try:
        with urllib.request.urlopen(
            request, timeout=30, context=shared_ssl_context()
        ) as response:
            if response.status != 200:
                raise SystemExit(f"registry verification returned HTTP {response.status}")
    except urllib.error.HTTPError as error:
        raise SystemExit(f"registry verification returned HTTP {error.code}") from error


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dry-run", action="store_true", help="print the plan, write nothing")
    args = parser.parse_args()

    print(f"mint {TOKEN_NAME} on {BOT_USER} with scopes {SCOPES}, stash to ssm {TARGET_PARAM}")
    if args.dry_run:
        return 0

    ssm = boto3.client("ssm")
    password = ssm.get_parameter(Name=PASSWORD_PARAM, WithDecryption=True)["Parameter"]["Value"]
    basic = "Basic " + base64.b64encode(f"{BOT_USER}:{password}".encode()).decode()

    _, existing = api("GET", f"/users/{BOT_USER}/tokens", basic)
    for token in existing or []:
        if token.get("name") == TOKEN_NAME:
            api("DELETE", f"/users/{BOT_USER}/tokens/{token['id']}", basic)
            print(f"deleted prior {TOKEN_NAME}")

    _, minted = api(
        "POST",
        f"/users/{BOT_USER}/tokens",
        basic,
        {"name": TOKEN_NAME, "scopes": SCOPES},
    )
    value = (minted or {}).get("sha1", "")
    if not value:
        raise SystemExit("token create returned no value")

    verify_registry(value)
    print("minted and verified against the registry")

    ssm.put_parameter(Name=TARGET_PARAM, Value=value, Type="SecureString", Overwrite=True)
    print(f"ssm {TARGET_PARAM} updated")
    return 0


if __name__ == "__main__":
    sys.exit(main())

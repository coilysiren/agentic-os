#!/usr/bin/env python3
"""Sync Forgejo Actions secrets from their SSM sources of truth.

Repo Actions secrets (Telegram alert credentials, promote/release PAT,
package-repository writers, housecast's PyPI upload token, and deploy's
pin-reconciler pair) are write-only in Forgejo, so drift shows up as
silently-dead alert, publication, or auto-deploy steps. This makes the mapping explicit and re-applying it one verb:
`just sync-actions-secrets` (add `-- --dry-run` to preview).

Entries are keyed `owner/repo`, so the mapping spans orgs.

Values never touch disk or argv: read from SSM with the AWS CLI, PUT straight
to the Forgejo secrets API, authenticated by the attended
``FORGEJO_ADMIN_TOKEN`` value.
"""
from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys
import urllib.request
from pathlib import Path

from agentic_os import shared_ssl_context

FORGEJO_BASE = "https://forgejo.coilysiren.me/api/v1"
OWNER = "coilyco-flight-deck"
TELEGRAM_DEFAULTS_PATH = (
    Path(__file__).resolve().parent.parent
    / "actions"
    / "telegram-alert"
    / "defaults.json"
)


def load_secret_sources(path: Path) -> dict[str, str]:
    """Load Actions-secret to SSM-parameter mappings from an action manifest."""
    try:
        manifest = json.loads(path.read_text(encoding="utf-8"))
        if manifest["schema-version"] != 1:
            raise ValueError("unsupported schema-version")
        secrets = manifest["secrets"]
        sources = {
            value["actions-secret"]: value["ssm-parameter"]
            for value in secrets.values()
        }
    except (KeyError, OSError, TypeError, ValueError, json.JSONDecodeError) as exc:
        raise SystemExit(f"invalid action defaults manifest {path}: {exc}") from exc
    if not sources or any(
        not name or not parameter.startswith("/")
        for name, parameter in sources.items()
    ):
        raise SystemExit(f"invalid action secret sources in {path}")
    return sources


TELEGRAM_SECRET_SOURCES = load_secret_sources(TELEGRAM_DEFAULTS_PATH)

def slug(repo: str, owner: str = OWNER) -> str:
    """Return the ``owner/repo`` key the Forgejo secrets API addresses."""
    return f"{owner}/{repo}"


# owner/repo -> secret name -> SSM parameter (see SSM.md in agentic-os-kai), the
# owner-qualified key letting one mapping span orgs. Writers rotate separately.
MAPPING: dict[str, dict[str, str]] = {
    slug("agentic-os"): {
        **TELEGRAM_SECRET_SOURCES,
        "CI_RELEASE_TOKEN": "/forgejo/coilyco-ops/ci-release-token",
        "TAP_WRITE_TOKEN": "/forgejo/coilyco-ops/tap-bump-token",
        "SCOOP_WRITE_TOKEN": "/forgejo/coilyco-ops/scoop-write-token",
    },
    slug("ward"): {
        **TELEGRAM_SECRET_SOURCES,
        "CI_RELEASE_TOKEN": "/forgejo/coilyco-ops/ci-release-token",
    },
    slug("umbra"): {
        **TELEGRAM_SECRET_SOURCES,
        "CI_RELEASE_TOKEN": "/forgejo/coilyco-ops/ci-release-token",
    },
    # housecast's PyPI train, read by its publish workflow. Trusted publishing
    # has no Forgejo identity provider, so an API token is the only path.
    slug("housecast"): {
        "PYPI_TOKEN": "/coilysiren/pypi/token",
    },
    # deploy's scheduled pin reconciler. Telegram is deliberately absent: the
    # repo already sets those two, and their live values are unreadable here.
    slug("deploy", "coilyco-bridge"): {
        "DEPLOY_PUSH_TOKEN": "/forgejo/coilyco-ops/ci-release-token",
        "REGISTRY_READ_TOKEN": "/forgejo/coilyco-ops/registry-read-token",
    },
}

# Mirrors Forgejo's own models/secret ValidateName, so a bad name fails here at
# plan time instead of as a mid-write HTTP 400 that half-applies the plan.
SECRET_NAME_PATTERN = re.compile("(?i)^[A-Z_][A-Z0-9_]*$")
FORBIDDEN_SECRET_PREFIX = re.compile("(?i)^(FORGEJO_|GITEA_|GITHUB_|[0-9])")


def invalid_secret_names(mapping: dict[str, dict[str, str]]) -> list[str]:
    """Return `repo_slug: NAME` for every secret Forgejo would reject."""
    return [
        f"{repo_slug}: {name}"
        for repo_slug, secrets in mapping.items()
        for name in secrets
        if not SECRET_NAME_PATTERN.match(name)
        or FORBIDDEN_SECRET_PREFIX.match(name)
    ]


def ssm_get(name: str) -> str:
    out = subprocess.run(
        [
            "aws", "ssm", "get-parameter", "--name", name, "--with-decryption",
            "--query", "Parameter.Value", "--output", "text",
        ],
        check=True,
        capture_output=True,
        text=True,
    )
    value = out.stdout.strip()
    if not value:
        raise SystemExit(f"ssm parameter {name} resolved empty")
    return value


def put_secret(token: str, repo_slug: str, name: str, value: str) -> None:
    url = f"{FORGEJO_BASE}/repos/{repo_slug}/actions/secrets/{name}"
    body = json.dumps({"data": value}).encode("utf-8")
    request = urllib.request.Request(
        url,
        data=body,
        method="PUT",
        headers={
            "Authorization": f"token {token}",
            "Content-Type": "application/json",
        },
    )
    with urllib.request.urlopen(
        request, timeout=30, context=shared_ssl_context()
    ) as response:
        response.read()


def admin_token() -> str:
    """Return the attended site-admin token supplied by the operator."""
    token = os.environ.get("FORGEJO_ADMIN_TOKEN", "").strip()
    if not token:
        raise SystemExit(
            "FORGEJO_ADMIN_TOKEN is required; load it with the attended "
            "infrastructure forgejo-admin-token helper"
        )
    return token


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dry-run", action="store_true", help="print the plan, write nothing")
    args = parser.parse_args()

    plan = [
        (repo_slug, name, param)
        for repo_slug, secrets in MAPPING.items()
        for name, param in secrets.items()
    ]
    for repo_slug, name, param in plan:
        print(f"{repo_slug}: {name} <- ssm {param}")

    # Before any write, so a rejected name cannot half-apply the plan.
    rejected = invalid_secret_names(MAPPING)
    if rejected:
        raise SystemExit(
            "Forgejo would reject these secret names (no FORGEJO_/GITEA_/GITHUB_ "
            "prefix, no leading digit):\n  " + "\n  ".join(rejected)
        )

    if args.dry_run:
        return 0

    token = admin_token()
    for repo_slug, name, param in plan:
        put_secret(token, repo_slug, name, ssm_get(param))
        print(f"{repo_slug}: {name} set")
    return 0


if __name__ == "__main__":
    sys.exit(main())

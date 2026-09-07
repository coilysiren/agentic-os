"""Teable schema administration, with a read-back assertion on every write.

This instance reports success without doing the thing in several confirmed
ways, so no write here trusts its own response. Each mutating verb re-reads
through a separate request and asserts the stored object matches what was
sent, failing loudly when it does not. See docs/teable-admin.md.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import urllib.error
import urllib.parse
import urllib.request
from typing import Any

from agentic_os import shared_ssl_context


DEFAULT_BASE_URL = "http://teable:3000/api"

EXIT_CODES = {
    "authorization_failure": 77,
    "api_failure": 69,
    "api_contract": 70,
    "readback_mismatch": 65,
    "refused": 64,
    "invalid_identifier": 64,
}

# Refused by name rather than by absence: an absent verb reads as
# "unimplemented" as readily as "refused". See docs for the reasoning.
REFUSALS = {
    "convert-field": (
        "convert is the one verb that destroys data while reporting the opposite: it emptied "
        "all 6,536 values in a column it declared required, returning 200 with notNull true. "
        "Do it in the Teable UI with an export in hand, and read the column back before "
        "trusting the response."
    ),
    "delete-table": (
        "Teable has no archive verb for a table, so a delete is unrecoverable outside a restic "
        "PVC restore. Rename the table in the Teable UI instead, which is reversible."
    ),
}


class TeableAdminError(RuntimeError):
    """A typed failure rendered without disturbing stdout."""

    def __init__(self, kind: str, message: str) -> None:
        super().__init__(message)
        self.kind = kind
        self.exit_code = EXIT_CODES.get(kind, EXIT_CODES["api_failure"])


class TeableAPI:
    """The subset of Teable's REST API schema administration needs."""

    def __init__(self, base_url: str, token: str) -> None:
        self.base_url = base_url.rstrip("/")
        self.token = token

    def request(
        self,
        method: str,
        path: str,
        query: dict[str, Any] | None = None,
        body: Any | None = None,
    ) -> Any:
        url = self.base_url + path
        if query:
            url += "?" + urllib.parse.urlencode(query, doseq=True)
        data = None
        headers = {"Authorization": f"Bearer {self.token}", "Accept": "application/json"}
        if body is not None:
            data = json.dumps(body).encode("utf-8")
            headers["Content-Type"] = "application/json"
        request = urllib.request.Request(url, data=data, headers=headers, method=method)
        try:
            with urllib.request.urlopen(  # noqa: S310 - operator-supplied base
                request, context=shared_ssl_context()
            ) as response:
                raw = response.read()
        except urllib.error.HTTPError as exc:
            detail = exc.read().decode("utf-8", "replace").strip()
            raise TeableAdminError(
                "api_failure", f"{method} {path} -> {exc.code}: {detail}"
            ) from exc
        except urllib.error.URLError as exc:
            raise TeableAdminError("api_failure", f"{method} {path}: {exc.reason}") from exc
        if not raw:
            return None
        try:
            return json.loads(raw)
        except json.JSONDecodeError as exc:
            raise TeableAdminError(
                "api_contract", f"{method} {path}: response was not JSON"
            ) from exc

    def list_fields(self, table_id: str) -> list[dict[str, Any]]:
        fields = self.request("GET", f"/table/{table_id}/field")
        if not isinstance(fields, list):
            raise TeableAdminError("api_contract", "field list was not an array")
        return fields

    def list_tables(self, base_id: str) -> list[dict[str, Any]]:
        tables = self.request("GET", f"/base/{base_id}/table")
        if not isinstance(tables, list):
            raise TeableAdminError("api_contract", "table list was not an array")
        return tables


def mismatches(requested: dict[str, Any], stored: dict[str, Any]) -> list[str]:
    """Every requested property that did not survive the round trip.

    This is the whole of the create defect: unknown properties are accepted
    and silently discarded, so a field asked for with five properties can be
    stored with three and return 200 either way. Only a key-by-key diff
    against a re-read finds the two that vanished.
    """
    problems = []
    for key, want in requested.items():
        if key not in stored:
            problems.append(f"{key}: requested, absent from the read-back")
        elif stored[key] != want:
            problems.append(f"{key}: requested {want!r}, stored {stored[key]!r}")
    return problems


def create_field(api: TeableAPI, table_id: str, spec: dict[str, Any]) -> dict[str, Any]:
    """Create one field, then prove it through an independent read."""
    created = api.request("POST", f"/table/{table_id}/field", body=spec)
    if not isinstance(created, dict) or not created.get("id"):
        raise TeableAdminError(
            "api_contract", "create returned no field id, so it cannot be read back"
        )
    field_id = created["id"]
    # A separate request on purpose. The create response is the thing under
    # suspicion, so it is never the evidence.
    stored = next((f for f in api.list_fields(table_id) if f.get("id") == field_id), None)
    if stored is None:
        raise TeableAdminError(
            "readback_mismatch",
            f"the tracker reported field {field_id} created, and a fresh field list does not carry it",
        )
    problems = mismatches(spec, stored)
    if problems:
        raise TeableAdminError(
            "readback_mismatch",
            f"field {field_id} exists and does not match what was requested:\n  "
            + "\n  ".join(problems)
            + "\nNothing was deleted: there is no delete-field verb. Remove it in the Teable UI.",
        )
    return stored


def create_table(api: TeableAPI, base_id: str, spec: dict[str, Any]) -> dict[str, Any]:
    """Create one table, then prove it through an independent read."""
    created = api.request("POST", f"/base/{base_id}/table", body=spec)
    if not isinstance(created, dict) or not created.get("id"):
        raise TeableAdminError(
            "api_contract", "create returned no table id, so it cannot be read back"
        )
    table_id = created["id"]
    stored = next((t for t in api.list_tables(base_id) if t.get("id") == table_id), None)
    if stored is None:
        raise TeableAdminError(
            "readback_mismatch",
            f"the tracker reported table {table_id} created, and a fresh table list does not carry it",
        )
    problems = mismatches({k: v for k, v in spec.items() if k != "fields"}, stored)
    if problems:
        raise TeableAdminError(
            "readback_mismatch",
            f"table {table_id} exists and does not match what was requested:\n  "
            + "\n  ".join(problems),
        )
    return stored


def _read_spec(path: str) -> dict[str, Any]:
    try:
        with open(path, encoding="utf-8") as handle:
            spec = json.load(handle)
    except OSError as exc:
        raise TeableAdminError("invalid_identifier", f"read spec {path}: {exc}") from exc
    except json.JSONDecodeError as exc:
        raise TeableAdminError("invalid_identifier", f"parse spec {path}: {exc}") from exc
    if not isinstance(spec, dict):
        raise TeableAdminError("invalid_identifier", f"{path}: spec must be a JSON object")
    return spec


def _parse_args(argv: list[str] | None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        prog="teable-admin",
        description="Teable schema administration with a read-back assertion on every write.",
    )
    sub = parser.add_subparsers(dest="command", required=True)

    create = sub.add_parser("create-field", help="create a field and prove it stored as requested")
    create.add_argument("table", help="table id")
    create.add_argument("--spec", required=True, help="path to a JSON file of field properties")

    listing = sub.add_parser("list-fields", help="list a table's fields")
    listing.add_argument("table", help="table id")

    table = sub.add_parser("create-table", help="create a table and prove it stored as requested")
    table.add_argument("base", help="base id")
    table.add_argument("--spec", required=True, help="path to a JSON file of table properties")

    describe = sub.add_parser("describe-base", help="list the tables in a base")
    describe.add_argument("base", help="base id")

    for refused in REFUSALS:
        sub.add_parser(refused, help="NOT AVAILABLE: refused by policy, run it for the reason")

    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = _parse_args(argv)

    if args.command in REFUSALS:
        print(
            f"teable-admin-error: refused: {args.command} is not available. {REFUSALS[args.command]}",
            file=sys.stderr,
        )
        return EXIT_CODES["refused"]

    token = os.environ.get("TEABLE_API_TOKEN")
    if not token:
        print(
            "teable-admin-error: authorization_failure: TEABLE_API_TOKEN is required",
            file=sys.stderr,
        )
        return EXIT_CODES["authorization_failure"]
    api = TeableAPI(os.environ.get("TEABLE_BASE_URL", DEFAULT_BASE_URL), token)

    try:
        if args.command == "create-field":
            result: Any = create_field(api, args.table, _read_spec(args.spec))
        elif args.command == "list-fields":
            result = api.list_fields(args.table)
        elif args.command == "create-table":
            result = create_table(api, args.base, _read_spec(args.spec))
        else:
            result = api.list_tables(args.base)
    except TeableAdminError as exc:
        print(f"teable-admin-error: {exc.kind}: {exc}", file=sys.stderr)
        return exc.exit_code

    print(json.dumps(result, indent=2, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

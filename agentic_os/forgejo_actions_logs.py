"""Fetch Forgejo Actions logs through the official Forgejo 16 REST API."""

from __future__ import annotations

import argparse
import dataclasses
import io
import json
import os
import re
import sys
import urllib.error
import urllib.parse
import urllib.request
import zipfile

from agentic_os import shared_ssl_context
from collections.abc import Mapping
from typing import Any


DEFAULT_BASE_URL = "https://forgejo.coilysiren.me"
DEFAULT_MAX_BYTES = 64 * 1024 * 1024
DEFAULT_CHUNK_BYTES = 1024 * 1024
MAX_METADATA_BYTES = 4 * 1024 * 1024
TERMINAL_STATUSES = frozenset({"success", "failure", "cancelled", "skipped"})
CONTENT_RANGE_RE = re.compile(r"^bytes (?P<start>\d+)-(?P<end>\d+)/(?P<total>\d+)$")
EMPTY_CONTENT_RANGE_RE = re.compile(r"^bytes \*/0$")

EXIT_CODES = {
    "invalid_identifier": 64,
    "too_large": 65,
    "missing_run": 66,
    "missing_job": 66,
    "missing_attempt": 66,
    "running_log": 75,
    "authorization_failure": 77,
    "expired_log": 69,
    "log_unavailable": 69,
    "api_failure": 69,
    "api_contract": 70,
}


class ForgejoActionsLogError(RuntimeError):
    """A typed failure that the command renders without changing stdout."""

    def __init__(self, kind: str, message: str):
        super().__init__(message)
        self.kind = kind
        self.exit_code = EXIT_CODES[kind]


@dataclasses.dataclass(frozen=True)
class RepositoryTarget:
    owner: str
    repo: str

    def api_path(self) -> str:
        owner = urllib.parse.quote(self.owner, safe="")
        repo = urllib.parse.quote(self.repo, safe="")
        return f"/repos/{owner}/{repo}/actions"


@dataclasses.dataclass(frozen=True)
class ResolvedRun:
    id: int
    index: int
    status: str

    @property
    def is_complete(self) -> bool:
        return self.status in TERMINAL_STATUSES


@dataclasses.dataclass(frozen=True)
class ResolvedJob:
    id: int
    index: int
    name: str
    attempt: int
    status: str
    task_id: int

    @property
    def is_complete(self) -> bool:
        return self.status in TERMINAL_STATUSES


@dataclasses.dataclass(frozen=True)
class LogWarning:
    kind: str
    message: str


@dataclasses.dataclass(frozen=True)
class FetchResult:
    data: bytes
    content_type: str
    state: str
    warnings: tuple[LogWarning, ...] = ()


def _positive_int(value: str) -> int:
    parsed = int(value)
    if parsed < 1:
        raise argparse.ArgumentTypeError("must be a positive integer")
    return parsed


def _required_positive(value: Any, *, field: str) -> int:
    if isinstance(value, bool):
        raise ForgejoActionsLogError(
            "api_contract", f"Forgejo returned a non-integer {field}."
        )
    try:
        parsed = int(value)
    except (TypeError, ValueError) as exc:
        raise ForgejoActionsLogError(
            "api_contract", f"Forgejo returned a non-integer {field}."
        ) from exc
    if parsed < 1:
        raise ForgejoActionsLogError(
            "api_contract", f"Forgejo returned a non-positive {field}."
        )
    return parsed


def _status(value: Any, *, field: str) -> str:
    if not isinstance(value, str) or not value:
        raise ForgejoActionsLogError(
            "api_contract", f"Forgejo returned an invalid {field}."
        )
    return value


def _read_bounded(response: Any, max_bytes: int) -> bytes:
    content_length = response.headers.get("Content-Length")
    if content_length:
        try:
            advertised = int(content_length)
        except ValueError:
            advertised = 0
        if advertised > max_bytes:
            raise ForgejoActionsLogError(
                "too_large",
                f"Forgejo advertised {advertised} bytes, above the {max_bytes}-byte limit.",
            )

    chunks: list[bytes] = []
    total = 0
    while total <= max_bytes:
        chunk = response.read(min(64 * 1024, max_bytes + 1 - total))
        if not chunk:
            break
        chunks.append(chunk)
        total += len(chunk)
    if total > max_bytes:
        raise ForgejoActionsLogError(
            "too_large", f"Forgejo returned more than the {max_bytes}-byte limit."
        )
    return b"".join(chunks)


def _http_error(
    exc: urllib.error.HTTPError, *, missing_kind: str
) -> ForgejoActionsLogError:
    if exc.code in (401, 403):
        return ForgejoActionsLogError(
            "authorization_failure",
            f"Forgejo denied the API request with HTTP {exc.code}.",
        )
    if exc.code == 404:
        return ForgejoActionsLogError(
            missing_kind, f"Forgejo returned HTTP 404 for the requested {missing_kind[8:]}."
        )
    return ForgejoActionsLogError(
        "api_failure", f"Forgejo returned HTTP {exc.code} for the API request."
    )


class ForgejoAPI:
    """Small byte-oriented Forgejo API client with bounded reads."""

    def __init__(self, *, base_url: str, token: str):
        self.base_url = f"{base_url.rstrip('/')}/api/v1"
        self.token = token

    def open(
        self,
        path: str,
        *,
        query: Mapping[str, str | int] | None = None,
        accept: str,
        headers: Mapping[str, str] | None = None,
    ) -> Any:
        url = f"{self.base_url}{path}"
        if query:
            url = f"{url}?{urllib.parse.urlencode(query)}"
        request_headers = {
            "Authorization": f"token {self.token}",
            "Accept": accept,
        }
        if headers:
            request_headers.update(headers)
        request = urllib.request.Request(url, headers=request_headers)
        return urllib.request.urlopen(request, context=shared_ssl_context())

    def get_json(
        self,
        path: str,
        *,
        query: Mapping[str, str | int] | None = None,
        missing_kind: str,
    ) -> Any:
        try:
            with self.open(path, query=query, accept="application/json") as response:
                body = _read_bounded(response, MAX_METADATA_BYTES)
        except urllib.error.HTTPError as exc:
            raise _http_error(exc, missing_kind=missing_kind) from exc
        except OSError as exc:
            raise ForgejoActionsLogError(
                "api_failure", f"Forgejo API request failed: {exc}."
            ) from exc
        try:
            return json.loads(body)
        except (UnicodeDecodeError, json.JSONDecodeError) as exc:
            raise ForgejoActionsLogError(
                "api_contract", "Forgejo returned unreadable JSON metadata."
            ) from exc

    def get_binary(
        self,
        path: str,
        *,
        query: Mapping[str, str | int] | None = None,
        accept: str,
        max_bytes: int,
        missing_kind: str,
    ) -> bytes:
        try:
            with self.open(path, query=query, accept=accept) as response:
                return _read_bounded(response, max_bytes)
        except urllib.error.HTTPError as exc:
            raise _http_error(exc, missing_kind=missing_kind) from exc
        except OSError as exc:
            raise ForgejoActionsLogError(
                "api_failure", f"Forgejo API request failed: {exc}."
            ) from exc


def _parse_identifier(identifier: str, *, kind: str) -> tuple[str, int | str]:
    if identifier.startswith("id:"):
        raw = identifier.removeprefix("id:")
        try:
            value = int(raw)
        except ValueError as exc:
            raise ForgejoActionsLogError(
                "invalid_identifier", f"{kind} identifier {identifier!r} is invalid."
            ) from exc
        if value < 1:
            raise ForgejoActionsLogError(
                "invalid_identifier", f"{kind} IDs must be positive."
            )
        return "id", value
    if kind == "run":
        try:
            value = int(identifier)
        except ValueError as exc:
            raise ForgejoActionsLogError(
                "invalid_identifier",
                f"run identifier {identifier!r} must be a visible run number or id:<n>.",
            ) from exc
        if value < 1:
            raise ForgejoActionsLogError(
                "invalid_identifier", "visible run numbers must be positive."
            )
        return "index", value
    if identifier.startswith("name:"):
        name = identifier.removeprefix("name:")
        if not name:
            raise ForgejoActionsLogError(
                "invalid_identifier", "job name identifiers cannot be empty."
            )
        return "name", name
    try:
        value = int(identifier)
    except ValueError:
        return "name", identifier
    if value < 0:
        raise ForgejoActionsLogError(
            "invalid_identifier", "visible job indexes cannot be negative."
        )
    return "index", value


def _resolved_run(payload: Any) -> ResolvedRun:
    if not isinstance(payload, dict):
        raise ForgejoActionsLogError(
            "api_contract", "Forgejo returned a non-object run."
        )
    return ResolvedRun(
        id=_required_positive(payload.get("id"), field="run id"),
        index=_required_positive(payload.get("index_in_repo"), field="run number"),
        status=_status(payload.get("status"), field="run status"),
    )


def resolve_run(
    api: ForgejoAPI, repository: RepositoryTarget, identifier: str
) -> ResolvedRun:
    form, value = _parse_identifier(identifier, kind="run")
    base = repository.api_path()
    if form == "id":
        payload = api.get_json(
            f"{base}/runs/{value}", missing_kind="missing_run"
        )
        return _resolved_run(payload)

    payload = api.get_json(
        f"{base}/runs",
        query={"run_number": int(value), "limit": 2},
        missing_kind="missing_run",
    )
    if not isinstance(payload, dict) or not isinstance(
        payload.get("workflow_runs"), list
    ):
        raise ForgejoActionsLogError(
            "api_contract", "Forgejo returned an invalid workflow-run list."
        )
    matches = payload["workflow_runs"]
    if not matches:
        raise ForgejoActionsLogError(
            "missing_run", f"Forgejo has no visible run {value} in this repository."
        )
    if len(matches) != 1:
        raise ForgejoActionsLogError(
            "api_contract", f"Forgejo returned multiple matches for visible run {value}."
        )
    return _resolved_run(matches[0])


def list_jobs(
    api: ForgejoAPI, repository: RepositoryTarget, run: ResolvedRun
) -> list[ResolvedJob]:
    payload = api.get_json(
        f"{repository.api_path()}/runs/{run.id}/jobs",
        missing_kind="missing_run",
    )
    if not isinstance(payload, list):
        raise ForgejoActionsLogError(
            "api_contract", "Forgejo returned a non-list workflow-job response."
        )
    jobs: list[ResolvedJob] = []
    for index, job in enumerate(payload):
        if not isinstance(job, dict):
            raise ForgejoActionsLogError(
                "api_contract", "Forgejo returned a non-object workflow job."
            )
        name = job.get("name")
        if not isinstance(name, str) or not name:
            raise ForgejoActionsLogError(
                "api_contract", "Forgejo returned a job without a name."
            )
        attempt = job.get("attempt", 0)
        task_id = job.get("task_id", 0)
        if isinstance(attempt, bool) or not isinstance(attempt, int) or attempt < 0:
            raise ForgejoActionsLogError(
                "api_contract", "Forgejo returned an invalid job attempt."
            )
        if isinstance(task_id, bool) or not isinstance(task_id, int) or task_id < 0:
            raise ForgejoActionsLogError(
                "api_contract", "Forgejo returned an invalid job task id."
            )
        jobs.append(
            ResolvedJob(
                id=_required_positive(job.get("id"), field="job id"),
                index=index,
                name=name,
                attempt=attempt,
                status=_status(job.get("status"), field="job status"),
                task_id=task_id,
            )
        )
    return jobs


def resolve_job(jobs: list[ResolvedJob], identifier: str) -> ResolvedJob:
    form, value = _parse_identifier(identifier, kind="job")
    if form == "index":
        index = int(value)
        if index >= len(jobs):
            raise ForgejoActionsLogError(
                "missing_job", f"run has no visible job index {index}."
            )
        return jobs[index]
    if form == "id":
        matches = [job for job in jobs if job.id == value]
    else:
        matches = [job for job in jobs if job.name == value]
    if not matches:
        raise ForgejoActionsLogError(
            "missing_job", f"run has no job matching {identifier!r}."
        )
    if len(matches) > 1:
        raise ForgejoActionsLogError(
            "invalid_identifier",
            f"job name {value!r} is ambiguous; use the visible index or id:<n>.",
        )
    return matches[0]


def _parse_content_range(value: str) -> tuple[int, int, int]:
    match = CONTENT_RANGE_RE.fullmatch(value)
    if not match:
        raise ForgejoActionsLogError(
            "api_contract", "Forgejo returned an invalid Content-Range header."
        )
    return tuple(int(match.group(name)) for name in ("start", "end", "total"))


def _job_404_error(run: ResolvedRun, job: ResolvedJob) -> ForgejoActionsLogError:
    if not run.is_complete or not job.is_complete:
        return ForgejoActionsLogError(
            "running_log", "the job exists, but its log is not available yet."
        )
    if job.task_id == 0:
        return ForgejoActionsLogError(
            "log_unavailable", "the completed job was never executed and has no log."
        )
    return ForgejoActionsLogError(
        "expired_log", "the completed job exists, but Forgejo no longer has its log."
    )


def fetch_job_bytes(
    api: ForgejoAPI,
    repository: RepositoryTarget,
    run: ResolvedRun,
    job: ResolvedJob,
    *,
    attempt: int | None,
    max_bytes: int,
    chunk_bytes: int,
) -> bytes:
    if attempt is not None and attempt > job.attempt:
        raise ForgejoActionsLogError(
            "missing_attempt",
            f"job has {job.attempt} attempt(s), so attempt {attempt} does not exist.",
        )
    query = {"attempt": attempt} if attempt is not None else None
    path = f"{repository.api_path()}/jobs/{job.id}/logs"
    chunks: list[bytes] = []
    offset = 0

    while offset <= max_bytes:
        end = min(offset + chunk_bytes - 1, max_bytes)
        try:
            with api.open(
                path,
                query=query,
                accept="text/plain",
                headers={"Range": f"bytes={offset}-{end}"},
            ) as response:
                status = response.status
                read_limit = (
                    max_bytes if status == 200 and offset == 0 else end - offset + 1
                )
                body = _read_bounded(response, read_limit)
                content_range = response.headers.get("Content-Range", "")
        except urllib.error.HTTPError as exc:
            if exc.code == 416:
                content_range = exc.headers.get("Content-Range", "")
                if offset == 0 and EMPTY_CONTENT_RANGE_RE.fullmatch(content_range):
                    return b""
                raise ForgejoActionsLogError(
                    "api_contract", "Forgejo rejected a valid log byte range."
                ) from exc
            if exc.code == 404:
                raise _job_404_error(run, job) from exc
            raise _http_error(exc, missing_kind="missing_job") from exc
        except OSError as exc:
            raise ForgejoActionsLogError(
                "api_failure", f"Forgejo API request failed: {exc}."
            ) from exc

        if status == 200:
            if offset:
                raise ForgejoActionsLogError(
                    "api_contract", "Forgejo stopped honoring log byte ranges."
                )
            return body
        if status != 206:
            raise ForgejoActionsLogError(
                "api_contract", f"Forgejo returned unexpected HTTP {status} for a log range."
            )

        start, range_end, total = _parse_content_range(content_range)
        if (
            start != offset
            or range_end >= total
            or range_end + 1 - start != len(body)
        ):
            raise ForgejoActionsLogError(
                "api_contract", "Forgejo returned a mismatched log byte range."
            )
        if total > max_bytes:
            raise ForgejoActionsLogError(
                "too_large",
                f"job log is {total} bytes, above the {max_bytes}-byte limit.",
            )
        chunks.append(body)
        offset += len(body)
        if offset >= total:
            return b"".join(chunks)
        if not body:
            raise ForgejoActionsLogError(
                "api_contract", "Forgejo returned an empty partial log range."
            )

    raise ForgejoActionsLogError(
        "too_large", f"job log exceeds the {max_bytes}-byte limit."
    )


def _run_archive_warnings(data: bytes) -> tuple[LogWarning, ...]:
    try:
        archive = zipfile.ZipFile(io.BytesIO(data))
    except zipfile.BadZipFile as exc:
        raise ForgejoActionsLogError(
            "api_contract", "Forgejo returned an invalid whole-run ZIP archive."
        ) from exc
    warnings: list[LogWarning] = []
    with archive:
        for entry in archive.infolist():
            if not entry.filename.endswith(".MISSING"):
                continue
            with archive.open(entry) as marker_file:
                marker = marker_file.read(4097)
            if marker == b"logs have been cleaned up\n":
                kind = "expired_log"
            elif marker == b"job has not been executed yet\n":
                kind = "running_log"
            else:
                kind = "log_unavailable"
            warnings.append(
                LogWarning(kind, f"whole-run archive contains {entry.filename}.")
            )
    return tuple(warnings)


def fetch_logs(
    api: ForgejoAPI,
    repository: RepositoryTarget,
    run_identifier: str,
    *,
    job_identifier: str | None,
    attempt: int | None,
    max_bytes: int,
    chunk_bytes: int = DEFAULT_CHUNK_BYTES,
) -> FetchResult:
    run = resolve_run(api, repository, run_identifier)
    state = "completed" if run.is_complete else "running"
    if job_identifier is None:
        data = api.get_binary(
            f"{repository.api_path()}/runs/{run.id}/logs",
            accept="application/zip",
            max_bytes=max_bytes,
            missing_kind="missing_run",
        )
        warnings = list(_run_archive_warnings(data))
        if state == "running":
            warnings.insert(
                0,
                LogWarning(
                    "running_log", "whole-run archive is a snapshot of a running workflow."
                ),
            )
        return FetchResult(data, "application/zip", state, tuple(warnings))

    jobs = list_jobs(api, repository, run)
    job = resolve_job(jobs, job_identifier)
    data = fetch_job_bytes(
        api,
        repository,
        run,
        job,
        attempt=attempt,
        max_bytes=max_bytes,
        chunk_bytes=chunk_bytes,
    )
    warnings: tuple[LogWarning, ...] = ()
    if state == "running" or not job.is_complete:
        state = "running"
        warnings = (
            LogWarning("running_log", "job output is a snapshot of a running workflow."),
        )
    return FetchResult(data, "text/plain", state, warnings)


def _parse_args(argv: list[str] | None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        prog="actions logs",
        description="Fetch exact Forgejo 16 workflow-run or job log bytes.",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""identifier forms:
  run       visible run number, or id:<internal-run-id>
  job       zero-based visible index, exact name, name:<exact-name>, or id:<job-id>
  attempt   positive 1-based attempt; omit for the latest

Omit job for a whole-run ZIP. Job output is exact log bytes. Diagnostics go to
stderr, and stdout stays empty on typed errors. Output is bounded to 64 MiB by
default; use --max-bytes to choose a different positive bound.""",
    )
    parser.add_argument("owner", help="repository owner")
    parser.add_argument("repo", help="repository name")
    parser.add_argument("run", help="visible run number or id:<n>")
    parser.add_argument("job", nargs="?", help="visible index, exact name, or id:<n>")
    parser.add_argument(
        "attempt", type=_positive_int, nargs="?", help="1-based historical attempt"
    )
    parser.add_argument(
        "--max-bytes",
        type=_positive_int,
        default=DEFAULT_MAX_BYTES,
        help=f"maximum output bytes (default: {DEFAULT_MAX_BYTES})",
    )
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = _parse_args(argv)
    token = os.environ.get("FORGEJO_TOKEN")
    if not token:
        print(
            "forgejo-actions-log-error: authorization_failure: FORGEJO_TOKEN is required",
            file=sys.stderr,
        )
        return EXIT_CODES["authorization_failure"]

    base_url = os.environ.get("FORGEJO_BASE_URL", DEFAULT_BASE_URL)
    api = ForgejoAPI(base_url=base_url, token=token)
    repository = RepositoryTarget(args.owner, args.repo)
    try:
        result = fetch_logs(
            api,
            repository,
            args.run,
            job_identifier=args.job,
            attempt=args.attempt,
            max_bytes=args.max_bytes,
        )
    except ForgejoActionsLogError as exc:
        print(f"forgejo-actions-log-error: {exc.kind}: {exc}", file=sys.stderr)
        return exc.exit_code

    for warning in result.warnings:
        print(
            f"forgejo-actions-log-warning: {warning.kind}: {warning.message}",
            file=sys.stderr,
        )
    sys.stdout.buffer.write(result.data)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

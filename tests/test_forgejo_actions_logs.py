from __future__ import annotations

import io
import json
import urllib.error
import urllib.parse
import zipfile
from collections.abc import Callable
from email.message import Message
from typing import Any

import pytest

from agentic_os import forgejo_actions_logs as logs


RUN = {
    "id": 6281,
    "index_in_repo": 886,
    "status": "failure",
}
JOB = {
    "id": 9134,
    "name": "test",
    "attempt": 2,
    "status": "failure",
    "task_id": 771,
}
REPOSITORY = logs.RepositoryTarget("coilyco-flight-deck", "agentic-os")


class FakeResponse:
    def __init__(
        self,
        data: bytes,
        *,
        status: int = 200,
        headers: dict[str, str] | None = None,
    ):
        self._body = io.BytesIO(data)
        self.status = status
        self.headers = Message()
        for key, value in (headers or {}).items():
            self.headers[key] = value

    def __enter__(self) -> FakeResponse:
        return self

    def __exit__(self, *args: object) -> None:
        return None

    def getcode(self) -> int:
        return self.status

    def read(self, size: int = -1) -> bytes:
        return self._body.read(size)


def _json_response(payload: Any) -> FakeResponse:
    return FakeResponse(
        json.dumps(payload).encode("utf-8"),
        headers={"Content-Type": "application/json"},
    )


def _http_error(request: urllib.request.Request, code: int, **headers: str):
    message = Message()
    for key, value in headers.items():
        message[key.replace("_", "-")] = value
    return urllib.error.HTTPError(
        request.full_url,
        code,
        "fixture",
        message,
        io.BytesIO(b""),
    )


def _install(
    monkeypatch: pytest.MonkeyPatch,
    handler: Callable[[urllib.request.Request], FakeResponse],
) -> tuple[logs.ForgejoAPI, list[urllib.request.Request]]:
    calls: list[urllib.request.Request] = []

    def fake_urlopen(request: urllib.request.Request, **_: object) -> FakeResponse:
        calls.append(request)
        return handler(request)

    monkeypatch.setattr(logs.urllib.request, "urlopen", fake_urlopen)
    return logs.ForgejoAPI(base_url="https://forgejo.example", token="secret"), calls


def _metadata(
    request: urllib.request.Request,
    *,
    run: dict[str, Any] = RUN,
    jobs: list[dict[str, Any]] | None = None,
) -> FakeResponse | None:
    parsed = urllib.parse.urlparse(request.full_url)
    if parsed.path.endswith("/actions/runs"):
        query = urllib.parse.parse_qs(parsed.query)
        assert query == {"run_number": ["886"], "limit": ["2"]}
        return _json_response({"total_count": 1, "workflow_runs": [run]})
    if parsed.path.endswith("/actions/runs/6281/jobs"):
        return _json_response([JOB] if jobs is None else jobs)
    return None


def _range_response(request: urllib.request.Request, data: bytes) -> FakeResponse:
    value = request.get_header("Range")
    assert value is not None
    start_raw, end_raw = value.removeprefix("bytes=").split("-", 1)
    start = int(start_raw)
    end = min(int(end_raw), len(data) - 1)
    if start >= len(data):
        raise _http_error(request, 416, Content_Range=f"bytes */{len(data)}")
    body = data[start : end + 1]
    return FakeResponse(
        body,
        status=206,
        headers={
            "Content-Range": f"bytes {start}-{end}/{len(data)}",
            "Content-Length": str(len(body)),
        },
    )


def _fetch_job(
    api: logs.ForgejoAPI,
    *,
    job: str = "0",
    attempt: int | None = None,
    max_bytes: int = 1024,
    chunk_bytes: int = 4,
) -> logs.FetchResult:
    return logs.fetch_logs(
        api,
        REPOSITORY,
        "886",
        job_identifier=job,
        attempt=attempt,
        max_bytes=max_bytes,
        chunk_bytes=chunk_bytes,
    )


def test_job_log_resolves_visible_identifiers_and_chunks_exact_bytes(monkeypatch):
    expected = b"0123456789"

    def handler(request):
        metadata = _metadata(request)
        if metadata:
            return metadata
        assert request.full_url.endswith("/actions/jobs/9134/logs")
        return _range_response(request, expected)

    api, calls = _install(monkeypatch, handler)
    result = _fetch_job(api)

    assert result.data == expected
    assert result.content_type == "text/plain"
    assert result.state == "completed"
    assert [call.get_header("Range") for call in calls[-3:]] == [
        "bytes=0-3",
        "bytes=4-7",
        "bytes=8-11",
    ]
    assert calls[-1].get_header("Authorization") == "token secret"


def test_job_log_preserves_non_utf8_bytes(monkeypatch):
    expected = b"ok\n\xff\xfe\x80\n"

    def handler(request):
        return _metadata(request) or _range_response(request, expected)

    api, _ = _install(monkeypatch, handler)

    assert _fetch_job(api, chunk_bytes=64).data == expected


def test_job_log_accepts_a_bounded_server_that_ignores_range(monkeypatch):
    expected = b"full response\n"

    def handler(request):
        metadata = _metadata(request)
        if metadata:
            return metadata
        return FakeResponse(expected, headers={"Content-Length": str(len(expected))})

    api, _ = _install(monkeypatch, handler)

    assert _fetch_job(api, max_bytes=len(expected), chunk_bytes=4).data == expected


def test_empty_job_log_accepts_an_unsatisfiable_initial_range(monkeypatch):
    def handler(request):
        metadata = _metadata(request)
        if metadata:
            return metadata
        raise _http_error(request, 416, Content_Range="bytes */0")

    api, _ = _install(monkeypatch, handler)

    assert _fetch_job(api).data == b""


def test_large_job_log_fails_before_writing_output(monkeypatch):
    def handler(request):
        return _metadata(request) or _range_response(request, b"123456")

    api, _ = _install(monkeypatch, handler)

    with pytest.raises(logs.ForgejoActionsLogError) as raised:
        _fetch_job(api, max_bytes=5)

    assert raised.value.kind == "too_large"


def test_historical_attempt_is_forwarded_after_job_resolution(monkeypatch):
    expected = b"attempt one\n"

    def handler(request):
        metadata = _metadata(request)
        if metadata:
            return metadata
        query = urllib.parse.parse_qs(urllib.parse.urlparse(request.full_url).query)
        assert query == {"attempt": ["1"]}
        return _range_response(request, expected)

    api, _ = _install(monkeypatch, handler)

    assert _fetch_job(api, attempt=1, chunk_bytes=64).data == expected


def test_attempt_above_the_reported_retry_count_is_typed(monkeypatch):
    api, _ = _install(monkeypatch, lambda request: _metadata(request))

    with pytest.raises(logs.ForgejoActionsLogError) as raised:
        _fetch_job(api, attempt=3)

    assert raised.value.kind == "missing_attempt"


def test_expired_completed_job_log_is_distinct_from_a_missing_job(monkeypatch):
    def handler(request):
        metadata = _metadata(request)
        if metadata:
            return metadata
        raise _http_error(request, 404)

    api, _ = _install(monkeypatch, handler)

    with pytest.raises(logs.ForgejoActionsLogError) as raised:
        _fetch_job(api)
    assert raised.value.kind == "expired_log"

    with pytest.raises(logs.ForgejoActionsLogError) as raised:
        _fetch_job(api, job="1")
    assert raised.value.kind == "missing_job"


def test_running_job_success_and_not_ready_are_distinct(monkeypatch):
    running_run = {**RUN, "status": "running"}
    running_job = {**JOB, "status": "running"}

    def success_handler(request):
        metadata = _metadata(request, run=running_run, jobs=[running_job])
        return metadata or _range_response(request, b"still running\n")

    api, _ = _install(monkeypatch, success_handler)
    result = _fetch_job(api, chunk_bytes=64)
    assert result.state == "running"
    assert [warning.kind for warning in result.warnings] == ["running_log"]

    def not_ready_handler(request):
        metadata = _metadata(request, run=running_run, jobs=[running_job])
        if metadata:
            return metadata
        raise _http_error(request, 404)

    api, _ = _install(monkeypatch, not_ready_handler)
    with pytest.raises(logs.ForgejoActionsLogError) as raised:
        _fetch_job(api)
    assert raised.value.kind == "running_log"


def test_missing_run_and_authorization_failure_are_typed(monkeypatch):
    def missing_handler(request):
        return _json_response({"total_count": 0, "workflow_runs": []})

    api, _ = _install(monkeypatch, missing_handler)
    with pytest.raises(logs.ForgejoActionsLogError) as raised:
        _fetch_job(api)
    assert raised.value.kind == "missing_run"

    def denied_handler(request):
        raise _http_error(request, 403)

    api, _ = _install(monkeypatch, denied_handler)
    with pytest.raises(logs.ForgejoActionsLogError) as raised:
        _fetch_job(api)
    assert raised.value.kind == "authorization_failure"


def test_job_id_and_name_forms_resolve_without_exposing_task_ids(monkeypatch):
    expected = b"log\n"

    def handler(request):
        return _metadata(request) or _range_response(request, expected)

    api, _ = _install(monkeypatch, handler)

    assert _fetch_job(api, job="id:9134").data == expected
    assert _fetch_job(api, job="name:test").data == expected


def test_whole_run_returns_the_exact_zip_and_reports_expired_entries(monkeypatch):
    body = io.BytesIO()
    with zipfile.ZipFile(body, "w") as archive:
        archive.writestr("test-9134-attempt-2.log", b"ok\n")
        archive.writestr("old-9135-attempt-1.MISSING", b"logs have been cleaned up\n")
    expected = body.getvalue()

    def handler(request):
        metadata = _metadata(request)
        if metadata:
            return metadata
        assert request.full_url.endswith("/actions/runs/6281/logs")
        return FakeResponse(
            expected,
            headers={
                "Content-Type": "application/zip",
                "Content-Length": str(len(expected)),
            },
        )

    api, _ = _install(monkeypatch, handler)
    result = logs.fetch_logs(
        api,
        REPOSITORY,
        "886",
        job_identifier=None,
        attempt=None,
        max_bytes=len(expected),
    )

    assert result.data == expected
    assert result.content_type == "application/zip"
    assert result.state == "completed"
    assert [warning.kind for warning in result.warnings] == ["expired_log"]


def test_whole_run_is_bounded_by_content_length(monkeypatch):
    def handler(request):
        metadata = _metadata(request)
        if metadata:
            return metadata
        return FakeResponse(b"", headers={"Content-Length": "101"})

    api, _ = _install(monkeypatch, handler)

    with pytest.raises(logs.ForgejoActionsLogError) as raised:
        logs.fetch_logs(
            api,
            REPOSITORY,
            "886",
            job_identifier=None,
            attempt=None,
            max_bytes=100,
        )

    assert raised.value.kind == "too_large"


def test_internal_run_id_uses_the_single_run_endpoint(monkeypatch):
    def handler(request):
        parsed = urllib.parse.urlparse(request.full_url)
        if parsed.path.endswith("/actions/runs/6281"):
            return _json_response(RUN)
        if parsed.path.endswith("/actions/runs/6281/jobs"):
            return _json_response([JOB])
        return _range_response(request, b"log\n")

    api, calls = _install(monkeypatch, handler)
    result = logs.fetch_logs(
        api,
        REPOSITORY,
        "id:6281",
        job_identifier="0",
        attempt=None,
        max_bytes=100,
    )

    assert result.data == b"log\n"
    assert "/actions/runs/6281" in calls[0].full_url


def test_attempt_defaults_to_latest_and_whole_run_omits_job():
    job = logs._parse_args(["o", "r", "886", "0"])
    whole_run = logs._parse_args(["o", "r", "886"])

    assert job.attempt is None
    assert whole_run.job is None


def test_main_writes_exact_bytes_and_warnings_to_separate_streams(
    monkeypatch, capsysbinary
):
    expected = b"\xffraw\n"
    monkeypatch.setenv("FORGEJO_TOKEN", "secret")
    monkeypatch.setattr(
        logs,
        "fetch_logs",
        lambda *args, **kwargs: logs.FetchResult(
            expected,
            "text/plain",
            "running",
            (logs.LogWarning("running_log", "snapshot"),),
        ),
    )

    assert logs.main(["coilyco-example", "repo", "1", "0"]) == 0

    captured = capsysbinary.readouterr()
    assert captured.out == expected
    assert captured.err == (
        b"forgejo-actions-log-warning: running_log: snapshot\n"
    )


def test_main_leaves_stdout_empty_for_typed_errors(monkeypatch, capsys):
    monkeypatch.setenv("FORGEJO_TOKEN", "secret")

    def fail(*args, **kwargs):
        raise logs.ForgejoActionsLogError("expired_log", "gone")

    monkeypatch.setattr(logs, "fetch_logs", fail)

    assert logs.main(["coilyco-example", "repo", "1", "0"]) == 69

    captured = capsys.readouterr()
    assert captured.out == ""
    assert captured.err == "forgejo-actions-log-error: expired_log: gone\n"

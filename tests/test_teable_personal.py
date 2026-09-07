"""Each test pins the pinned-base contract or a confirmed Teable defect."""

from __future__ import annotations

import io
import json
import urllib.request
from collections.abc import Callable

import pytest

from agentic_os import teable_admin as admin
from agentic_os import teable_personal as personal

BASE = "bsepinned"
TABLE = "tblinside"
OUTSIDE = "tbloutside"


class FakeResponse(io.BytesIO):
    """Enough of an http response for urlopen's context-manager use."""

    def __enter__(self) -> "FakeResponse":
        return self

    def __exit__(self, *_: object) -> None:
        self.close()


def _json_response(payload: object) -> FakeResponse:
    return FakeResponse(json.dumps(payload).encode("utf-8"))


def _install(
    monkeypatch: pytest.MonkeyPatch,
    handler: Callable[[urllib.request.Request], FakeResponse],
) -> tuple[admin.TeableAPI, list[urllib.request.Request]]:
    calls: list[urllib.request.Request] = []

    def fake_urlopen(request: urllib.request.Request, **_: object) -> FakeResponse:
        calls.append(request)
        return handler(request)

    # TeableAPI is imported from teable_admin, so that module owns the socket.
    monkeypatch.setattr(admin.urllib.request, "urlopen", fake_urlopen)
    return admin.TeableAPI(base_url="http://teable.example/api", token="secret"), calls


def _pinned(api: admin.TeableAPI) -> personal.PinnedBase:
    pinned = personal.PinnedBase(api, BASE)
    pinned._tables = [{"id": TABLE, "name": "connections"}]
    return pinned


# --- the pin ----------------------------------------------------------------


def test_a_table_outside_the_pinned_base_is_refused_before_any_request(monkeypatch):
    """The pin is the whole reason this surface is separate from teable-admin."""
    api, calls = _install(monkeypatch, lambda request: _json_response({}))
    pinned = _pinned(api)

    with pytest.raises(personal.TeableError) as caught:
        pinned.check(OUTSIDE)

    assert caught.value.kind == "refused"
    assert OUTSIDE in str(caught.value)
    assert calls == [], "a refused table still reached the network"


def test_a_table_inside_the_pinned_base_passes_the_check(monkeypatch):
    api, _ = _install(monkeypatch, lambda request: _json_response({}))
    assert _pinned(api).check(TABLE) == TABLE


def test_the_base_comes_from_the_environment_and_not_from_the_caller(monkeypatch, capsys):
    """No verb takes a base argument, so a caller cannot point this elsewhere."""
    monkeypatch.setenv("TEABLE_API_TOKEN", "secret")
    monkeypatch.setenv("TEABLE_BASE_ID", BASE)
    api, calls = _install(monkeypatch, lambda request: _json_response({"id": BASE}))
    monkeypatch.setattr(personal, "TeableAPI", lambda *a, **k: api)

    assert personal.main(["list-base"]) == 0
    assert calls[0].full_url.endswith(f"/base/{BASE}")


def test_a_missing_base_id_is_a_typed_refusal_naming_its_ssm_path(monkeypatch, capsys):
    monkeypatch.setenv("TEABLE_API_TOKEN", "secret")
    monkeypatch.delenv("TEABLE_BASE_ID", raising=False)

    assert personal.main(["list-base"]) == admin.EXIT_CODES["invalid_identifier"]
    assert "connections-base-id" in capsys.readouterr().err


# --- create-record, against the silent-discard defect -----------------------


def test_create_record_refuses_when_a_requested_value_is_discarded(monkeypatch):
    def handler(request):
        if request.get_method() == "POST":
            return _json_response({"records": [{"id": "rec1", "fields": {"name": "Ada"}}]})
        return _json_response({"id": "rec1", "fields": {"name": "Ada"}})

    api, _ = _install(monkeypatch, handler)

    with pytest.raises(personal.TeableError) as caught:
        personal.create_record(api, TABLE, {"name": "Ada", "tier": "close"})

    assert caught.value.kind == "readback_mismatch"
    assert "tier" in str(caught.value)
    # There is no delete-record verb, so the refusal has to say the row stayed.
    assert "no delete-record verb" in str(caught.value)


def test_create_record_reads_back_through_a_separate_request(monkeypatch):
    """The create response is the thing under suspicion, so it is not the evidence."""

    def handler(request):
        if request.get_method() == "POST":
            # A create response that lies: it echoes what was asked for.
            return _json_response({"records": [{"id": "rec1", "fields": {"tier": "close"}}]})
        return _json_response({"id": "rec1", "fields": {}})

    api, calls = _install(monkeypatch, handler)

    with pytest.raises(personal.TeableError) as caught:
        personal.create_record(api, TABLE, {"tier": "close"})

    assert caught.value.kind == "readback_mismatch"
    assert [c.get_method() for c in calls] == ["POST", "GET"]


def test_create_record_succeeds_when_every_value_survives(monkeypatch):
    stored = {"id": "rec1", "fields": {"name": "Ada"}}

    def handler(request):
        if request.get_method() == "POST":
            return _json_response({"records": [stored]})
        return _json_response(stored)

    api, _ = _install(monkeypatch, handler)
    assert personal.create_record(api, TABLE, {"name": "Ada"}) == stored


def test_create_record_refuses_a_response_carrying_no_record_id(monkeypatch):
    api, _ = _install(monkeypatch, lambda request: _json_response({"records": [{"fields": {}}]}))

    with pytest.raises(personal.TeableError) as caught:
        personal.create_record(api, TABLE, {"name": "Ada"})

    assert caught.value.kind == "api_contract"


# --- edit-record ------------------------------------------------------------


def test_edit_record_refuses_when_the_update_did_not_store(monkeypatch):
    def handler(request):
        if request.get_method() == "PATCH":
            return _json_response({"id": "rec1", "fields": {"tier": "close"}})
        return _json_response({"id": "rec1", "fields": {"tier": "loose"}})

    api, calls = _install(monkeypatch, handler)

    with pytest.raises(personal.TeableError) as caught:
        personal.edit_record(api, TABLE, "rec1", {"tier": "close"})

    assert caught.value.kind == "readback_mismatch"
    assert [c.get_method() for c in calls] == ["PATCH", "GET"]


def test_edit_record_succeeds_when_the_update_survives(monkeypatch):
    stored = {"id": "rec1", "fields": {"tier": "close"}}
    api, _ = _install(monkeypatch, lambda request: _json_response(stored))
    assert personal.edit_record(api, TABLE, "rec1", {"tier": "close"}) == stored


def test_a_record_read_back_without_a_fields_object_is_a_contract_failure(monkeypatch):
    api, _ = _install(monkeypatch, lambda request: _json_response({"id": "rec1"}))

    with pytest.raises(personal.TeableError) as caught:
        personal.edit_record(api, TABLE, "rec1", {"tier": "close"})

    assert caught.value.kind == "api_contract"


# --- refusals ---------------------------------------------------------------


@pytest.mark.parametrize("verb", sorted(personal.REFUSALS))
def test_a_refused_verb_names_its_defect_and_reaches_no_upstream(verb, monkeypatch, capsys):
    """Absence reads as unimplemented as easily as refused, so each one speaks."""

    def explode(_request, **_kwargs):  # pragma: no cover - reaching this is the failure
        raise AssertionError("a refused verb reached the network")

    monkeypatch.setattr(admin.urllib.request, "urlopen", explode)
    monkeypatch.setenv("TEABLE_API_TOKEN", "secret")
    monkeypatch.setenv("TEABLE_BASE_ID", BASE)

    assert personal.main([verb]) == admin.EXIT_CODES["refused"]
    assert "NOT AVAILABLE" not in capsys.readouterr().out


def test_the_delete_refusal_points_at_the_reversible_substitute(monkeypatch, capsys):
    monkeypatch.setenv("TEABLE_API_TOKEN", "secret")
    monkeypatch.setenv("TEABLE_BASE_ID", BASE)
    personal.main(["delete-record"])
    assert "edit-record" in capsys.readouterr().err


def test_delete_record_is_not_a_guardfile_leaf() -> None:
    """Two walls: the module refuses it, and aosguard never grants it."""
    from pathlib import Path

    guardfile = Path(__file__).resolve().parents[1] / ".umbra" / "guardfiles"
    text = (guardfile / "aosguard" / "teable-personal.kdl").read_text(encoding="utf-8")
    assert "can run delete-record" not in text


# --- reads ------------------------------------------------------------------


def test_projection_travels_as_repeated_query_keys(monkeypatch):
    """Declared as an array; a single joined key is the defect that emptied reads."""
    api, calls = _install(monkeypatch, lambda request: _json_response({"records": []}))

    personal.list_records(api, TABLE, projection=["name", "org"])

    url = calls[0].full_url
    assert url.count("projection=") == 2
    assert "projection=name" in url and "projection=org" in url


def test_list_records_passes_paging_and_view_through(monkeypatch):
    api, calls = _install(monkeypatch, lambda request: _json_response({"records": []}))

    personal.list_records(api, TABLE, take=5, skip=10, view_id="viw1")

    url = calls[0].full_url
    assert "take=5" in url and "skip=10" in url and "viewId=viw1" in url


# --- credential handling ----------------------------------------------------


def test_a_missing_token_is_a_typed_refusal_rather_than_a_traceback(monkeypatch, capsys):
    monkeypatch.delenv("TEABLE_API_TOKEN", raising=False)
    monkeypatch.setenv("TEABLE_BASE_ID", BASE)

    assert personal.main(["list-base"]) == admin.EXIT_CODES["authorization_failure"]
    assert "TEABLE_API_TOKEN is required" in capsys.readouterr().err


def test_the_token_travels_as_a_bearer_header_never_in_the_url(monkeypatch):
    api, calls = _install(monkeypatch, lambda request: _json_response({"records": []}))
    personal.list_records(api, TABLE)
    assert calls[0].get_header("Authorization") == "Bearer secret"
    assert "secret" not in calls[0].full_url

"""Each test pins a confirmed Teable defect rather than a happy path."""

from __future__ import annotations

import io
import json
import urllib.error
import urllib.request
from collections.abc import Callable
from email.message import Message

import pytest

from agentic_os import teable_admin as admin

EXIT_AUTH = admin.EXIT_CODES["authorization_failure"]


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

    monkeypatch.setattr(admin.urllib.request, "urlopen", fake_urlopen)
    return admin.TeableAPI(base_url="http://teable.example/api", token="secret"), calls


# --- create-field, against the silent-discard defect ------------------------


def test_create_field_refuses_when_a_requested_property_is_discarded(monkeypatch):
    """POST /field accepts unknown properties and stores them nowhere."""
    stored = {"id": "fld1", "name": "priority", "type": "singleSelect"}

    def handler(request):
        if request.get_method() == "POST":
            return _json_response(stored)
        return _json_response([stored])

    api, _ = _install(monkeypatch, handler)

    with pytest.raises(admin.TeableAdminError) as caught:
        admin.create_field(
            api, "tbl1", {"name": "priority", "type": "singleSelect", "notNull": True}
        )

    assert caught.value.kind == "readback_mismatch"
    assert "notNull" in str(caught.value)
    # There is no delete-field verb, so the refusal has to say the field stayed.
    assert "no delete-field verb" in str(caught.value)


def test_create_field_succeeds_when_every_property_survives(monkeypatch):
    stored = {"id": "fld1", "name": "priority", "type": "singleSelect"}

    def handler(request):
        if request.get_method() == "POST":
            return _json_response(stored)
        return _json_response([stored])

    api, _ = _install(monkeypatch, handler)

    got = admin.create_field(api, "tbl1", {"name": "priority", "type": "singleSelect"})
    assert got == stored


def test_create_field_reads_back_through_a_separate_request(monkeypatch):
    """The create response is the thing under suspicion, so it is not the evidence."""
    def handler(request):
        if request.get_method() == "POST":
            # A create response that lies: it echoes what was asked for.
            return _json_response({"id": "fld1", "name": "priority", "notNull": True})
        # The stored truth disagrees.
        return _json_response([{"id": "fld1", "name": "priority"}])

    api, calls = _install(monkeypatch, handler)

    with pytest.raises(admin.TeableAdminError) as caught:
        admin.create_field(api, "tbl1", {"name": "priority", "notNull": True})

    assert caught.value.kind == "readback_mismatch"
    assert [c.get_method() for c in calls] == ["POST", "GET"]


def test_create_field_refuses_when_the_field_does_not_appear_at_all(monkeypatch):
    def handler(request):
        if request.get_method() == "POST":
            return _json_response({"id": "fld1"})
        return _json_response([])

    api, _ = _install(monkeypatch, handler)

    with pytest.raises(admin.TeableAdminError) as caught:
        admin.create_field(api, "tbl1", {"name": "priority"})

    assert caught.value.kind == "readback_mismatch"


def test_create_field_refuses_a_response_carrying_no_id(monkeypatch):
    api, _ = _install(monkeypatch, lambda request: _json_response({"ok": True}))

    with pytest.raises(admin.TeableAdminError) as caught:
        admin.create_field(api, "tbl1", {"name": "priority"})

    assert caught.value.kind == "api_contract"


# --- create-table -----------------------------------------------------------


def test_create_table_reads_back_and_ignores_the_nested_fields_key(monkeypatch):
    """`fields` is a creation payload, not a stored table property."""
    stored = {"id": "tbl9", "name": "platform"}

    def handler(request):
        if request.get_method() == "POST":
            return _json_response(stored)
        return _json_response([stored])

    api, _ = _install(monkeypatch, handler)

    got = admin.create_table(api, "baseone", {"name": "platform", "fields": [{"name": "x"}]})
    assert got == stored


# --- the diff itself --------------------------------------------------------


def test_mismatches_names_both_absent_and_differing_properties():
    problems = admin.mismatches(
        {"name": "a", "notNull": True, "type": "number"},
        {"name": "a", "type": "singleLineText"},
    )
    assert any("notNull" in p and "absent" in p for p in problems)
    assert any("type" in p and "singleLineText" in p for p in problems)
    assert not any(p.startswith("name") for p in problems)


# --- refusals ---------------------------------------------------------------


@pytest.mark.parametrize("verb", sorted(admin.REFUSALS))
def test_a_refused_verb_names_its_defect_and_reaches_no_upstream(verb, monkeypatch, capsys):
    """Absence reads as unimplemented as easily as refused, so each one speaks."""
    def explode(_request, **_kwargs):  # pragma: no cover - reaching this is the failure
        raise AssertionError("a refused verb reached the network")

    monkeypatch.setattr(admin.urllib.request, "urlopen", explode)
    monkeypatch.setenv("TEABLE_API_TOKEN", "secret")

    assert admin.main([verb]) == admin.EXIT_CODES["refused"]
    assert "NOT AVAILABLE" not in capsys.readouterr().out


def test_convert_refusal_cites_the_destroyed_values(monkeypatch, capsys):
    monkeypatch.setenv("TEABLE_API_TOKEN", "secret")
    admin.main(["convert-field"])
    assert "6,536" in capsys.readouterr().err


# --- credential handling ----------------------------------------------------


def test_a_missing_token_is_a_typed_refusal_rather_than_a_traceback(monkeypatch, capsys):
    monkeypatch.delenv("TEABLE_API_TOKEN", raising=False)
    assert admin.main(["list-fields", "tbl1"]) == EXIT_AUTH
    assert "TEABLE_API_TOKEN is required" in capsys.readouterr().err


def test_the_token_travels_as_a_bearer_header_never_in_the_url(monkeypatch):
    api, calls = _install(monkeypatch, lambda request: _json_response([]))
    api.list_fields("tbl1")
    assert calls[0].get_header("Authorization") == "Bearer secret"
    assert "secret" not in calls[0].full_url


# --- upstream failures ------------------------------------------------------


def test_an_http_error_surfaces_its_status_and_body(monkeypatch):
    def handler(request):
        raise urllib.error.HTTPError(
            request.full_url, 401, "fixture", Message(), io.BytesIO(b'{"message":"Unauthorized"}')
        )

    api, _ = _install(monkeypatch, handler)

    with pytest.raises(admin.TeableAdminError) as caught:
        api.list_fields("tbl1")

    assert caught.value.kind == "api_failure"
    assert "401" in str(caught.value)
    assert "Unauthorized" in str(caught.value)


def test_a_non_array_field_list_is_a_contract_failure(monkeypatch):
    api, _ = _install(monkeypatch, lambda request: _json_response({"not": "an array"}))

    with pytest.raises(admin.TeableAdminError) as caught:
        api.list_fields("tbl1")

    assert caught.value.kind == "api_contract"

"""Who can reach the gateway, and the error envelope for every failure."""

from __future__ import annotations

import re

import httpx
import pytest
from starlette.middleware.cors import CORSMiddleware

ROUTES = [
    ("GET", "/v1/bootstrap"),
    ("GET", "/v1/status"),
    ("GET", "/v1/models"),
    ("GET", "/v1/privacy"),
    ("POST", "/v1/chat/stream"),
    ("GET", "/v1/conversations"),
    ("DELETE", "/v1/conversations"),
    ("GET", "/v1/conversations/abc"),
    ("DELETE", "/v1/conversations/abc"),
    ("GET", "/v1/anything-else"),
]


def assert_envelope(response: httpx.Response, code: str) -> dict:
    body = response.json()
    assert set(body) == {"error"}
    assert set(body["error"]) == {"code", "message", "detail", "retryable"}
    assert body["error"]["code"] == code
    return body["error"]


def bare(gateway) -> httpx.Client:
    return httpx.Client(base_url=gateway.url, trust_env=False)


@pytest.mark.parametrize("method,path", ROUTES)
def test_every_v1_route_needs_a_token(make, method, path) -> None:
    gateway = make(provider="mock")
    with bare(gateway) as client:
        for headers in ({}, {"Authorization": "Bearer wrong"}, {"Authorization": "Basic abc"}):
            response = client.request(method, path, headers=headers, json={} if method == "POST" else None)
            assert response.status_code == 401
            assert_envelope(response, "auth_required")


def test_token_is_checked_before_the_body_is_read(make) -> None:
    gateway = make(provider="mock")
    with bare(gateway) as client:
        response = client.post(
            "/v1/chat/stream",
            content=b"{not json",
            headers={"Content-Type": "application/json"},
        )
    assert response.status_code == 401


def test_healthz_is_open_and_says_nothing_else(make) -> None:
    gateway = make(provider="mock")
    with bare(gateway) as client:
        response = client.get("/healthz")
    assert response.status_code == 200
    assert response.json() == {"status": "ok"}


@pytest.mark.parametrize(
    "header", [("Origin", "https://evil.example"), ("Origin", "null"), ("Sec-Fetch-Site", "cross-site")]
)
@pytest.mark.parametrize("path", ["/healthz", "/v1/bootstrap"])
def test_browser_requests_are_refused(make, header, path) -> None:
    gateway = make(provider="mock")
    response = gateway.client.get(path, headers=dict([header]))
    assert response.status_code == 403
    assert_envelope(response, "permission_denied")
    assert "access-control-allow-origin" not in response.headers


def test_cors_preflight_is_refused(make) -> None:
    gateway = make(provider="mock")
    response = gateway.client.options(
        "/v1/chat/stream",
        headers={"Origin": "https://evil.example", "Access-Control-Request-Method": "POST"},
    )
    assert response.status_code == 403


def test_no_cors_middleware_is_installed(make) -> None:
    gateway = make(provider="mock")
    assert not [item for item in gateway.app.user_middleware if item.cls is CORSMiddleware]


@pytest.mark.parametrize("value", ["text/plain", "application/x-www-form-urlencoded", "application/jsonp"])
def test_post_requires_json_content_type(make, value) -> None:
    gateway = make(provider="mock")
    response = gateway.client.post(
        "/v1/chat/stream", content=b'{"message":"hi"}', headers={"Content-Type": value}
    )
    assert response.status_code == 422
    assert_envelope(response, "validation_error")


def test_post_with_no_content_type_is_refused(make) -> None:
    gateway = make(provider="mock")
    response = gateway.client.post("/v1/chat/stream", content=b'{"message":"hi"}')
    assert response.status_code == 422
    assert_envelope(response, "validation_error")
    assert gateway.conversations() == []


def test_json_content_type_with_charset_is_accepted(make) -> None:
    gateway = make(provider="mock", overrides={"providers": {"mock": {"tokens_per_second": 5000}}})
    response = gateway.client.post(
        "/v1/chat/stream",
        content=b'{"message":"hi"}',
        headers={"Content-Type": "Application/JSON; charset=utf-8"},
    )
    assert response.status_code == 200


@pytest.mark.parametrize("host", ["evil.example", "127.0.0.1.evil.gov", "[::1]:8000", "attacker:8000"])
def test_bad_host_header_is_refused(make, host) -> None:
    gateway = make(provider="mock")
    response = gateway.client.get("/healthz", headers={"Host": host})
    assert response.status_code == 403
    assert_envelope(response, "permission_denied")


@pytest.mark.parametrize("host", ["127.0.0.1:8000", "localhost:8000", "LOCALHOST", "127.0.0.1"])
def test_listed_hosts_pass(make, host) -> None:
    gateway = make(provider="mock")
    assert gateway.client.get("/healthz", headers={"Host": host}).status_code == 200


def test_docs_are_closed_by_default(make) -> None:
    gateway = make(provider="mock")
    for path in ("/docs", "/redoc", "/openapi.json"):
        response = gateway.client.get(path)
        assert response.status_code == 404
        assert_envelope(response, "not_found")


def test_docs_can_be_opened(make) -> None:
    gateway = make(provider="mock", overrides={"gateway": {"docs_enabled": True}})
    assert gateway.client.get("/openapi.json").status_code == 200


def test_docs_pages_are_refused_to_a_browser(make) -> None:
    """As docs/development.md says: the schema is for curl. A browser sends
    Sec-Fetch-Site, even on a typed address, and is refused like any web page."""
    gateway = make(provider="mock", overrides={"gateway": {"docs_enabled": True}})
    assert gateway.client.get("/docs").status_code == 200
    for site in ("none", "same-origin"):
        for path in ("/docs", "/openapi.json"):
            response = gateway.client.get(path, headers={"Sec-Fetch-Site": site})
            assert response.status_code == 403
            assert_envelope(response, "permission_denied")


def test_request_id_is_generated_not_taken_from_the_client(make) -> None:
    gateway = make(provider="mock")
    first = gateway.client.get("/healthz", headers={"X-Request-ID": "forged"})
    second = gateway.client.get("/healthz")
    assert first.headers["x-request-id"] != "forged"
    assert first.headers["x-request-id"] != second.headers["x-request-id"]
    assert gateway.client.get("/nope").headers["x-request-id"]


def test_server_header_stays_out_of_refusals(make) -> None:
    gateway = make(provider="mock")
    assert gateway.client.get("/nope", headers={"Origin": "x"}).status_code == 403


def test_404_and_405_use_the_envelope(make) -> None:
    gateway = make(provider="mock")
    missing = gateway.client.get("/v1/nothing")
    assert missing.status_code == 404
    assert_envelope(missing, "not_found")
    unknown = gateway.client.get("/nothing")
    assert unknown.status_code == 404
    assert_envelope(unknown, "not_found")
    wrong = gateway.client.put("/v1/status")
    assert wrong.status_code == 405
    assert_envelope(wrong, "validation_error")
    assert "allow" in wrong.headers
    assert gateway.client.post("/healthz").status_code == 405


def test_trailing_slash_is_a_plain_404(make) -> None:
    gateway = make(provider="mock")
    response = gateway.client.get("/v1/status/", follow_redirects=False)
    assert response.status_code == 404


@pytest.mark.parametrize(
    "body",
    [
        b"{broken",
        b'{"message": 5}',
        b'{"tier": "root"}',
        b'{"assistant": "evil"}',
        b'{"attachments": "x"}',
        b'{"conversation_id": "a b"}',
        b"[]",
    ],
)
def test_validation_failures_use_the_envelope(make, body) -> None:
    gateway = make(provider="mock")
    response = gateway.client.post(
        "/v1/chat/stream", content=body, headers={"Content-Type": "application/json"}
    )
    assert response.status_code == 422
    assert_envelope(response, "validation_error")
    assert gateway.conversations() == []


@pytest.mark.parametrize("value", ["bad id", "a.b", "x" * 65])
def test_bad_path_identifier_is_422(make, value) -> None:
    gateway = make(provider="mock")
    response = gateway.client.get(f"/v1/conversations/{value}")
    assert response.status_code == 422
    assert_envelope(response, "validation_error")


@pytest.mark.parametrize("limit,status", [(0, 422), (501, 422), (500, 200), (1, 200)])
def test_conversation_limit_bounds(make, limit, status) -> None:
    gateway = make(provider="mock")
    assert gateway.client.get("/v1/conversations", params={"limit": limit}).status_code == status


def test_unexpected_failure_is_a_clean_500(make, monkeypatch) -> None:
    gateway = make(provider="mock")

    async def boom(*args, **kwargs):
        raise RuntimeError("private text must not escape")

    monkeypatch.setattr(gateway.services.store, "list_conversations", boom)
    response = gateway.client.get("/v1/conversations")
    assert response.status_code == 500
    error = assert_envelope(response, "internal")
    assert "private" not in response.text
    assert error["retryable"] is True


def test_oversized_body_is_refused_before_parsing(make) -> None:
    gateway = make(provider="mock")
    response = gateway.client.post(
        "/v1/chat/stream",
        content=b"x" * (8 * 1024 * 1024),
        headers={"Content-Type": "application/json"},
    )
    assert response.status_code == 413
    assert_envelope(response, "request_too_large")


def test_oversized_chunked_body_is_refused(make) -> None:
    gateway = make(provider="mock")

    def body():
        for _ in range(9):
            yield b"x" * (1024 * 1024)

    response = gateway.client.post(
        "/v1/chat/stream", content=body(), headers={"Content-Type": "application/json"}
    )
    assert response.status_code == 413


def test_detail_only_for_diagnostics_holders(make, fake) -> None:
    fake.tags = []
    allowed = make()
    assert allowed.post_chat().error["detail"]
    denied = make(
        overrides={
            "identity": {
                "user": {
                    "permissions": {
                        "advanced_mode": False,
                        "manual_model_selection": False,
                        "view_diagnostics": False,
                    }
                }
            }
        }
    )
    assert denied.post_chat().error["detail"] is None


def test_error_messages_never_name_a_location() -> None:
    from app.models.errors import CATALOG

    pattern = re.compile(r"this computer|localhost|127\.0\.0\.1|ollama", re.IGNORECASE)
    assert not [code for code, kind in CATALOG.items() if pattern.search(kind.message)]

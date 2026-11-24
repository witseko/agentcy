"""Check the access token gate and the option validation of the HTTP bridge."""

import asyncio
import os

import pytest

from agentcy.mcp.http_transport import (
    ACCESS_TOKEN_VARIABLE,
    BearerTokenGate,
    HttpSettings,
    read_settings,
    transport_security,
)

ACCESS_TOKEN = "a" * 32


def request(gate: BearerTokenGate, scope_type: str, authorization: bytes | None) -> list[dict]:
    """Send one request through the gate and return what the gate or the app answered."""
    sent: list[dict] = []

    async def receive() -> dict:
        return {"type": "http.request"}

    async def send(message: dict) -> None:
        sent.append(message)

    headers = [] if authorization is None else [(b"authorization", authorization)]
    asyncio.run(gate({"type": scope_type, "headers": headers}, receive, send))
    return sent


@pytest.fixture
def gate() -> BearerTokenGate:
    """Wrap an app that answers every request it receives with one marker message."""

    async def app(scope: dict, receive: object, send: object) -> None:
        await send({"type": "reached-app"})

    return BearerTokenGate(app, ACCESS_TOKEN)


@pytest.mark.parametrize(
    "authorization",
    [
        None,
        b"",
        b"Bearer",
        b"Bearer " + b"b" * 32,
        b"Bearer " + ACCESS_TOKEN.encode() + b"x",
        b"Basic " + ACCESS_TOKEN.encode(),
        ACCESS_TOKEN.encode(),
    ],
)
def test_request_without_the_access_token_gets_401(
    gate: BearerTokenGate, authorization: bytes | None
) -> None:
    """A missing or wrong credential never reaches the MCP app."""
    start, body = request(gate, "http", authorization)

    assert start["status"] == 401
    assert (b"www-authenticate", b"Bearer") in start["headers"]
    assert body == {"type": "http.response.body", "body": b"Unauthorized"}


@pytest.mark.parametrize("scheme", [b"Bearer", b"bearer"])
def test_request_with_the_access_token_reaches_the_app(
    gate: BearerTokenGate, scheme: bytes
) -> None:
    """The bearer scheme name is case-insensitive."""
    sent = request(gate, "http", scheme + b" " + ACCESS_TOKEN.encode())

    assert sent == [{"type": "reached-app"}]


def test_websocket_without_the_access_token_is_closed(gate: BearerTokenGate) -> None:
    """Other connection types also need the token."""
    assert request(gate, "websocket", None) == [{"type": "websocket.close", "code": 1008}]


def test_lifespan_events_pass_the_gate(gate: BearerTokenGate) -> None:
    """The server's own startup and shutdown events carry no credential."""
    assert request(gate, "lifespan", None) == [{"type": "reached-app"}]


def test_read_settings_removes_the_token_from_the_environment(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """The core and launched agents inherit the environment, so the token must leave it."""
    monkeypatch.setenv(ACCESS_TOKEN_VARIABLE, ACCESS_TOKEN)

    settings = read_settings(8765, "bridge.example.com")

    assert settings == HttpSettings(8765, ACCESS_TOKEN, "bridge.example.com")
    assert ACCESS_TOKEN_VARIABLE not in os.environ


@pytest.mark.parametrize("access_token", [None, "", "short", "é" * 32])
def test_read_settings_rejects_a_weak_access_token(
    monkeypatch: pytest.MonkeyPatch, access_token: str | None
) -> None:
    """The bridge does not start over HTTP without a long ASCII secret."""
    if access_token is None:
        monkeypatch.delenv(ACCESS_TOKEN_VARIABLE, raising=False)
    else:
        monkeypatch.setenv(ACCESS_TOKEN_VARIABLE, access_token)

    with pytest.raises(ValueError, match=ACCESS_TOKEN_VARIABLE):
        read_settings(8765, None)


@pytest.mark.parametrize(
    ("port", "public_host"),
    [
        (0, None),
        (65536, None),
        (8765, "https://bridge.example.com"),
        (8765, "bridge.example.com:443"),
        (8765, "bridge.example.com/mcp"),
        (8765, "*"),
        (8765, ""),
    ],
)
def test_read_settings_rejects_invalid_options(
    monkeypatch: pytest.MonkeyPatch, port: int, public_host: str | None
) -> None:
    """A bad port or host name stops the start and leaves the token in place."""
    monkeypatch.setenv(ACCESS_TOKEN_VARIABLE, ACCESS_TOKEN)

    with pytest.raises(ValueError, match="--http"):
        read_settings(port, public_host)


def test_transport_security_allows_only_loopback_and_the_tunnel_host() -> None:
    """Host header checks stay on, and the tunnel host name is the only public name."""
    local = transport_security(HttpSettings(8765, ACCESS_TOKEN))
    tunnel = transport_security(HttpSettings(8765, ACCESS_TOKEN, "bridge.example.com"))

    assert local.enable_dns_rebinding_protection
    assert local.allowed_hosts == ["127.0.0.1:*", "localhost:*"]
    assert tunnel.allowed_hosts == [
        "127.0.0.1:*",
        "localhost:*",
        "bridge.example.com",
        "bridge.example.com:*",
    ]
    assert "https://bridge.example.com" in tunnel.allowed_origins

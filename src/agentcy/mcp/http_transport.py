"""Serve the MCP tools over HTTP on the loopback interface for a remote chat behind a tunnel."""

import hmac
import os
import re
from collections.abc import Awaitable, Callable, MutableMapping
from dataclasses import dataclass
from typing import Any

from mcp.server.fastmcp import FastMCP
from mcp.server.transport_security import TransportSecuritySettings

ACCESS_TOKEN_VARIABLE = "AGENTCY_HTTP_TOKEN"  # noqa: S105 - variable name, not a secret
MIN_ACCESS_TOKEN_LENGTH = 32
LOOPBACK_HOST = "127.0.0.1"
PUBLIC_HOST_PATTERN = re.compile(r"[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?")
UNAUTHORIZED_BODY = b"Unauthorized"

Scope = MutableMapping[str, Any]
Message = MutableMapping[str, Any]
Receive = Callable[[], Awaitable[Message]]
Send = Callable[[Message], Awaitable[None]]
AsgiApp = Callable[[Scope, Receive, Send], Awaitable[None]]


@dataclass(frozen=True)
class HttpSettings:
    """Hold the port, the access token, and the tunnel host name of one HTTP bridge."""

    port: int
    access_token: str
    public_host: str | None = None


def read_settings(port: int, public_host: str | None) -> HttpSettings:
    """Validate the HTTP options and take the access token out of the environment.

    Raises ValueError with a message for the user when an option is not valid.
    """
    if not 1 <= port <= 65535:
        raise ValueError("--http needs a port from 1 to 65535")
    if public_host is not None and not PUBLIC_HOST_PATTERN.fullmatch(public_host):
        raise ValueError("--http-public-host needs a host name without scheme, port, or path")
    # The core and the launched agents inherit this environment, so the token leaves it here.
    access_token = os.environ.pop(ACCESS_TOKEN_VARIABLE, "")
    if len(access_token) < MIN_ACCESS_TOKEN_LENGTH or not access_token.isascii():
        raise ValueError(
            f"Set {ACCESS_TOKEN_VARIABLE} to a random ASCII secret of at least "
            f"{MIN_ACCESS_TOKEN_LENGTH} characters, for example from 'openssl rand -hex 32'"
        )
    return HttpSettings(port=port, access_token=access_token, public_host=public_host)


def transport_security(settings: HttpSettings) -> TransportSecuritySettings:
    """Accept the loopback names and the tunnel host name in Host and Origin headers."""
    hosts = [f"{LOOPBACK_HOST}:*", "localhost:*"]
    origins = [f"http://{LOOPBACK_HOST}:*", "http://localhost:*"]
    if settings.public_host:
        hosts += [settings.public_host, f"{settings.public_host}:*"]
        origins.append(f"https://{settings.public_host}")
    return TransportSecuritySettings(
        enable_dns_rebinding_protection=True, allowed_hosts=hosts, allowed_origins=origins
    )


class BearerTokenGate:
    """Pass a request to the wrapped ASGI app only when it carries the access token."""

    def __init__(self, app: AsgiApp, access_token: str) -> None:
        self.app = app
        self.access_token = access_token.encode("ascii")

    async def __call__(self, scope: Scope, receive: Receive, send: Send) -> None:
        """Answer 401 to an HTTP request without the token and close such a WebSocket."""
        if scope["type"] == "lifespan" or self.authorized(scope):
            await self.app(scope, receive, send)
        elif scope["type"] == "http":
            await send(
                {
                    "type": "http.response.start",
                    "status": 401,
                    "headers": [
                        (b"www-authenticate", b"Bearer"),
                        (b"content-type", b"text/plain; charset=utf-8"),
                        (b"content-length", str(len(UNAUTHORIZED_BODY)).encode("ascii")),
                    ],
                }
            )
            await send({"type": "http.response.body", "body": UNAUTHORIZED_BODY})
        else:
            await send({"type": "websocket.close", "code": 1008})

    def authorized(self, scope: Scope) -> bool:
        """Compare the bearer credential with the access token in constant time."""
        header = dict(scope.get("headers", [])).get(b"authorization", b"")
        scheme, _, credential = header.partition(b" ")
        return scheme.lower() == b"bearer" and hmac.compare_digest(
            credential.strip(), self.access_token
        )


def run(server: FastMCP, settings: HttpSettings) -> None:
    """Serve the tools at /mcp on the loopback interface until the process stops."""
    import uvicorn

    app = BearerTokenGate(server.streamable_http_app(), settings.access_token)
    uvicorn.run(app, host=LOOPBACK_HOST, port=settings.port, log_level="warning")

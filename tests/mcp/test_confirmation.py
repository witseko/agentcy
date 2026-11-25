"""Check that close asks the human to confirm only when the client can ask, and retire never does."""

import asyncio
import json
from collections.abc import Awaitable, Callable
from typing import Any

import pytest
from mcp import Client
from mcp.shared.exceptions import MCPError
from mcp.types import INVALID_PARAMS, ElicitRequestParams, ElicitResult, Request
from pydantic import TypeAdapter
from sqlalchemy import Connection

from agentcy.conversations import models, repository
from agentcy.mcp import fresh_server, server

RAW_RESULT = TypeAdapter(dict[str, Any])
MODES = ["legacy", "2026-07-28"]
DECLINED = (
    "The user declined this action, so nothing changed. Do not retry it unless the user asks again."
)
CANCELLED = DECLINED.replace("declined", "cancelled")
# Each refusal leaves the target as it was and returns this tool error text.
REFUSALS = [
    (ElicitResult(action="accept", content={"confirm": False}), DECLINED),
    (ElicitResult(action="decline"), DECLINED),
    (ElicitResult(action="cancel"), CANCELLED),
]
Callback = Callable[[Any, ElicitRequestParams], Awaitable[ElicitResult]]


@pytest.fixture
def app(conversation_db: Connection, monkeypatch: pytest.MonkeyPatch) -> server.BridgeServer:
    """Build the main server on the test database without starting its stdio loop."""
    built: list[server.BridgeServer] = []
    monkeypatch.setattr(
        server, "exchange", lambda request: repository.apply(conversation_db, request)
    )
    monkeypatch.setattr(server.BridgeServer, "run", lambda app, transport: built.append(app))
    server.serve(conversation_db.info["workspace"])
    return built[0]


def answering(answer: ElicitResult, questions: list[ElicitRequestParams]) -> Callback:
    """Return an elicitation callback that records each question and gives one answer."""

    async def callback(context: Any, params: ElicitRequestParams) -> ElicitResult:
        questions.append(params)
        return answer

    return callback


def run_client(
    app: server.BridgeServer,
    mode: str,
    action: Callable[[Client], Awaitable[Any]],
    callback: Callback | None = None,
) -> Any:
    """Connect an in-process client, declaring elicitation only when a callback is given."""

    async def connect() -> Any:
        async with Client(app, mode=mode, elicitation_callback=callback) as client:
            return await action(client)

    return asyncio.run(connect())


async def send(client: Client, method: str, params: dict[str, Any]) -> dict[str, Any]:
    """Send one request and return the raw result, which may ask for input."""
    request = Request[dict[str, Any], str](method=method, params=params)
    return await client.session.send_request(request, RAW_RESULT)


async def call(client: Client, tool: str, **arguments: Any) -> dict[str, Any]:
    """Call a tool, answer any elicitation through the client callback, and return JSON."""
    result = await client.call_tool(tool, arguments)
    return result.model_dump(mode="json", by_alias=True, exclude_none=True)


async def team(client: Client) -> tuple[str, str]:
    """Create a conversation whose worker Scout joins, and return the lead and Scout tokens."""
    created = text(
        await call(
            client, "create_conversation", brief="Review", your_name="Lead", invitees=["Scout"]
        )
    )
    joined = text(await call(client, "join_conversation", code=created["invitations"]["Scout"]))
    return created["token"], joined["token"]


def text(result: dict[str, Any]) -> dict[str, Any]:
    """Decode the JSON text block of a successful tool result."""
    assert not result.get("isError"), result
    (content,) = result["content"]
    return json.loads(content["text"])


def error_text(result: dict[str, Any]) -> str:
    """Return the text of a failed tool result."""
    assert result["isError"] is True, result
    (content,) = result["content"]
    return content["text"]


async def state(client: Client, token: str) -> dict[str, Any]:
    """Return whether the conversation is open and Scout's membership state."""
    status = text(await call(client, "conversation_status", token=token))
    return {"open": status["open"], "scout": status["members"]["Scout"]["state"]}


@pytest.mark.parametrize("mode", MODES)
def test_accept_closes_once(app: server.BridgeServer, mode: str) -> None:
    """An accepted confirmation closes the conversation once and returns today's result."""
    questions: list[ElicitRequestParams] = []

    async def action(client: Client) -> tuple[dict, dict]:
        lead, _ = await team(client)
        closed = text(await call(client, "close_conversation", token=lead, summary="Done"))
        return closed, await state(client, lead)

    accept = ElicitResult(action="accept", content={"confirm": True})
    closed, after = run_client(app, mode, action, answering(accept, questions))
    assert closed == {"closed": True}
    assert after == {"open": False, "scout": "joined"}
    assert [question.message for question in questions] == ["Close this conversation?"]
    schema = questions[0].model_dump(by_alias=True)["requestedSchema"]
    assert schema["required"] == ["confirm"]
    assert schema["properties"]["confirm"]["type"] == "boolean"


@pytest.mark.parametrize("mode", MODES)
@pytest.mark.parametrize(("answer", "expected"), REFUSALS)
def test_refusal_changes_nothing(
    app: server.BridgeServer, mode: str, answer: ElicitResult, expected: str
) -> None:
    """A false confirm, a decline, or a cancel keeps the conversation open."""
    questions: list[ElicitRequestParams] = []

    async def action(client: Client) -> tuple[dict, dict]:
        lead, _ = await team(client)
        closed = await call(client, "close_conversation", token=lead)
        return closed, await state(client, lead)

    closed, after = run_client(app, mode, action, answering(answer, questions))
    assert error_text(closed) == f"Error executing tool close_conversation: {expected}"
    assert after == {"open": True, "scout": "joined"}
    assert len(questions) == 1


@pytest.mark.parametrize("mode", MODES)
def test_client_without_elicitation_is_not_asked(app: server.BridgeServer, mode: str) -> None:
    """A client that does not declare elicitation gets today's results with no question.

    open_invite has no confirmation, so its result shows the shape of an unchanged tool.
    """

    async def action(client: Client) -> tuple[dict, dict]:
        lead, _ = await team(client)
        unchanged = await call(client, "open_invite", token=lead, enabled=False)
        closed = await call(client, "close_conversation", token=lead)
        return unchanged, closed

    unchanged, closed = run_client(app, mode, action)
    assert text(closed) == {"closed": True}
    payload = {"content", "structuredContent"}
    assert closed.keys() == unchanged.keys()
    assert {key: closed[key] for key in closed.keys() - payload} == {
        key: unchanged[key] for key in unchanged.keys() - payload
    }


@pytest.mark.parametrize("mode", MODES)
def test_retire_is_never_asked(app: server.BridgeServer, mode: str) -> None:
    """A client that declares elicitation retires a participant with no question."""
    questions: list[ElicitRequestParams] = []

    async def action(client: Client) -> tuple[dict, dict]:
        lead, _ = await team(client)
        retired = text(await call(client, "retire_participant", token=lead, name="Scout"))
        return retired, await state(client, lead)

    accept = ElicitResult(action="accept", content={"confirm": True})
    retired, after = run_client(app, mode, action, answering(accept, questions))
    assert retired == {"participant": "Scout", "retired": True}
    assert after == {"open": True, "scout": "retired"}
    assert questions == []


@pytest.mark.parametrize("mode", MODES)
def test_invalid_arguments_fail_before_the_question(app: server.BridgeServer, mode: str) -> None:
    """A malformed token fails argument validation without asking the human."""
    questions: list[ElicitRequestParams] = []

    async def action(client: Client) -> dict:
        return await call(client, "close_conversation", token="")

    accept = ElicitResult(action="accept", content={"confirm": True})
    result = run_client(app, mode, action, answering(accept, questions))
    assert "validation error" in error_text(result)
    assert questions == []


def test_input_schemas_gain_no_parameter(app: server.BridgeServer) -> None:
    """The hidden confirmation parameter stays out of both tool input schemas."""

    async def action(client: Client) -> dict[str, dict]:
        listed = await client.list_tools()
        return {tool.name: tool.input_schema for tool in listed.tools}

    schemas = run_client(app, "2026-07-28", action)
    assert set(schemas["close_conversation"]["properties"]) == {"token", "summary"}
    assert schemas["close_conversation"]["required"] == ["token"]
    assert set(schemas["retire_participant"]["properties"]) == {"token", "name"}
    assert schemas["retire_participant"]["required"] == ["token", "name"]


def test_retry_cannot_change_the_arguments(app: server.BridgeServer) -> None:
    """The sealed request state binds the answer to the arguments the human saw."""
    accept = {"action": "accept", "content": {"confirm": True}}

    async def action(client: Client) -> tuple[dict, str, dict, dict]:
        lead, _ = await team(client)
        asked = await send(
            client,
            "tools/call",
            {"name": "close_conversation", "arguments": {"token": lead, "summary": "Seen"}},
        )
        (key,) = asked["inputRequests"]
        responses = {key: accept}
        with pytest.raises(MCPError) as switched:
            await send(
                client,
                "tools/call",
                {
                    "name": "close_conversation",
                    "arguments": {"token": lead, "summary": "Changed"},
                    "inputResponses": responses,
                    "requestState": asked["requestState"],
                },
            )
        assert switched.value.error.code == INVALID_PARAMS
        # Without the sealed state the answer is not trusted, so the question comes again.
        unsealed = await send(
            client,
            "tools/call",
            {
                "name": "close_conversation",
                "arguments": {"token": lead, "summary": "Changed"},
                "inputResponses": responses,
            },
        )
        return asked, switched.value.error.message, unsealed, await state(client, lead)

    asked, message, unsealed, after = run_client(
        app, "2026-07-28", action, answering(ElicitResult(action="decline"), [])
    )
    assert asked["resultType"] == "input_required"
    assert message == "Invalid or expired requestState"
    assert unsealed["resultType"] == "input_required"
    (question,) = unsealed["inputRequests"].values()
    assert question["params"]["message"] == "Close this conversation?"
    assert after["open"] is True


def test_fresh_worker_never_asks(
    conversation_db: Connection, monkeypatch: pytest.MonkeyPatch
) -> None:
    """The fresh-worker server has no close or retire tool and asks no question."""
    created = repository.apply(
        conversation_db,
        models.Create(
            workspace=conversation_db.info["workspace"],
            brief="Review",
            initiator="Lead",
            invitees=["Fresh"],
        ),
    )
    built: list[server.BridgeServer] = []
    monkeypatch.setattr(
        fresh_server, "exchange", lambda request: repository.apply(conversation_db, request)
    )
    monkeypatch.setattr(server.BridgeServer, "run", lambda app, transport: built.append(app))
    fresh_server.serve(conversation_db.info["workspace"], created["invitations"]["Fresh"])
    questions: list[ElicitRequestParams] = []

    async def action(client: Client) -> tuple[set[str], dict]:
        names = {tool.name for tool in (await client.list_tools()).tools}
        await call(client, "join_conversation")
        return names, text(await call(client, "conversation_status"))

    accept = ElicitResult(action="accept", content={"confirm": True})
    names, status = run_client(built[0], "2026-07-28", action, answering(accept, questions))
    assert names.isdisjoint({"close_conversation", "retire_participant"})
    assert status["open"] is True
    assert questions == []

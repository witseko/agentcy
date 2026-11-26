"""Confirm or refuse closing a conversation through the stdio shim in both MCP protocol eras.

A legacy (2025-11-25) client gets the question as an `elicitation/create` server request
during the `tools/call`. A 2026-07-28 client gets an `input_required` result and answers
by repeating the `tools/call` with its responses and the sealed request state.
"""

import asyncio
import json
from pathlib import Path
from typing import Any

import pytest
from mcp import Client
from mcp.client.session import ClientRequestContext, ElicitationFnT
from mcp.types import ElicitRequestParams, ElicitResult, Request, TextContent
from pydantic import TypeAdapter

# pytest registers the imported fixture for this module.
from test_protocol_eras import EXPECTED_VERSION, call, connect_stdio
from test_stdio_conversation import bridge_home  # noqa: F401

MODES = ["legacy", "auto"]
QUESTION = "Close this conversation?"
DECLINED = (
    "Error executing tool close_conversation: The user declined this action, so nothing "
    "changed. Do not retry it unless the user asks again."
)
CANCELLED = DECLINED.replace("declined", "cancelled")
ACCEPT = ElicitResult(action="accept", content={"confirm": True})
# Each refusal leaves the conversation open and returns this tool error text.
REFUSALS = {
    "accept-false": (ElicitResult(action="accept", content={"confirm": False}), DECLINED),
    "decline": (ElicitResult(action="decline"), DECLINED),
    "cancel": (ElicitResult(action="cancel"), CANCELLED),
}
RAW_RESULT = TypeAdapter(dict[str, Any])


def answering(answer: ElicitResult, questions: list[ElicitRequestParams]) -> ElicitationFnT:
    """Return an elicitation callback that records each question and gives one answer."""

    async def callback(context: ClientRequestContext, params: ElicitRequestParams) -> ElicitResult:
        questions.append(params)
        return answer

    return callback


async def close(client: Client, token: str) -> tuple[bool, str]:
    """Call close_conversation and return the error flag and the single text block."""
    result = await client.call_tool("close_conversation", {"token": token})
    (content,) = result.content
    assert isinstance(content, TextContent)
    return result.is_error, content.text


async def lead_token(client: Client, mode: str) -> str:
    """Check the negotiated era, then create a conversation and return the lead's token."""
    assert client.protocol_version == EXPECTED_VERSION[mode]
    created = await call(
        client, "create_conversation", brief="Close", your_name="Lead", invitees=["W"]
    )
    return created["token"]


async def is_open(client: Client, token: str) -> bool:
    """Return whether the core reports the conversation as open."""
    status = await call(client, "conversation_status", token=token)
    return status["open"]


async def send(client: Client, method: str, params: dict[str, Any]) -> dict[str, Any]:
    """Send one request and return the raw result, which may ask for input."""
    request = Request[dict[str, Any], str](method=method, params=params)
    return await client.session.send_request(request, RAW_RESULT)


@pytest.mark.parametrize("mode", MODES)
def test_accept_closes_the_conversation(
    bridge_home: Path,  # noqa: F811 - pytest fixture argument
    mode: str,
) -> None:
    """An accepted confirmation closes the conversation after one question."""
    questions: list[ElicitRequestParams] = []

    async def run() -> tuple[bool, str, bool]:
        async with connect_stdio(bridge_home, mode, answering(ACCEPT, questions)) as client:
            token = await lead_token(client, mode)
            is_error, text = await close(client, token)
            return is_error, text, await is_open(client, token)

    is_error, text, still_open = asyncio.run(run())
    assert (is_error, json.loads(text), still_open) == (False, {"closed": True}, False)
    assert [question.message for question in questions] == [QUESTION]
    schema = questions[0].model_dump(by_alias=True)["requestedSchema"]
    assert schema["required"] == ["confirm"]
    assert schema["properties"]["confirm"]["type"] == "boolean"


@pytest.mark.parametrize("mode", MODES)
@pytest.mark.parametrize(("answer", "expected"), REFUSALS.values(), ids=REFUSALS.keys())
def test_refusal_keeps_the_conversation_open(
    bridge_home: Path,  # noqa: F811 - pytest fixture argument
    mode: str,
    answer: ElicitResult,
    expected: str,
) -> None:
    """A false confirm, a decline, or a cancel returns a tool error and changes nothing."""
    questions: list[ElicitRequestParams] = []

    async def run() -> tuple[bool, str, bool]:
        async with connect_stdio(bridge_home, mode, answering(answer, questions)) as client:
            token = await lead_token(client, mode)
            is_error, text = await close(client, token)
            return is_error, text, await is_open(client, token)

    assert asyncio.run(run()) == (True, expected, True)
    assert [question.message for question in questions] == [QUESTION]


@pytest.mark.parametrize("mode", MODES)
def test_client_without_elicitation_closes_without_a_question(
    bridge_home: Path,  # noqa: F811 - pytest fixture argument
    mode: str,
) -> None:
    """A client that declares no elicitation closes the conversation directly."""

    async def run() -> tuple[bool, str, bool]:
        async with connect_stdio(bridge_home, mode) as client:
            token = await lead_token(client, mode)
            is_error, text = await close(client, token)
            return is_error, text, await is_open(client, token)

    is_error, text, still_open = asyncio.run(run())
    assert (is_error, json.loads(text), still_open) == (False, {"closed": True}, False)


def test_legacy_client_answers_a_server_request_inside_the_call(
    bridge_home: Path,  # noqa: F811 - pytest fixture argument
) -> None:
    """A legacy client answers the question during one tools/call that returns the result."""
    questions: list[ElicitRequestParams] = []

    async def run() -> dict[str, Any]:
        async with connect_stdio(bridge_home, "legacy", answering(ACCEPT, questions)) as client:
            token = await lead_token(client, "legacy")
            arguments = {"token": token}
            return await send(
                client, "tools/call", {"name": "close_conversation", "arguments": arguments}
            )

    result = asyncio.run(run())
    assert "resultType" not in result
    assert json.loads(result["content"][0]["text"]) == {"closed": True}
    assert [question.message for question in questions] == [QUESTION]


def test_modern_client_gets_input_required_and_repeats_the_call(
    bridge_home: Path,  # noqa: F811 - pytest fixture argument
) -> None:
    """A 2026-07-28 client gets the question as a result and answers it in a second call."""
    questions: list[ElicitRequestParams] = []

    async def run() -> tuple[dict[str, Any], dict[str, Any], bool]:
        async with connect_stdio(bridge_home, "auto", answering(ACCEPT, questions)) as client:
            token = await lead_token(client, "auto")
            params = {"name": "close_conversation", "arguments": {"token": token}}
            asked = await send(client, "tools/call", params)
            (key,) = asked["inputRequests"]
            answered = await send(
                client,
                "tools/call",
                {
                    **params,
                    "inputResponses": {key: ACCEPT.model_dump(mode="json", exclude_none=True)},
                    "requestState": asked["requestState"],
                },
            )
            return asked, answered, await is_open(client, token)

    asked, answered, still_open = asyncio.run(run())
    assert asked["resultType"] == "input_required"
    (question,) = asked["inputRequests"].values()
    assert question["method"] == "elicitation/create"
    assert question["params"]["message"] == QUESTION
    assert json.loads(answered["content"][0]["text"]) == {"closed": True}
    assert still_open is False
    # In this era no elicitation/create server request reaches the client callback.
    assert questions == []

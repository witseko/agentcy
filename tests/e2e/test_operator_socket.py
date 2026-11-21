"""Drive a conversation from the operator protocol while a stdio MCP client leads it."""

import asyncio
import json
import socket
import stat
from pathlib import Path
from typing import Any

# pytest registers the imported bridge_home fixture for this module.
from test_stdio_conversation import bridge_home, call, connect  # noqa: F401

BRIEF = "Check the retry behavior"
REQUEST_TO_LEAD = "Start with the retry tests."
SUMMARY = "The human closed the review."


def operate(home: Path, action: str, **fields: Any) -> dict:
    """Send one authorised operator request over the core's socket."""
    key = (home / "operator.key").read_text(encoding="ascii")
    payload = {"version": 1, "key": key, "action": action, **fields}
    with socket.socket(socket.AF_UNIX) as client:
        client.settimeout(10)
        client.connect(str(home / "core.sock"))
        client.sendall(json.dumps(payload).encode() + b"\n")
        with client.makefile("rb") as reader:
            response = json.loads(reader.readline())
    assert "error" not in response, response
    return response["result"]


async def lead_from_a_chat(home: Path, workspace: Path) -> None:
    """Create as the human, join the lead slot over MCP, then post, list, show and close."""
    directory = str(workspace)
    created = operate(
        home,
        "operator_create",
        directory=directory,
        brief=BRIEF,
        lead="Lead",
        invitees=["Worker"],
    )
    conversation = created["conversation"]
    lead_slot = created["invitations"][0]
    assert lead_slot["participant"] == "Lead"
    assert lead_slot["code"] in lead_slot["join_prompt"]
    async with connect(home, workspace) as lead:
        joined = await call(lead, "join_conversation", code=lead_slot["code"])
        assert joined["role"] == "lead"
        assert joined["brief"] == BRIEF
        assert joined["roster"]["Human"] == "human"
        token = joined["token"]

        posted = operate(
            home,
            "operator_post",
            directory=directory,
            conversation=conversation,
            submission_id="ui-1",
            body=REQUEST_TO_LEAD,
            to="Lead",
        )
        page = await call(lead, "read_messages", token=token, wait_seconds=2)
        assert page["messages"] == [
            {"id": posted["message_id"], "label": "Human", "body": REQUEST_TO_LEAD, "to": "Lead"}
        ]

        listed = operate(home, "operator_list", directory=directory)["conversations"]
        assert [(item["conversation"], item["title"], item["joined"]) for item in listed] == [
            (conversation, BRIEF, 1)
        ]
        view = operate(home, "operator_show", directory=directory, conversation=conversation)
        assert view["status"]["members"]["Lead"]["state"] == "joined"
        assert view["status"]["members"]["Human"] == {"role": "human", "state": "human"}
        assert [message["body"] for message in view["messages"]] == [REQUEST_TO_LEAD]

        closed = operate(
            home,
            "operator_close",
            directory=directory,
            conversation=conversation,
            summary=SUMMARY,
        )
        assert closed == {"closed": True}
        page = await call(lead, "read_messages", token=token, after=page["cursor"], wait_seconds=2)
        assert page["closed"] is True
        assert [(message["label"], message["body"]) for message in page["messages"]] == [
            ("Human", SUMMARY)
        ]


def test_operator_drives_a_conversation_with_a_chat_lead(bridge_home: Path) -> None:  # noqa: F811
    """A real core serves the operator protocol and the stdio shim together."""
    workspace = bridge_home / "workspace"
    workspace.mkdir()
    asyncio.run(lead_from_a_chat(bridge_home, workspace))
    key_file = bridge_home / "operator.key"
    assert stat.S_IMODE(key_file.stat().st_mode) == 0o600

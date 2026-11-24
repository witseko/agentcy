"""Stand in for the chats of a video scene: post scripted messages through the real bridge.

    stand_in.py --workspace DIR --scene SCENE.json --state STATE.json create
    stand_in.py --workspace DIR --scene SCENE.json --state STATE.json join NAME [CODE]
    stand_in.py --workspace DIR --scene SCENE.json --state STATE.json post CUE

Each call starts the bridge's stdio shim (``agentcy`` next to this Python)
the way a desktop app or CLI does, so the core, the database and the
terminal UI are real; only the model text comes from the scene file.
Run it with the project's ``.venv/bin/python`` and AGENTCY_HOME set to the
take's private bridge folder.

The scene file is JSON:

    {
      "brief": "Find out whether a retry after a timeout can submit twice.",
      "lead": "Lead",
      "invitees": ["Timeout", "Fixtures"],
      "joined": ["Timeout", "Fixtures"],
      "models": {"Lead": "claude-opus-5-5"},
      "cues": {
        "assign": {"as": "Lead", "kind": "task", "to": "Timeout",
                   "due_minutes": 10, "body": "Trace the timeout path."},
        "done": {"as": "Timeout", "kind": "done", "reply_to": "assign",
                 "body": "The retry reuses the request ID: client.py:42."}
      }
    }

``create`` starts the conversation as ``lead`` and joins everyone in
``joined``. ``join NAME CODE`` joins with a code from the screen (for a
chat the terminal UI created); without CODE it uses the code ``create``
stored. ``post CUE`` posts one cue as its participant; ``reply_to`` names an
earlier cue. The state file keeps participant tokens between calls, so it
is created with mode 0600 and belongs in the take's private work folder.
"""

import argparse
import asyncio
import json
import os
import sys
from contextlib import asynccontextmanager
from datetime import timedelta
from pathlib import Path
from typing import Any

from mcp import ClientSession, StdioServerParameters
from mcp.client.stdio import stdio_client
from mcp.types import TextContent

TIMEOUT_SECONDS = 15
MESSAGE_FIELDS = ("kind", "to", "due_minutes")


@asynccontextmanager
async def connect(workspace: Path, log: Path):
    """Start one bridge shim for ``workspace`` and yield an initialized MCP session.

    The shim's own log lines go to ``log``, so they do not mix with the take's output.
    """
    shim = Path(sys.executable).parent / "agentcy"
    parameters = StdioServerParameters(
        command=str(shim), args=["--workspace", str(workspace)], env=dict(os.environ), cwd=workspace
    )
    timeout = timedelta(seconds=TIMEOUT_SECONDS)
    with log.open("a") as errors:
        async with (
            stdio_client(parameters, errlog=errors) as (reader, writer),
            ClientSession(reader, writer, read_timeout_seconds=timeout) as session,
        ):
            await session.initialize()
            yield session


async def call(session: ClientSession, tool: str, **arguments: Any) -> dict[str, Any]:
    """Call a bridge tool and decode its JSON result; stop on a tool error."""
    result = await session.call_tool(tool, arguments)
    text = "".join(part.text for part in result.content if isinstance(part, TextContent))
    if result.isError:
        raise SystemExit(f"{tool} failed: {text}")
    return json.loads(text)


def load_state(path: Path) -> dict[str, Any]:
    """Read the tokens, codes and message IDs of earlier calls."""
    if not path.exists():
        return {"tokens": {}, "codes": {}, "messages": {}}
    return json.loads(path.read_text())


def save_state(path: Path, state: dict[str, Any]) -> None:
    """Write the state file readable by the owner only, since it holds tokens."""
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(descriptor, "w") as file:
        json.dump(state, file, indent=2)


async def join(
    session: ClientSession, scene: dict[str, Any], state: dict[str, Any], name: str, code: str
) -> None:
    """Redeem ``code`` as ``name`` and keep the participant token."""
    model = scene.get("models", {}).get(name)
    arguments = {"code": code, "name": name} | ({"model": model} if model else {})
    joined = await call(session, "join_conversation", **arguments)
    state["tokens"][name] = joined["token"]


async def create(session: ClientSession, scene: dict[str, Any], state: dict[str, Any]) -> None:
    """Start the scene's conversation as its lead, then join the listed participants."""
    lead = scene["lead"]
    model = scene.get("models", {}).get(lead)
    created = await call(
        session,
        "create_conversation",
        brief=scene["brief"],
        your_name=lead,
        invitees=scene.get("invitees", []),
        **({"your_model": model} if model else {}),
    )
    state["conversation"] = created["conversation"]
    state["tokens"][lead] = created["token"]
    state["codes"] = created["invitations"]
    for name in scene.get("joined", []):
        await join(session, scene, state, name, state["codes"][name])


def check_cue(scene: dict[str, Any], state: dict[str, Any], cue_name: str) -> None:
    """Stop before any bridge call when the cue cannot be posted yet."""
    cue = scene["cues"].get(cue_name)
    if cue is None:
        raise SystemExit(f"The scene has no cue '{cue_name}'.")
    if cue["as"] not in state["tokens"]:
        raise SystemExit(f"{cue['as']} has not joined yet, so it cannot post '{cue_name}'.")
    if "reply_to" in cue and cue["reply_to"] not in state["messages"]:
        raise SystemExit(f"Post '{cue['reply_to']}' before '{cue_name}', which replies to it.")


async def post(session: ClientSession, scene: dict[str, Any], state: dict[str, Any], cue_name: str) -> None:
    """Post the cue ``cue_name`` as its participant and remember its message ID."""
    cue = scene["cues"][cue_name]
    author = cue["as"]
    arguments = {field: cue[field] for field in MESSAGE_FIELDS if field in cue}
    if "reply_to" in cue:
        arguments["reply_to"] = state["messages"][cue["reply_to"]]
    posted = await call(
        session,
        "post_message",
        token=state["tokens"][author],
        submission_id=cue_name,
        body=cue["body"],
        **arguments,
    )
    if "message_id" in posted:
        state["messages"][cue_name] = posted["message_id"]


async def run(arguments: argparse.Namespace) -> None:
    """Run one stand-in command and save the updated state."""
    scene = json.loads(arguments.scene.read_text())
    state = load_state(arguments.state)
    code = ""
    if arguments.command == "join":
        code = arguments.code or state["codes"].get(arguments.name, "")
        if not code:
            raise SystemExit(f"No join code for {arguments.name}: pass it.")
    elif arguments.command == "post":
        check_cue(scene, state, arguments.cue)
    log = arguments.state.with_name("stand-in.log")
    async with connect(arguments.workspace, log) as session:
        if arguments.command == "create":
            await create(session, scene, state)
        elif arguments.command == "join":
            await join(session, scene, state, arguments.name, code)
        else:
            await post(session, scene, state, arguments.cue)
    save_state(arguments.state, state)


def main() -> None:
    """Parse the command line and run the stand-in command."""
    parser = argparse.ArgumentParser(description="Post scripted chat messages through the bridge.")
    parser.add_argument("--workspace", type=Path, required=True, help="the scene's project folder")
    parser.add_argument("--scene", type=Path, required=True, help="the scene file (JSON)")
    parser.add_argument("--state", type=Path, required=True, help="private file for tokens between calls")
    commands = parser.add_subparsers(dest="command", required=True)
    commands.add_parser("create", help="start the conversation as the lead")
    join_command = commands.add_parser("join", help="join as NAME, with CODE or the stored code")
    join_command.add_argument("name")
    join_command.add_argument("code", nargs="?")
    post_command = commands.add_parser("post", help="post one cue")
    post_command.add_argument("cue")
    asyncio.run(run(parser.parse_args()))


if __name__ == "__main__":
    main()

"""Exercise operator authorisation, launches, wakes and close through the core's socket."""

import asyncio
import json
import os
import socket
import stat
import sys
from collections.abc import Iterator
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass
from pathlib import Path
from tempfile import TemporaryDirectory
from threading import Event, Thread
from typing import Any

import pytest
from sqlalchemy import select, update

from agents_chat.conversations import models
from agents_chat.conversations.assignment_tables import assignment
from agents_chat.conversations.operator_models import CONTRACT
from agents_chat.conversations.progress import lead_waiting_on
from agents_chat.conversations.roles import HUMAN_LABEL
from agents_chat.mcp import concurrency, server
from agents_chat.mcp.concurrency import transaction
from agents_chat.mcp.core import (
    OPERATOR_KEY_ERROR,
    OPERATOR_KEY_FILE,
    REQUEST,
    Core,
    write_operator_key,
)
from agents_chat.storage.database import workspace_key

FIXTURE = Path(__file__).parents[1] / "fixtures" / "fake_run_plugin.py"
EXAMPLES = Path(__file__).parents[2] / "protocol" / "operator.v1.examples.json"


@dataclass(frozen=True)
class Bridge:
    """Hold a core serving a private socket, its operator key and one checkout."""

    core: Core
    home: Path
    socket: Path
    directory: Path
    workspace: str

    @property
    def key(self) -> str:
        """Read the key the way an operator client does."""
        return (self.home / OPERATOR_KEY_FILE).read_text(encoding="ascii")


@pytest.fixture
def bridge(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> Iterator[Bridge]:
    """Serve a core with one fixture plugin on a short socket path."""
    home = tmp_path / "bridge"
    home.mkdir(mode=0o700)
    config = home / "plugins.json"
    config.write_text(
        json.dumps(
            {
                "plugins": {
                    "fixture": {
                        "command": [sys.executable, str(FIXTURE)],
                        "model": "test/model",
                        "timeout_seconds": 120,
                        "environment": ["TEST_FRESH_RESULT"],
                    },
                },
            }
        ),
        encoding="utf-8",
    )
    config.chmod(0o600)
    monkeypatch.setenv("AGENTS_CHAT_HOME", str(home))
    directory = tmp_path / "workspace"
    directory.mkdir()
    # A pytest base temp directory can exceed the Unix socket path limit on macOS.
    with TemporaryDirectory(prefix="agents-chat-operator-", dir="/tmp") as sockets:
        socket_path = Path(sockets) / "core.sock"
        core = Core(str(socket_path), write_operator_key(home))
        core.worker.ready.set()
        thread = Thread(target=core.serve_forever, daemon=True)
        thread.start()
        try:
            yield Bridge(core, home, socket_path, directory, workspace_key(directory))
        finally:
            core.shutdown()
            core.server_close()
            thread.join(timeout=5)


def send(bridge: Bridge, payload: dict) -> dict:
    """Exchange one JSON line with the core."""
    with socket.socket(socket.AF_UNIX) as client:
        client.settimeout(10)
        client.connect(str(bridge.socket))
        client.sendall(json.dumps(payload).encode() + b"\n")
        with client.makefile("rb") as reader:
            return json.loads(reader.readline())


def operate(bridge: Bridge, action: str, **fields: Any) -> dict:
    """Send an authorised operator request, check its result shape, and return it."""
    payload = {"version": 1, "key": bridge.key, "action": action, **fields}
    if action != "operator_plugins":
        payload["directory"] = str(bridge.directory)
    response = send(bridge, payload)
    assert "error" not in response, response
    CONTRACT[type(REQUEST.validate_python(payload))].model_validate(response["result"])
    return response["result"]


def operator_error(bridge: Bridge, action: str, **fields: Any) -> str:
    """Send an authorised operator request that must fail and return its error."""
    payload = {"version": 1, "key": bridge.key, "action": action, **fields}
    payload["directory"] = str(bridge.directory)
    response = send(bridge, payload)
    assert "result" not in response, response
    return response["error"]


def create(bridge: Bridge, invitees: list | None = None) -> dict:
    """Create a conversation as the human with a lead and one worker slot."""
    return operate(
        bridge,
        "operator_create",
        brief="Review retries",
        lead="Lead",
        invitees=invitees or ["Worker"],
    )


def join(bridge: Bridge, code: str) -> str:
    """Join a slot as a model and return its token."""
    joined = bridge.core.apply(models.Join(workspace=bridge.workspace, code=code))
    return joined["token"]


def codes(created: dict) -> dict[str, str]:
    """Map slot names to their join codes."""
    return {item["participant"]: item["code"] for item in created["invitations"]}


def dispatch(bridge: Bridge) -> str:
    """Send one queued launch through the fixture plugin and return its join code."""
    job = bridge.core.worker.jobs.get_nowait()
    assert job is not None
    bridge.core.worker.launch(job)
    return job.start.join_code


def launch_states(bridge: Bridge) -> list[str]:
    """Read every persisted assignment state."""
    with transaction(bridge.workspace) as connection:
        return list(connection.scalars(select(assignment.c.state)))


def test_operator_key_file_is_private_and_replaced(tmp_path: Path) -> None:
    """Each start writes a new 0600 key and never writes through a planted symlink."""
    home = tmp_path / "bridge"
    home.mkdir(mode=0o700)
    target = tmp_path / "target"
    target.write_text("unchanged", encoding="ascii")
    (home / OPERATOR_KEY_FILE).symlink_to(target)

    first = write_operator_key(home)
    second = write_operator_key(home)

    path = home / OPERATOR_KEY_FILE
    assert not path.is_symlink()
    assert stat.S_IMODE(path.stat().st_mode) == 0o600
    assert path.read_text(encoding="ascii") == second
    assert first != second
    assert len(second) >= 43
    assert target.read_text(encoding="ascii") == "unchanged"
    assert sorted(item.name for item in home.iterdir()) == [OPERATOR_KEY_FILE]


def test_every_action_rejects_a_missing_or_wrong_key(bridge: Bridge) -> None:
    """Every action fails with one generic error that never contains the key."""
    member_token = join(bridge, codes(create(bridge))["Lead"])
    examples = json.loads(EXAMPLES.read_bytes())
    wrong_keys = ["wrong", member_token, "k" * 200, "", "schlüssel"]
    for example in examples:
        request = {**example["request"]}
        if "directory" in request:
            request["directory"] = str(bridge.directory)
        missing = {field: value for field, value in request.items() if field != "key"}
        attempts = [missing] + [{**request, "key": key} for key in wrong_keys]
        for attempt in attempts:
            response = send(bridge, attempt)
            assert response == {"error": OPERATOR_KEY_ERROR}, (example["action"], response)
    listed = operate(bridge, "operator_list")["conversations"]
    assert len(listed) == 1
    assert listed[0]["joined"] == 1


def test_member_requests_still_validate_after_operator_parsing(bridge: Bridge) -> None:
    """An invalid member request keeps its existing generic error."""
    response = send(bridge, {"action": "status", "workspace": bridge.workspace})
    assert response == {"error": "Invalid bridge arguments"}
    response = send(
        bridge, {"action": "status", "workspace": bridge.workspace, "token": "t", "key": "k"}
    )
    assert response == {"error": "Invalid bridge arguments"}
    response = send(bridge, {"version": 1, "key": bridge.key, "action": "operator_list"})
    assert response == {"error": "Invalid bridge arguments"}
    relative = {"version": 1, "key": bridge.key, "action": "operator_list", "directory": "work"}
    assert send(bridge, relative) == {"error": "Invalid bridge arguments"}
    missing = {**relative, "directory": str(bridge.directory / "missing")}
    assert send(bridge, missing) == {"error": "Directory must be an existing directory"}


def test_plugins_lists_names_and_models_only(bridge: Bridge) -> None:
    """Commands, environment names and caps stay in the private configuration."""
    assert operate(bridge, "operator_plugins") == {
        "plugins": [{"name": "fixture", "model": "test/model"}]
    }


def test_launch_fills_only_worker_slots(bridge: Bridge) -> None:
    """The human can launch a worker, not the lead, and cannot relaunch an ended run."""
    created = create(bridge)
    conversation = created["conversation"]

    worker = operate(
        bridge, "operator_launch", conversation=conversation, name="Worker", plugin="fixture"
    )
    assert worker["state"] == "queued"
    dispatch(bridge)
    assert launch_states(bridge) == ["running"]
    members = operate(bridge, "operator_show", conversation=conversation)["status"]["members"]
    assert members["Worker"]["launch_state"] == "running"
    error = operator_error(
        bridge, "operator_launch", conversation=conversation, name="Lead", plugin="fixture"
    )
    assert "lead" in error.lower()

    with transaction(bridge.workspace) as connection:
        connection.execute(update(assignment).values(state="failed"))
    error = operator_error(
        bridge, "operator_launch", conversation=conversation, name="Worker", plugin="fixture"
    )
    assert error == "The fresh run for this slot has ended. Relaunch is not supported yet"
    error = operator_error(
        bridge, "operator_launch", conversation=conversation, name=HUMAN_LABEL, plugin="fixture"
    )
    assert "reserved" in error
    error = operator_error(
        bridge, "operator_launch", conversation=conversation, name="New", plugin="missing"
    )
    assert error == "Fresh-run plugin is not configured"


def test_member_launch_still_protects_the_lead_slot(bridge: Bridge) -> None:
    """Only the human's launch may fill an unused lead or co-lead slot."""
    created = create(bridge, ["Worker", {"name": "Partner", "role": "co-lead"}])
    lead_token = join(bridge, codes(created)["Lead"])
    with pytest.raises(ValueError, match="cannot replace a lead or co-lead slot"):
        bridge.core.apply(
            models.Launch(
                workspace=bridge.workspace,
                token=lead_token,
                name="Partner",
                plugin="fixture",
                directory=str(bridge.directory),
            )
        )


def test_human_is_not_waited_on_and_keeps_guidance_working(
    bridge: Bridge, monkeypatch: pytest.MonkeyPatch
) -> None:
    """Lead and worker guidance treat the human as neither a worker nor a lead."""
    monkeypatch.setattr(concurrency, "SETTLE_SECONDS", 0.05)
    monkeypatch.setattr(server, "exchange", bridge.core.apply)
    created = create(bridge)
    conversation = created["conversation"]
    lead = join(bridge, codes(created)["Lead"])
    worker = join(bridge, codes(created)["Worker"])
    note = operate(
        bridge, "operator_post", conversation=conversation, submission_id="n", body="Go ahead."
    )

    page = asyncio.run(server.read_member(bridge.workspace, lead, 0, 1, {}))
    assert page["messages"][-1]["label"] == HUMAN_LABEL
    assert page["messages"][-1]["id"] == note["message_id"]
    status = bridge.core.apply(models.Status(workspace=bridge.workspace, token=lead))
    assert status["members"][HUMAN_LABEL] == {"role": "human", "state": "human"}
    assert lead_waiting_on(status, "Lead") == ["Worker"]
    assert "Worker" in server.guidance(status, "Lead", False, 1)
    assert HUMAN_LABEL not in server.guidance(status, "Lead", False, 1)
    worker_page = asyncio.run(server.read_member(bridge.workspace, worker, 0, 1, {}))
    assert worker_page["next"].startswith("Handle these messages")

    bridge.core.apply(
        models.Post(
            workspace=bridge.workspace,
            token=worker,
            submission_id="done",
            body="Finished.",
            kind="done",
        )
    )
    status = bridge.core.apply(models.Status(workspace=bridge.workspace, token=lead))
    assert lead_waiting_on(status, "Lead") == []
    assert server.guidance(status, "Lead", False, 1).startswith("All other members are done")


def test_operator_post_wakes_a_waiting_read(
    bridge: Bridge, monkeypatch: pytest.MonkeyPatch
) -> None:
    """A human message wakes a listening lead like any member message."""
    monkeypatch.setattr(concurrency, "SETTLE_SECONDS", 0.05)
    created = create(bridge)
    lead = join(bridge, codes(created)["Lead"])
    read = concurrency.read_page
    waiting = Event()

    def first_read(request: models.Read) -> dict:
        """Signal once the listener holds the room condition."""
        page = read(request)
        waiting.set()
        return page

    monkeypatch.setattr(concurrency, "read_page", first_read)
    with ThreadPoolExecutor(max_workers=1) as executor:
        listener = executor.submit(
            bridge.core.apply,
            models.Read(workspace=bridge.workspace, token=lead, wait_seconds=10),
        )
        assert waiting.wait(timeout=2)
        operate(
            bridge,
            "operator_post",
            conversation=created["conversation"],
            submission_id="wake",
            body="Please start.",
            to="Lead",
        )
        page = listener.result(timeout=5)

    assert [(entry["label"], entry["body"]) for entry in page["messages"]] == [
        (HUMAN_LABEL, "Please start.")
    ]


def test_operator_close_cancels_running_assignments(bridge: Bridge) -> None:
    """The human's close stops a running fresh run through the existing cancel path."""
    created = create(bridge)
    conversation = created["conversation"]
    operate(bridge, "operator_launch", conversation=conversation, name="Worker", plugin="fixture")
    dispatch(bridge)
    assert launch_states(bridge) == ["running"]

    assert operate(bridge, "operator_close", conversation=conversation, summary="Done.") == {
        "closed": True
    }
    bridge.core.worker.scan()

    assert launch_states(bridge) == ["cancelled"]
    view = operate(bridge, "operator_show", conversation=conversation)
    assert view["status"]["members"]["Worker"]["launch_state"] == "cancelled"
    assert [message["body"] for message in view["messages"]] == [
        "Reissued the invitation for Worker as worker.",
        "Done.",
        "Worker had the fresh run cancelled.",
    ]
    assert operate(bridge, "operator_list")["conversations"][0]["open"] is False


def test_running_core_uses_the_private_key_file(bridge: Bridge) -> None:
    """The core accepts the key that its owner-only key file holds."""
    info = os.stat(bridge.home / OPERATOR_KEY_FILE)
    assert stat.S_IMODE(info.st_mode) == 0o600
    assert info.st_uid == os.getuid()
    assert bridge.core.operator_key == bridge.key


def test_archive_and_search_through_the_socket(bridge: Bridge) -> None:
    """The human archives a closed chat, lists the archive and finds its messages."""
    conversation = create(bridge)["conversation"]
    error = operator_error(bridge, "operator_archive", conversation=conversation, archived=True)
    assert error == "Close the conversation before archiving it"
    operate(
        bridge,
        "operator_post",
        conversation=conversation,
        submission_id="hint",
        body="Check the backoff ceiling.",
    )
    operate(bridge, "operator_close", conversation=conversation)

    archived = operate(bridge, "operator_archive", conversation=conversation, archived=True)

    assert archived == {"archived": True}
    assert operate(bridge, "operator_list")["conversations"] == []
    archive = operate(bridge, "operator_list", archived=True)["conversations"]
    assert [item["conversation"] for item in archive] == [conversation]
    hits = operate(bridge, "operator_search", query="backoff")["hits"]
    assert [(hit["conversation"], hit["author"], hit["archived"]) for hit in hits] == [
        (conversation, HUMAN_LABEL, True)
    ]
    assert operate(bridge, "operator_show", conversation=conversation)["archived"] is True


def test_search_without_a_store_creates_none(bridge: Bridge) -> None:
    """Searching a checkout that has no conversations returns nothing and creates no store."""
    assert operate(bridge, "operator_search", query="retry") == {"hits": []}
    assert list(bridge.home.glob("*.sqlite3")) == []

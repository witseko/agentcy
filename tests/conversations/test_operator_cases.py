"""Replay the committed operator accept and reject cases through the core's own handler."""

import json
import socket
import threading
from pathlib import Path
from typing import get_args

import pytest
from pydantic import ValidationError

from agentcy.conversations.operator_models import CONTRACT
from agentcy.mcp import core

PROTOCOL = Path(__file__).parents[2] / "protocol"
CASE_KEY = "fake-operator-key-0001"
ACCEPTED = "accepted by validation and the key check"
OPERATOR_ACTIONS = {get_args(model.model_fields["action"].annotation)[0] for model in CONTRACT}


class Accepted(ValueError):
    """Stop the real Core.operate right after its key check."""


class KeyOnlyCore:
    """Hold the case key and borrow the real dispatch of core.Core, so precedence is real."""

    operator_key = CASE_KEY

    def apply(self, request: object) -> dict:
        """Route the request exactly as the running core does."""
        return core.Core.apply(self, request)

    def operate(self, request: object) -> dict:
        """Check the key exactly as the running core does."""
        return core.Core.operate(self, request)


def load_cases() -> list[dict]:
    """Read the committed operator cases."""
    return json.loads((PROTOCOL / "operator.v1.cases.json").read_bytes())


def case_line(case: dict) -> bytes:
    """Return the request line as the core reads it, newline included."""
    text = case["raw"] if "raw" in case else json.dumps(case["request"])
    return text.encode() + b"\n"


def stop_after_key_check(*_arguments: object) -> None:
    """Stand in for the first step after the key check."""
    raise Accepted(ACCEPTED)


def daemon_reply(line: bytes) -> dict:
    """Send one line through core.Handler over a socket pair and return the decoded reply."""
    client, server = socket.socketpair()
    with client, server:
        # A long line can exceed the socket buffer, so the handler reads while the test writes.
        handler = threading.Thread(target=core.Handler, args=(server, "", KeyOnlyCore()))
        handler.start()
        client.sendall(line)
        client.shutdown(socket.SHUT_WR)
        reply = client.makefile("rb").readline()
        handler.join()
    return json.loads(reply)


def test_cases_cover_every_operator_action_and_field() -> None:
    """Require a minimal, a full, and a rejected case per action, and every field in the full one."""
    cases = load_cases()
    names = [case["name"] for case in cases]
    assert len(names) == len(set(names))
    by_name = {case["name"]: case for case in cases}
    assert len(OPERATOR_ACTIONS) == 17
    for model in CONTRACT:
        action = get_args(model.model_fields["action"].annotation)[0]
        minimal = by_name[f"{action}: minimal valid"]
        full = by_name[f"{action}: every optional field"]
        assert minimal["valid"] and full["valid"]
        assert set(full["request"]) == set(model.model_fields)
        assert any(case["action"] == action and not case["valid"] for case in cases)
        assert any(
            case["action"] == action and case.get("error") == core.OPERATOR_KEY_ERROR
            for case in cases
        )


@pytest.mark.parametrize("case", load_cases(), ids=lambda case: case["name"][:80])
def test_case_matches_the_core(case: dict, monkeypatch: pytest.MonkeyPatch) -> None:
    """Replay one case: the validator's decision and dump, then the handler's error string."""
    monkeypatch.setattr(core, "plugin_names", stop_after_key_check)
    monkeypatch.setattr(core.operator, "checkout_workspace", stop_after_key_check)
    line = case_line(case)
    try:
        parsed = core.REQUEST.validate_json(line)
    except ValidationError:
        assert not case["valid"]
    else:
        assert case["valid"]
        assert parsed.action in OPERATOR_ACTIONS
        assert parsed.model_dump(mode="json") == case.get("normalised", case.get("request"))
    error = daemon_reply(line).get("error")
    assert error == case.get("error", ACCEPTED)

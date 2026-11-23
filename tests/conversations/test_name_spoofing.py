"""Test that no participant can pass as the human by name, body control codes, or look-alikes."""

from typing import Any

import pytest
from pydantic import ValidationError
from sqlalchemy import Connection

from agents_chat.conversations import models, operator, repository
from agents_chat.conversations import operator_models as actions

ENVELOPE = {"version": 1, "key": "test-operator-key", "directory": "/work/repository"}
LOOKALIKES = [
    "You",
    "you",
    "\u041duman",  # Cyrillic En
    "Hum\u0430n",  # Cyrillic a
    "h\u054dman",  # Armenian Seh
    "H u m a n",
    "Hum-an",
    "Y\u043eu",  # Cyrillic o
    "\u0570uman",  # Armenian ho
]
HIDDEN_CHARACTERS = ["Human\u200b", "HUMAN\u2060", "Hu\u200eman", "Hu\u202eman", "Hu\x00man"]
WORKSPACE = "a" * 64
VALID_NAMES = ["Мария", "José", "Worker 2", "Agent7", "Humanoid", "Youth"]


def workspace_of(connection: Connection) -> str:
    """Return the workspace key of the test store."""
    return connection.info["workspace"]


def create_team(connection: Connection, invitees: list[str], initiator: str = "Lead") -> dict:
    """Create a conversation through the repository."""
    return repository.apply(
        connection,
        models.Create(
            workspace=workspace_of(connection),
            brief="Review",
            initiator=initiator,
            invitees=invitees,
            open_invite=True,
        ),
    )


@pytest.mark.parametrize("name", LOOKALIKES)
def test_lookalike_names_are_rejected_everywhere(conversation_db: Connection, name: str) -> None:
    """Create, invite, and open-invite join all refuse names that look like the human."""
    workspace = workspace_of(conversation_db)
    with pytest.raises(ValueError, match="reserved"):
        create_team(conversation_db, ["W"], initiator=name)
    with pytest.raises(ValueError, match="reserved"):
        create_team(conversation_db, [name])
    team = create_team(conversation_db, ["W"])
    with pytest.raises(ValueError, match="reserved"):
        repository.apply(
            conversation_db, models.Invite(workspace=workspace, token=team["token"], name=name)
        )
    with pytest.raises(ValueError, match="reserved"):
        repository.apply(
            conversation_db,
            models.Join(workspace=workspace, code=team["open_invite"]["code"], name=name),
        )
    with pytest.raises(ValueError, match="reserved"):
        operator.apply(
            conversation_db,
            workspace,
            actions.InviteParticipant.model_validate(
                {
                    **ENVELOPE,
                    "action": "operator_invite",
                    "conversation": team["conversation"],
                    "name": name,
                }
            ),
        )


@pytest.mark.parametrize("name", HIDDEN_CHARACTERS)
def test_names_with_hidden_characters_are_invalid(name: str) -> None:
    """A name with a control or format character fails validation without echoing the input."""
    with pytest.raises(ValidationError) as error:
        models.Join(workspace=WORKSPACE, code="c", name=name)
    assert "control or format" in str(error.value)
    assert name not in str(error.value)


def test_names_must_be_in_nfkc_form() -> None:
    """A name that changes under NFKC is invalid."""
    with pytest.raises(ValidationError, match="NFKC"):
        models.Join(workspace=WORKSPACE, code="c", name="Ｗorker")


@pytest.mark.parametrize("name", VALID_NAMES)
def test_ordinary_names_still_work(conversation_db: Connection, name: str) -> None:
    """Non-Latin names, accents, spaces, digits, and names that only contain Human or You work."""
    team = create_team(conversation_db, [name])
    assert name in team["invitations"]


def test_body_rejects_control_characters_but_allows_whitespace() -> None:
    """An escape character fails validation; newline, tab, and carriage return pass."""
    with pytest.raises(ValidationError) as error:
        models.Post(
            workspace=WORKSPACE, token="t" * 43, submission_id="s", body="hi \x1b[2J", kind="note"
        )
    assert "control characters" in str(error.value)
    assert "\x1b" not in str(error.value)
    post = models.Post(
        workspace=WORKSPACE,
        token="t" * 43,
        submission_id="s",
        body="line\n\tindent\r\nnext",
        kind="note",
    )
    assert post.body == "line\n\tindent\r\nnext"


def operator_request(model: type[actions.OperatorRequest], **fields: Any) -> Any:
    """Build one validated operator request."""
    return model.model_validate({**ENVELOPE, **fields})


def test_human_messages_carry_role_and_worker_messages_do_not(conversation_db: Connection) -> None:
    """Read and the join page mark the human's messages with role human and no others."""
    workspace = workspace_of(conversation_db)
    created = operator.apply(
        conversation_db,
        workspace,
        operator_request(
            actions.CreateConversation,
            action="operator_create",
            brief="Review",
            lead="Lead",
            invitees=["Worker"],
        ),
    )
    conversation = created["conversation"]
    codes = {slot["participant"]: slot["code"] for slot in created["invitations"]}
    lead = repository.apply(conversation_db, models.Join(workspace=workspace, code=codes["Lead"]))
    operator.apply(
        conversation_db,
        workspace,
        operator_request(
            actions.PostMessage,
            action="operator_post",
            conversation=conversation,
            submission_id="human-1",
            body="Please start.",
        ),
    )
    repository.apply(
        conversation_db,
        models.Post(
            workspace=workspace,
            token=lead["token"],
            submission_id="lead-1",
            body="Starting.",
            kind="note",
        ),
    )

    page = repository.apply(
        conversation_db, models.Read(workspace=workspace, token=lead["token"], after=0)
    )
    by_body = {message["body"]: message for message in page["messages"]}
    assert by_body["Please start."]["role"] == "human"
    assert "role" not in by_body["Starting."]

    worker = repository.apply(
        conversation_db, models.Join(workspace=workspace, code=codes["Worker"])
    )
    initial = {message["body"]: message for message in worker["messages"]}
    assert initial["Please start."]["role"] == "human"
    assert "role" not in initial["Starting."]

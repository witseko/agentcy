"""Verify role changes, lead transfers, and authorization rules across repository and operator."""

from secrets import token_hex

import pytest
from pydantic import ValidationError
from sqlalchemy import Connection

from agents_chat.conversations import models, operator, operator_models, repository

ENVELOPE = {"version": 1, "key": "test-operator-key", "directory": "/work"}


def _setup_room(connection: Connection) -> tuple[str, str, str, str, str]:
    """Create a conversation with Lead, Alice, and Bob, all joined."""
    workspace = token_hex(32)
    created = repository.apply(
        connection,
        models.Create(
            workspace=workspace,
            brief="Coordinate role changes",
            initiator="Lead",
            invitees=["Alice", "Bob"],
        ),
    )
    alice = repository.apply(
        connection, models.Join(workspace=workspace, code=created["invitations"]["Alice"])
    )
    bob = repository.apply(
        connection, models.Join(workspace=workspace, code=created["invitations"]["Bob"])
    )
    return workspace, created["conversation"], created["token"], alice["token"], bob["token"]


def test_promote_worker_to_lead_demotes_previous_lead_to_worker(
    conversation_db: Connection,
) -> None:
    """Promoting a worker to lead demotes the previous lead to worker and keeps them active."""
    workspace, _, lead_token, alice_token, _ = _setup_room(conversation_db)

    result = repository.apply(
        conversation_db,
        models.SetRole(workspace=workspace, token=lead_token, name="Alice", role="lead"),
    )

    assert result["participant"] == "Alice"
    assert result["role"] == "lead"
    assert result["roster"] == {"Lead": "worker", "Alice": "lead", "Bob": "worker"}

    # Previous lead stays active (can still post) and is no longer allowed to change roles.
    posted = repository.apply(
        conversation_db,
        models.Post(workspace=workspace, token=lead_token, submission_id="s1", body="Still active"),
    )
    assert posted["message_id"] > 0

    with pytest.raises(ValueError, match="Only a lead"):
        repository.apply(
            conversation_db,
            models.SetRole(workspace=workspace, token=lead_token, name="Bob", role="lead"),
        )

    # New lead has leadership authority and sees the role-change event.
    page = repository.apply(
        conversation_db, models.Read(workspace=workspace, token=alice_token, after=0)
    )
    assert page["role"] == "lead"
    assert any(
        msg.get("kind") == "event" and "Changed lead to Alice" in msg["body"]
        for msg in page["messages"]
    )


def test_co_lead_promotion_and_demotion_preserves_lead_invariant(
    conversation_db: Connection,
) -> None:
    """Setting co-lead shares leadership; demoting one co-lead restores sole lead."""
    workspace, _, lead_token, alice_token, _ = _setup_room(conversation_db)

    promoted = repository.apply(
        conversation_db,
        models.SetRole(workspace=workspace, token=lead_token, name="Alice", role="co-lead"),
    )
    assert promoted["roster"] == {"Lead": "co-lead", "Alice": "co-lead", "Bob": "worker"}

    demoted = repository.apply(
        conversation_db,
        models.SetRole(workspace=workspace, token=alice_token, name="Lead", role="worker"),
    )
    assert demoted["roster"] == {"Lead": "worker", "Alice": "lead", "Bob": "worker"}


def test_set_role_rejects_unauthorized_and_invalid_transitions(
    conversation_db: Connection,
) -> None:
    """Reject worker callers, invalid roles, archived targets, Human, sole-lead demotion, and closed chats."""
    workspace, room_id, lead_token, alice_token, _ = _setup_room(conversation_db)

    # Worker cannot change roles.
    with pytest.raises(ValueError, match="Only a lead"):
        repository.apply(
            conversation_db,
            models.SetRole(workspace=workspace, token=alice_token, name="Bob", role="co-lead"),
        )

    # Invalid role rejected at schema edge.
    with pytest.raises(ValidationError):
        models.SetRole(workspace=workspace, token=lead_token, name="Alice", role="human")  # type: ignore[arg-type]

    # Cannot demote the only lead.
    with pytest.raises(ValueError, match="Cannot demote the only lead"):
        repository.apply(
            conversation_db,
            models.SetRole(workspace=workspace, token=lead_token, name="Lead", role="worker"),
        )

    # Unknown participant rejected.
    with pytest.raises(ValueError, match="Unknown participant"):
        repository.apply(
            conversation_db,
            models.SetRole(workspace=workspace, token=lead_token, name="Ghost", role="lead"),
        )

    # Retired/archived participant stays in DB and cannot have role changed.
    operator.apply(
        conversation_db,
        workspace,
        operator_models.RetireParticipant.model_validate(
            {**ENVELOPE, "action": "operator_retire", "conversation": room_id, "name": "Bob"}
        ),
    )
    with pytest.raises(ValueError, match="Participant is retired: Bob"):
        repository.apply(
            conversation_db,
            models.SetRole(workspace=workspace, token=lead_token, name="Bob", role="lead"),
        )

    # Human role cannot be changed.
    with pytest.raises(ValueError, match="Cannot change the role of Human"):
        repository.apply(
            conversation_db,
            models.SetRole(workspace=workspace, token=lead_token, name="Human", role="lead"),
        )

    # Closed conversation rejects role changes.
    repository.apply(conversation_db, models.Close(workspace=workspace, token=lead_token))
    with pytest.raises(ValueError, match="Conversation is closed"):
        repository.apply(
            conversation_db,
            models.SetRole(workspace=workspace, token=lead_token, name="Alice", role="lead"),
        )


def test_operator_set_role_updates_show_and_keeps_archived_participants(
    conversation_db: Connection,
) -> None:
    """Operator role changes and retirements are reflected in operator_show without deleting old members."""
    workspace, room_id, _, _, _ = _setup_room(conversation_db)

    operator.apply(
        conversation_db,
        workspace,
        operator_models.RetireParticipant.model_validate(
            {**ENVELOPE, "action": "operator_retire", "conversation": room_id, "name": "Bob"}
        ),
    )
    changed = operator.apply(
        conversation_db,
        workspace,
        operator_models.SetParticipantRole.model_validate(
            {
                **ENVELOPE,
                "action": "operator_set_role",
                "conversation": room_id,
                "name": "Alice",
                "role": "lead",
            }
        ),
    )
    assert changed["participant"] == "Alice"
    assert changed["role"] == "lead"
    assert changed["roster"]["Alice"] == "lead"
    assert changed["roster"]["Lead"] == "worker"
    assert changed["roster"]["Bob"] == "worker"

    view = operator.apply(
        conversation_db,
        workspace,
        operator_models.ShowConversation.model_validate(
            {**ENVELOPE, "action": "operator_show", "conversation": room_id, "after": 0}
        ),
    )
    members = view["status"]["members"]
    assert members["Alice"]["role"] == "lead"
    assert members["Lead"]["role"] == "worker"
    assert members["Lead"]["state"] == "joined"
    # Bob was not deleted; Bob remains present with retired state.
    assert members["Bob"]["state"] == "retired"
    assert any(
        msg.get("kind") == "event" and "Changed lead to Alice" in msg["body"]
        for msg in view["messages"]
    )

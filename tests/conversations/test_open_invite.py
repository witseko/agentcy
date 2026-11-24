"""Verify reusable open invites and lead retire/plugin management rules."""

from secrets import token_hex

import pytest
from pydantic import ValidationError
from sqlalchemy import Connection

from agentcy.conversations import models, operator, repository
from agentcy.conversations import operator_models as actions

ENVELOPE = {"version": 1, "key": "test-operator-key", "directory": "/work/repository"}


def test_create_requires_invitees_unless_open_invite_is_enabled() -> None:
    """Reject empty invitees when open_invite is False, and allow them when True."""
    workspace = token_hex(32)
    with pytest.raises(ValidationError, match="open_invite"):
        models.Create(workspace=workspace, brief="Need help", initiator="Lead", invitees=[])
    request = models.Create(
        workspace=workspace, brief="Open room", initiator="Lead", invitees=[], open_invite=True
    )
    assert request.open_invite is True
    assert request.invitees == []


def test_open_invite_lets_multiple_agents_join_and_disambiguates_names(
    conversation_db: Connection,
) -> None:
    """One shared code works for every joiner, honoring optional names and auto-numbering."""
    connection = conversation_db
    workspace = token_hex(32)
    created = repository.apply(
        connection,
        models.Create(
            workspace=workspace,
            brief="Investigate flaky build",
            initiator="Lead",
            invitees=[],
            open_invite=True,
        ),
    )
    open_code = created["open_invite"]["code"]
    assert "CODE" in created["open_invite"]["join_prompt"]

    alice = repository.apply(
        connection, models.Join(workspace=workspace, code=open_code, name="Alice")
    )
    alice_two = repository.apply(
        connection, models.Join(workspace=workspace, code=open_code, name="Alice")
    )
    alice_three = repository.apply(
        connection, models.Join(workspace=workspace, code=open_code, name="alice")
    )
    auto_one = repository.apply(connection, models.Join(workspace=workspace, code=open_code))
    auto_two = repository.apply(connection, models.Join(workspace=workspace, code=open_code))

    assert alice["participant"] == "Alice"
    assert alice_two["participant"] == "Alice 2"
    assert alice_three["participant"] == "alice 3"
    assert auto_one["participant"] == "Worker 1"
    assert auto_two["participant"] == "Worker 2"
    assert len({alice["token"], alice_two["token"], auto_one["token"], auto_two["token"]}) == 4
    assert alice["role"] == "worker"

    # Reject the reserved Human name and foreign workspaces.
    with pytest.raises(ValueError, match="reserved"):
        repository.apply(connection, models.Join(workspace=workspace, code=open_code, name="Human"))
    with pytest.raises(ValueError, match="does not match this workspace"):
        repository.apply(connection, models.Join(workspace=token_hex(32), code=open_code))

    # Joiners can read and post, and lifecycle events are visible.
    page = repository.apply(
        connection, models.Read(workspace=workspace, token=created["token"], after=0)
    )
    events = [msg["body"] for msg in page["messages"] if msg.get("kind") == "event"]
    assert "Alice joined via the open invite." in events
    assert "Worker 1 joined via the open invite." in events


def test_open_invite_enforces_participant_cap(conversation_db: Connection) -> None:
    """Stop accepting open-invite joins once the 17-participant limit is reached."""
    connection = conversation_db
    workspace = token_hex(32)
    created = repository.apply(
        connection,
        models.Create(
            workspace=workspace, brief="Cap test", initiator="Lead", invitees=[], open_invite=True
        ),
    )
    code = created["open_invite"]["code"]
    for _ in range(models.MAX_PARTICIPANTS - 1):
        repository.apply(connection, models.Join(workspace=workspace, code=code))
    with pytest.raises(ValueError, match=f"{models.MAX_PARTICIPANTS} participants"):
        repository.apply(connection, models.Join(workspace=workspace, code=code))


def test_open_invite_rotate_disable_close_and_authorization(conversation_db: Connection) -> None:
    """Only active leads/co-leads can toggle or rotate; rotating, disabling, or closing revokes."""
    connection = conversation_db
    workspace = token_hex(32)
    created = repository.apply(
        connection,
        models.Create(workspace=workspace, brief="Rotate test", initiator="Lead", invitees=["W1"]),
    )
    lead = created["token"]
    worker = repository.apply(
        connection, models.Join(workspace=workspace, code=created["invitations"]["W1"])
    )["token"]

    # Worker cannot enable open invite.
    with pytest.raises(ValueError, match="Only a lead"):
        repository.apply(connection, models.OpenInvite(workspace=workspace, token=worker))

    # Lead enables open invite.
    first = repository.apply(
        connection, models.OpenInvite(workspace=workspace, token=lead, enabled=True)
    )
    assert first["open_invite"] is True
    status = repository.apply(connection, models.Status(workspace=workspace, token=lead))
    assert status["open_invite"] is True

    # Lead rotates open invite; old code fails, new code succeeds.
    rotated = repository.apply(
        connection, models.OpenInvite(workspace=workspace, token=lead, enabled=True)
    )
    assert rotated["code"] != first["code"]
    with pytest.raises(ValueError, match="Invalid or expired"):
        repository.apply(connection, models.Join(workspace=workspace, code=first["code"]))
    guest = repository.apply(
        connection, models.Join(workspace=workspace, code=rotated["code"], name="Guest")
    )
    assert guest["participant"] == "Guest"

    # Lead disables open invite; code fails, existing members keep working.
    disabled = repository.apply(
        connection, models.OpenInvite(workspace=workspace, token=lead, enabled=False)
    )
    assert disabled == {"open_invite": False}
    status = repository.apply(connection, models.Status(workspace=workspace, token=lead))
    assert "open_invite" not in status
    with pytest.raises(ValueError, match="Invalid or expired"):
        repository.apply(connection, models.Join(workspace=workspace, code=rotated["code"]))
    posted = repository.apply(
        connection,
        models.Post(workspace=workspace, token=guest["token"], submission_id="g1", body="Still here"),
    )
    assert posted["message_id"] > 0

    # Re-enable then close: closing revokes the open invite too.
    re_enabled = repository.apply(
        connection, models.OpenInvite(workspace=workspace, token=lead, enabled=True)
    )
    repository.apply(connection, models.Close(workspace=workspace, token=lead))
    with pytest.raises(ValueError, match="Invalid or expired"):
        repository.apply(connection, models.Join(workspace=workspace, code=re_enabled["code"]))
    with pytest.raises(ValueError, match="Conversation is closed"):
        repository.apply(connection, models.OpenInvite(workspace=workspace, token=lead))


def test_lead_retire_participant_rules_and_authorization(conversation_db: Connection) -> None:
    """Leads and co-leads can retire participants; workers and retired leads cannot."""
    connection = conversation_db
    workspace = token_hex(32)
    created = repository.apply(
        connection,
        models.Create(
            workspace=workspace,
            brief="Retire test",
            initiator="Lead",
            invitees=[models.Invitation(name="CoLead", role="co-lead"), "Worker"],
        ),
    )
    lead = created["token"]
    colead = repository.apply(
        connection, models.Join(workspace=workspace, code=created["invitations"]["CoLead"])
    )["token"]
    worker = repository.apply(
        connection, models.Join(workspace=workspace, code=created["invitations"]["Worker"])
    )["token"]

    # Worker cannot retire anyone.
    with pytest.raises(ValueError, match="Only a lead"):
        repository.apply(connection, models.Retire(workspace=workspace, token=worker, name="Lead"))

    # Co-lead retires Worker.
    retired = repository.apply(
        connection, models.Retire(workspace=workspace, token=colead, name="Worker")
    )
    assert retired == {"participant": "Worker", "retired": True}

    # Retired worker cannot post.
    with pytest.raises(ValueError, match="retired"):
        repository.apply(
            connection,
            models.Post(workspace=workspace, token=worker, submission_id="w1", body="Blocked"),
        )

    # Retired lead cannot invite, retire, or manage open invites.
    repository.apply(connection, models.Retire(workspace=workspace, token=lead, name="CoLead"))
    with pytest.raises(ValueError, match="retired"):
        repository.apply(connection, models.OpenInvite(workspace=workspace, token=colead))
    with pytest.raises(ValueError, match="retired"):
        repository.apply(connection, models.Retire(workspace=workspace, token=colead, name="Lead"))


def test_operator_create_and_toggle_open_invite(conversation_db: Connection) -> None:
    """Operator can create a zero-worker open-invite room and rotate or disable its code."""
    connection = conversation_db
    workspace = token_hex(32)
    created = operator.apply(
        connection,
        workspace,
        actions.CreateConversation.model_validate(
            {
                **ENVELOPE,
                "action": "operator_create",
                "brief": "Open operator chat",
                "lead": "Lead",
                "invitees": [],
                "open_invite": True,
            }
        ),
    )
    open_code = created["open_invite"]["code"]
    assert open_code in created["open_invite"]["join_prompt"]

    joined = repository.apply(
        connection, models.Join(workspace=workspace, code=open_code, name="Helper")
    )
    assert joined["participant"] == "Helper"

    view = operator.apply(
        connection,
        workspace,
        actions.ShowConversation.model_validate(
            {
                **ENVELOPE,
                "action": "operator_show",
                "conversation": created["conversation"],
                "after": 0,
            }
        ),
    )
    assert view["status"]["open_invite"] is True
    assert view["status"]["members"]["Helper"]["state"] == "joined"

    disabled = operator.apply(
        connection,
        workspace,
        actions.SetOpenInvite.model_validate(
            {
                **ENVELOPE,
                "action": "operator_open_invite",
                "conversation": created["conversation"],
                "enabled": False,
            }
        ),
    )
    assert disabled == {"open_invite": False}

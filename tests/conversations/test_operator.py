"""Test the human's operator actions against a private conversation store."""

import time
from pathlib import Path
from secrets import token_hex
from typing import Any

import pytest
from sqlalchemy import Connection, select, update

from agents_chat.conversations import models, operator, repository
from agents_chat.conversations import operator_models as actions
from agents_chat.conversations.roles import HUMAN_LABEL, Role
from agents_chat.conversations.tables import member, room
from agents_chat.mcp.concurrency import Notifications
from agents_chat.mcp.core import Core

DIRECTORY = "/work/repository"
ENVELOPE = {"version": 1, "key": "test-operator-key", "directory": DIRECTORY}


def run(connection: Connection, model: type[actions.OperatorRequest], **fields: Any) -> dict:
    """Validate and apply one operator action in the test store."""
    request = model.model_validate({**ENVELOPE, **fields})
    return operator.apply(connection, connection.info["workspace"], request)


def create(connection: Connection, invitees: list | None = None, **fields: Any) -> dict:
    """Create a conversation as the human with one worker unless more slots are given."""
    defaults = {"brief": "Review retries\nCite the tests.", "lead": "Lead"}
    return run(
        connection,
        actions.CreateConversation,
        action="operator_create",
        invitees=invitees or ["Worker"],
        **{**defaults, **fields},
    )


def join(connection: Connection, code: str) -> dict:
    """Redeem a join code as a model would."""
    return repository.apply(
        connection, models.Join(workspace=connection.info["workspace"], code=code)
    )


def show(connection: Connection, conversation: str, after: int = 0) -> dict:
    """Read one operator view of a conversation."""
    return run(
        connection,
        actions.ShowConversation,
        action="operator_show",
        conversation=conversation,
        after=after,
    )


def human_post(connection: Connection, conversation: str, body: str, **fields: Any) -> dict:
    """Post as the human with a submission ID derived from the body."""
    return run(
        connection,
        actions.PostMessage,
        action="operator_post",
        conversation=conversation,
        submission_id=fields.pop("submission_id", body),
        body=body,
        **fields,
    )


def test_create_invites_every_slot_and_adds_the_human(conversation_db: Connection) -> None:
    """Nobody holds a token after create, each slot has a ready prompt, and the time is kept."""
    before = int(time.time())
    created = create(conversation_db, ["Worker", {"name": "Partner", "role": "co-lead"}])

    invitations = {item["participant"]: item for item in created["invitations"]}
    assert [item["participant"] for item in created["invitations"]] == ["Lead", "Worker", "Partner"]
    assert invitations["Lead"]["role"] == "co-lead"
    assert invitations["Worker"]["role"] == "worker"
    for item in created["invitations"]:
        assert "CODE" not in item["join_prompt"]
        assert item["code"] in item["join_prompt"]
    assert invitations["Lead"]["join_prompt"].startswith(operator.LEAD_JOIN_PROMPT.split("CODE")[0])
    assert invitations["Worker"]["join_prompt"] == repository.JOIN_PROMPT.replace(
        "CODE", invitations["Worker"]["code"]
    )
    assert created["roster"] == {
        "Lead": "co-lead",
        "Worker": "worker",
        "Partner": "co-lead",
        HUMAN_LABEL: "human",
    }
    rows = conversation_db.execute(
        select(member.c.label, member.c.token_hash, member.c.invite_hash)
    ).all()
    assert all(token_hash is None for _, token_hash, _ in rows)
    assert {label for label, _, invite_hash in rows if invite_hash is None} == {HUMAN_LABEL}
    created_time = conversation_db.scalar(select(room.c.created))
    assert before <= created_time <= int(time.time())


def test_chat_create_records_created_time(conversation_db: Connection) -> None:
    """A conversation started from a chat also records its creation time."""
    repository.apply(
        conversation_db,
        models.Create(
            workspace=conversation_db.info["workspace"],
            brief="Review",
            initiator="Lead",
            invitees=["Worker"],
        ),
    )
    assert conversation_db.scalar(select(room.c.created)) is not None


def test_lead_joins_and_status_reports_the_human(conversation_db: Connection) -> None:
    """The invited lead joins with its code and sees the human as a distinct member."""
    created = create(conversation_db)
    lead = join(conversation_db, created["invitations"][0]["code"])
    assert lead["role"] == "lead"
    assert lead["roster"][HUMAN_LABEL] == "human"

    status = repository.apply(
        conversation_db,
        models.Status(workspace=conversation_db.info["workspace"], token=lead["token"]),
    )
    assert status["members"][HUMAN_LABEL] == {"role": "human", "state": "human"}
    assert status["members"]["Worker"]["state"] == "invited"


def test_human_does_not_count_toward_the_participant_limit(conversation_db: Connection) -> None:
    """Seventeen model slots fit beside the human, and an eighteenth slot is refused."""
    workers = [f"Worker{number}" for number in range(models.MAX_PARTICIPANTS - 1)]
    created = create(conversation_db, workers)
    conversation = created["conversation"]
    assert len(created["roster"]) == models.MAX_PARTICIPANTS + 1

    with pytest.raises(ValueError, match="already has 17 participants"):
        run(
            conversation_db,
            actions.InviteParticipant,
            action="operator_invite",
            conversation=conversation,
            name="Extra",
        )
    reissued = run(
        conversation_db,
        actions.InviteParticipant,
        action="operator_invite",
        conversation=conversation,
        name="Worker0",
    )
    assert reissued["participant"] == "Worker0"


@pytest.mark.parametrize("name", [HUMAN_LABEL, "human", " HUMAN "])
def test_models_cannot_take_the_human_name(conversation_db: Connection, name: str) -> None:
    """Create and invite reject the reserved label in any letter case."""
    workspace = conversation_db.info["workspace"]
    with pytest.raises(ValueError, match="reserved"):
        repository.apply(
            conversation_db,
            models.Create(workspace=workspace, brief="Review", initiator=name, invitees=["W"]),
        )
    with pytest.raises(ValueError, match="reserved"):
        repository.apply(
            conversation_db,
            models.Create(workspace=workspace, brief="Review", initiator="Lead", invitees=[name]),
        )
    team = repository.apply(
        conversation_db,
        models.Create(workspace=workspace, brief="Review", initiator="Lead", invitees=["W"]),
    )
    with pytest.raises(ValueError, match="reserved"):
        repository.apply(
            conversation_db, models.Invite(workspace=workspace, token=team["token"], name=name)
        )
    with pytest.raises(ValueError, match="reserved"):
        create(conversation_db, lead=name)


def test_human_cannot_own_a_task(conversation_db: Connection) -> None:
    """A lead cannot assign a task that the human has no way to complete."""
    created = create(conversation_db)
    lead = join(conversation_db, created["invitations"][0]["code"])
    with pytest.raises(ValueError, match="human cannot own a task"):
        repository.apply(
            conversation_db,
            models.Post(
                workspace=conversation_db.info["workspace"],
                token=lead["token"],
                submission_id="task",
                body="Decide.",
                kind="task",
                to=HUMAN_LABEL,
            ),
        )


def test_post_is_retry_safe_and_shown_with_created_time(conversation_db: Connection) -> None:
    """The human's own messages appear in the transcript with authors and times."""
    created = create(conversation_db)
    conversation = created["conversation"]
    first = human_post(conversation_db, conversation, "Start with the worker.")
    assert human_post(conversation_db, conversation, "Start with the worker.") == first
    with pytest.raises(ValueError, match="Submission ID already used"):
        human_post(
            conversation_db, conversation, "Other text", submission_id="Start with the worker."
        )
    question = human_post(
        conversation_db,
        conversation,
        "Which test covers it?",
        kind="question",
        to="Worker",
        reply_to=first["message_id"],
    )

    view = show(conversation_db, conversation)
    assert view["brief"] == "Review retries\nCite the tests."
    assert view["status"]["open"] is True
    assert [message["id"] for message in view["messages"]] == [
        first["message_id"],
        question["message_id"],
    ]
    second = view["messages"][1]
    assert second["label"] == HUMAN_LABEL
    assert second["kind"] == "question"
    assert second["to"] == "Worker"
    assert second["reply_to"] == first["message_id"]
    assert isinstance(second["created"], int)
    assert "kind" not in view["messages"][0]
    assert view["cursor"] == question["message_id"]
    assert view["more"] is False


def test_show_pages_after_the_cursor(conversation_db: Connection) -> None:
    """A long transcript arrives in pages that continue from the returned cursor."""
    conversation = create(conversation_db)["conversation"]
    for number in range(operator.PAGE_SIZE + 2):
        human_post(conversation_db, conversation, f"Note {number}")
    first = show(conversation_db, conversation)
    assert first["more"] is True
    assert len(first["messages"]) == operator.PAGE_SIZE
    rest = show(conversation_db, conversation, first["cursor"])
    assert [message["body"] for message in rest["messages"]] == [
        f"Note {operator.PAGE_SIZE}",
        f"Note {operator.PAGE_SIZE + 1}",
    ]
    assert rest["more"] is False


def test_first_write_adds_the_human_to_a_chat_conversation(conversation_db: Connection) -> None:
    """Show leaves a chat's roster unchanged, and the first post adds the human."""
    workspace = conversation_db.info["workspace"]
    team = repository.apply(
        conversation_db,
        models.Create(workspace=workspace, brief="Review", initiator="Lead", invitees=["W"]),
    )
    conversation = team["conversation"]
    assert HUMAN_LABEL not in show(conversation_db, conversation)["status"]["members"]

    posted = human_post(conversation_db, conversation, "Please also check timeouts.")
    page = repository.apply(
        conversation_db, models.Read(workspace=workspace, token=team["token"], after=0)
    )
    assert page["messages"][-1] == {
        "id": posted["message_id"],
        "label": HUMAN_LABEL,
        "body": "Please also check timeouts.",
    }
    assert page["roster"][HUMAN_LABEL] == "human"
    human_post(conversation_db, conversation, "One more note.")
    humans = conversation_db.scalars(select(member.c.id).where(member.c.role == Role.HUMAN))
    assert len(list(humans)) == 1


def test_list_is_newest_first_and_handles_old_rows(conversation_db: Connection) -> None:
    """Rows without a created time come last, titles are short, and the human is not counted."""
    workspace = conversation_db.info["workspace"]
    legacy = repository.apply(
        conversation_db,
        models.Create(workspace=workspace, brief="Old brief", initiator="Lead", invitees=["W"]),
    )
    conversation_db.execute(
        update(room).where(room.c.id == legacy["conversation"]).values(created=None)
    )
    older = create(conversation_db, brief="x" * 200)
    newer = create(conversation_db)
    conversation_db.execute(
        update(room).where(room.c.id == older["conversation"]).values(created=1)
    )
    join(conversation_db, newer["invitations"][1]["code"])

    listed = run(conversation_db, actions.ListConversations, action="operator_list")[
        "conversations"
    ]
    assert [item["conversation"] for item in listed] == [
        newer["conversation"],
        older["conversation"],
        legacy["conversation"],
    ]
    assert listed[0]["title"] == "Review retries"
    assert listed[0]["members"] == 2
    assert listed[0]["joined"] == 1
    assert listed[1]["title"] == "x" * (operator.TITLE_LENGTH - 3) + "..."
    assert listed[2]["created"] is None
    assert listed[2]["joined"] == 1


def test_invite_adds_and_reissues_with_ready_prompts(conversation_db: Connection) -> None:
    """The human can add a slot and replace its unused code."""
    conversation = create(conversation_db)["conversation"]
    fields = {"action": "operator_invite", "conversation": conversation}
    added = run(conversation_db, actions.InviteParticipant, name="Analyst", **fields)
    reissued = run(conversation_db, actions.InviteParticipant, name="analyst", **fields)
    assert added["participant"] == reissued["participant"] == "Analyst"
    assert added["code"] != reissued["code"]
    assert reissued["join_prompt"] == repository.JOIN_PROMPT.replace("CODE", reissued["code"])
    lead_code = run(conversation_db, actions.InviteParticipant, name="Lead", **fields)
    assert lead_code["role"] == "lead"
    assert lead_code["join_prompt"] == operator.LEAD_JOIN_PROMPT.replace("CODE", lead_code["code"])
    events = [message["body"] for message in show(conversation_db, conversation)["messages"]]
    assert events[0] == "Invited Analyst as worker."


def test_close_records_the_summary_and_discards_held_proposals(
    conversation_db: Connection,
) -> None:
    """Closing as the human reuses the lead's close path and can be retried."""
    created = create(conversation_db, ["Worker", "Peer"], sealed=True)
    conversation = created["conversation"]
    worker = join(conversation_db, created["invitations"][1]["code"])
    join(conversation_db, created["invitations"][2]["code"])
    repository.apply(
        conversation_db,
        models.Post(
            workspace=conversation_db.info["workspace"],
            token=worker["token"],
            submission_id="idea",
            body="Sealed idea",
            kind="proposal",
        ),
    )
    fields = {"action": "operator_close", "conversation": conversation}
    closed = run(conversation_db, actions.CloseConversation, summary="Stopped early.", **fields)
    assert closed == {"closed": True}
    assert run(conversation_db, actions.CloseConversation, **fields) == {"closed": True}

    view = show(conversation_db, conversation)
    assert view["status"]["open"] is False
    assert "round" not in view["status"]
    assert [(message["label"], message["body"]) for message in view["messages"]] == [
        (HUMAN_LABEL, "Stopped early.")
    ]
    with pytest.raises(ValueError, match="Conversation is closed"):
        human_post(conversation_db, conversation, "Late note")
    with pytest.raises(ValueError, match="Conversation is closed"):
        run(
            conversation_db,
            actions.InviteParticipant,
            action="operator_invite",
            conversation=conversation,
            name="Late",
        )


def test_unknown_conversation_is_rejected(conversation_db: Connection) -> None:
    """A conversation ID from another workspace or a typo is not found."""
    create(conversation_db)
    with pytest.raises(ValueError, match=operator.UNKNOWN_CONVERSATION):
        show(conversation_db, token_hex(12))
    with pytest.raises(ValueError, match=operator.UNKNOWN_CONVERSATION):
        human_post(conversation_db, token_hex(12), "Hello")


def test_listing_without_a_store_creates_none(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """Listing a checkout that has no conversations leaves the data directory empty."""
    home = tmp_path / "bridge"
    home.mkdir(mode=0o700)
    monkeypatch.setenv("AGENTS_CHAT_HOME", str(home))
    checkout = tmp_path / "checkout"
    checkout.mkdir()
    core = Core.__new__(Core)
    core.notifications = Notifications()
    core.operator_key = "test-operator-key"
    request = actions.ListConversations.model_validate(
        {**ENVELOPE, "action": "operator_list", "directory": str(checkout)}
    )

    assert core.apply(request) == {"conversations": []}
    assert list(home.glob("*.sqlite3")) == []
    show_request = actions.ShowConversation.model_validate(
        {
            **ENVELOPE,
            "action": "operator_show",
            "directory": str(checkout),
            "conversation": token_hex(12),
        }
    )
    with pytest.raises(ValueError, match=operator.UNKNOWN_CONVERSATION):
        core.apply(show_request)
    assert list(home.glob("*.sqlite3")) == []


def archive(connection: Connection, conversation: str, archived: bool = True) -> dict:
    """Archive or unarchive a conversation as the human."""
    return run(
        connection,
        actions.ArchiveConversation,
        action="operator_archive",
        conversation=conversation,
        archived=archived,
    )


def listed_ids(connection: Connection, archived: bool = False) -> list[str]:
    """List the conversation IDs of the active list or the archive."""
    listed = run(connection, actions.ListConversations, action="operator_list", archived=archived)
    return [item["conversation"] for item in listed["conversations"]]


def search(connection: Connection, query: str) -> list[dict]:
    """Search every conversation of the workspace as the human."""
    return run(connection, actions.SearchConversations, action="operator_search", query=query)[
        "hits"
    ]


def close(connection: Connection, conversation: str) -> None:
    """Close a conversation as the human without a summary."""
    run(connection, actions.CloseConversation, action="operator_close", conversation=conversation)


def test_archive_moves_a_closed_chat_between_lists(conversation_db: Connection) -> None:
    """Only a closed chat can be archived, and members can still read it."""
    created = create(conversation_db)
    conversation = created["conversation"]
    lead = join(conversation_db, created["invitations"][0]["code"])
    other = create(conversation_db)["conversation"]
    with pytest.raises(ValueError, match="Close the conversation before archiving it"):
        archive(conversation_db, conversation)
    close(conversation_db, conversation)

    assert archive(conversation_db, conversation) == {"archived": True}
    assert listed_ids(conversation_db) == [other]
    assert listed_ids(conversation_db, archived=True) == [conversation]
    assert show(conversation_db, conversation)["archived"] is True
    page = repository.apply(
        conversation_db,
        models.Read(workspace=conversation_db.info["workspace"], token=lead["token"]),
    )
    assert page["brief"] == "Review retries\nCite the tests."

    assert archive(conversation_db, conversation, archived=False) == {"archived": False}
    assert set(listed_ids(conversation_db)) == {conversation, other}
    assert listed_ids(conversation_db, archived=True) == []
    assert show(conversation_db, conversation)["archived"] is False
    with pytest.raises(ValueError, match=operator.UNKNOWN_CONVERSATION):
        archive(conversation_db, token_hex(12))


def test_search_finds_messages_and_briefs(conversation_db: Connection) -> None:
    """A hit names its conversation, message, author, time and a snippet around the words."""
    created = create(conversation_db, brief="Audit the backoff policy\nKeep it short.")
    conversation = created["conversation"]
    lead = join(conversation_db, created["invitations"][0]["code"])
    posted = repository.apply(
        conversation_db,
        models.Post(
            workspace=conversation_db.info["workspace"],
            token=lead["token"],
            submission_id="finding",
            body="The worker retries idempotent jobs with exponential backoff.",
        ),
    )
    create(conversation_db, brief="Unrelated chat about logging")
    close(conversation_db, conversation)
    archive(conversation_db, conversation)

    hits = search(conversation_db, "idempot BACKOFF")
    assert hits == [
        {
            "conversation": conversation,
            "title": "Audit the backoff policy",
            "archived": True,
            "message_id": posted["message_id"],
            "author": "Lead",
            "created": hits[0]["created"],
            "snippet": "The worker retries idempotent jobs with exponential backoff.",
        }
    ]
    assert hits[0]["created"] >= int(time.time()) - 5
    brief_hits = [hit for hit in search(conversation_db, "policy") if hit["message_id"] is None]
    assert [(hit["conversation"], hit["author"]) for hit in brief_hits] == [(conversation, None)]
    assert search(conversation_db, "nothing-like-this") == []


@pytest.mark.parametrize("query", ['"backoff', "backoff*", "(backoff)", "backoff:", "-backoff"])
def test_search_ignores_query_punctuation(conversation_db: Connection, query: str) -> None:
    """FTS5 punctuation in the query has no effect."""
    create(conversation_db, brief="Backoff review")
    assert [hit["title"] for hit in search(conversation_db, query)] == ["Backoff review"]


def test_search_treats_operators_as_words(conversation_db: Connection) -> None:
    """OR and NEAR are words to find, so every word of the query must match."""
    create(conversation_db, brief="Backoff review")
    assert search(conversation_db, "backoff OR zebra") == []
    assert search(conversation_db, "NEAR(backoff review)") == []


def test_search_needs_a_word(conversation_db: Connection) -> None:
    """A query of only punctuation is rejected before it reaches the index."""
    with pytest.raises(ValueError, match="Search needs at least one letter or digit"):
        search(conversation_db, '"*()')


def test_search_skips_sealed_proposals_until_release(conversation_db: Connection) -> None:
    """A proposal held for a sealed round is private, so search cannot reveal it."""
    created = create(conversation_db, ["Worker", "Peer"], sealed=True)
    worker = join(conversation_db, created["invitations"][1]["code"])
    peer = join(conversation_db, created["invitations"][2]["code"])

    def propose(token: str, body: str) -> None:
        """Submit a sealed proposal as one worker."""
        repository.apply(
            conversation_db,
            models.Post(
                workspace=conversation_db.info["workspace"],
                token=token,
                submission_id="idea",
                body=body,
                kind="proposal",
            ),
        )

    propose(worker["token"], "Sealed zebra idea")
    assert search(conversation_db, "zebra") == []
    propose(peer["token"], "Peer idea")
    assert [hit["author"] for hit in search(conversation_db, "zebra")] == ["Worker"]


@pytest.mark.parametrize("directory", ["/does/not/exist", "/etc/hosts"])
def test_directory_must_be_an_existing_directory(directory: str) -> None:
    """The workspace comes only from a real checkout directory."""
    with pytest.raises(ValueError, match="Directory must be an existing directory"):
        operator.checkout_workspace(directory)

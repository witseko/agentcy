"""Apply the human's operator actions through the same conversation rules as members."""

import time
from pathlib import Path

from sqlalchemy import Connection, and_, func, insert, literal_column, select, update

from agents_chat.conversations import (
    assignments,
    models,
    operator_models,
    repository,
    search,
    tasks,
)
from agents_chat.conversations.assignment_tables import PENDING_STATES
from agents_chat.conversations.roles import HUMAN_LABEL, Role
from agents_chat.conversations.tables import member, message, room
from agents_chat.storage.database import workspace_key
from agents_chat.tokens import usage as token_usage

LIST_LIMIT = 100
PAGE_SIZE = 50
TITLE_LENGTH = repository.TITLE_LENGTH
UNKNOWN_CONVERSATION = "Unknown conversation in this workspace"
LEAD_JOIN_PROMPT = (
    "Use agents-chat to join with CODE. You lead this conversation. Read the shared brief and "
    "catch up from cursor zero. Assign tasks with kind='task' and the worker's name in 'to'. "
    "You can add teammates with invite_participant, launch_participant (see list_plugins), or "
    "open_invite, and retire inactive or unneeded teammates with retire_participant. "
    "Track the team status in read_messages, assign follow-up work when workers finish, and post "
    "brief team updates so waiting teammates stay active. Mind teammate token budgets in "
    "team/token_balance and prefer assigning new tasks or handoffs to models with more remaining "
    f"budget. Messages from {HUMAN_LABEL} come from "
    "your user. Follow the bridge's guidance during this turn."
)


def checkout_workspace(directory: str) -> str:
    """Derive the workspace key from an existing absolute checkout path."""
    path = Path(directory)
    if not path.is_absolute():
        raise ValueError("Directory must be an absolute path")
    try:
        return workspace_key(path)
    except (OSError, ValueError):
        raise ValueError("Directory must be an existing directory") from None


def ready_prompt(role: str, code: str, brief: str, participant: str) -> str:
    """Return the join prompt for a slot with its topic, name, and code filled in."""
    template = repository.JOIN_PROMPT if role == Role.WORKER.label else LEAD_JOIN_PROMPT
    return repository.join_prompt(brief, participant=participant, template=template, code=code)


def ready_open_prompt(code: str, brief: str) -> str:
    """Return a ready-to-paste shared open-invite prompt with the code filled in."""
    return repository.open_join_prompt(brief, code=code)


def title(brief_start: str) -> str:
    """Shorten the first line of a brief for a list view."""
    return repository.session_title(brief_start)


def list_conversations(connection: Connection, archived: bool = False) -> dict:
    """Summarize the newest active or archived conversations.

    Conversations without a created time come last.
    """
    slots = and_(member.c.room == room.c.id, member.c.role != Role.HUMAN, member.c.retired == 0)
    members = select(func.count()).select_from(member).where(slots).scalar_subquery()
    joined = (
        select(func.count())
        .select_from(member)
        .where(slots, member.c.token_hash.is_not(None))
        .scalar_subquery()
    )
    rows = connection.execute(
        select(
            room.c.id,
            func.substr(room.c.brief, 1, TITLE_LENGTH + 1).label("brief_start"),
            room.c.open,
            room.c.created,
            members.label("members"),
            joined.label("joined"),
        )
        .where(room.c.archived == int(archived))
        .order_by(
            room.c.created.is_(None),
            room.c.created.desc(),
            literal_column("conversation.rowid").desc(),
        )
        .limit(LIST_LIMIT)
    ).mappings()
    return {
        "conversations": [
            {
                "conversation": row["id"],
                "title": title(row["brief_start"]),
                "open": bool(row["open"]),
                "created": row["created"],
                "members": row["members"],
                "joined": row["joined"],
            }
            for row in rows
        ]
    }


def human_actor(connection: Connection, conversation_id: str) -> dict:
    """Load the conversation with its human in the shape that authenticate returns.

    The human's id is None until its first write to a conversation that a chat created.
    """
    row = (
        connection.execute(
            select(
                room.c.id.label("room"),
                room.c.brief,
                room.c.open,
                room.c.round,
                room.c.round_deadline,
                room.c.created,
                room.c.archived,
                room.c.open_invite_hash,
                member.c.id,
            )
            .select_from(
                room.outerjoin(
                    member, and_(member.c.room == room.c.id, member.c.role == Role.HUMAN)
                )
            )
            .where(room.c.id == conversation_id)
        )
        .mappings()
        .one_or_none()
    )
    if row is None:
        raise ValueError(UNKNOWN_CONVERSATION)
    return {**row, "label": HUMAN_LABEL, "role": Role.HUMAN}


def add_human(connection: Connection, room_id: str) -> int:
    """Add the human participant. It has no token and no invitation."""
    labels = connection.scalars(select(member.c.label).where(member.c.room == room_id))
    if any(label.casefold() == HUMAN_LABEL.casefold() for label in labels):
        raise ValueError(f"A member of this conversation already uses the name {HUMAN_LABEL}")
    return connection.execute(
        insert(member).values(room=room_id, label=HUMAN_LABEL, role=Role.HUMAN)
    ).inserted_primary_key[0]


def writing_actor(connection: Connection, conversation_id: str) -> dict:
    """Return the human, adding it on its first write to a conversation that a chat created."""
    human = human_actor(connection, conversation_id)
    if human["id"] is None:
        human["id"] = add_human(connection, conversation_id)
    return human


def invitation(label: str, role: str, code: str, brief: str) -> dict:
    """Describe one slot's invitation with a ready join prompt."""
    return {
        "participant": label,
        "role": role,
        "code": code,
        "join_prompt": ready_prompt(role, code, brief, label),
    }


def create(
    connection: Connection, workspace: str, request: operator_models.CreateConversation
) -> dict:
    """Create a conversation with an invitation for every slot and add the human."""
    team = models.Create(
        workspace=workspace,
        brief=request.brief,
        initiator=request.lead,
        invitees=request.invitees,
        open_invite=request.open_invite,
        sealed=request.sealed,
        deadline_minutes=request.deadline_minutes,
    )
    roles = repository.team_roles(team)
    room_id, round_state, open_code = repository.insert_room(connection, team)
    invitations = [
        invitation(
            label,
            role.label,
            repository.insert_invitation(connection, room_id, workspace, label, role),
            request.brief,
        )
        for label, role in roles.items()
    ]
    add_human(connection, room_id)
    result = {
        "conversation": room_id,
        "invitations": invitations,
        "roster": repository.roster(connection, room_id),
        **round_state,
    }
    if open_code is not None:
        result["open_invite"] = {
            "code": open_code,
            "join_prompt": ready_open_prompt(open_code, request.brief),
        }
    return result


def transcript(
    connection: Connection,
    room_id: str,
    after: int = 0,
    before: int | None = None,
    tail: bool = False,
) -> dict:
    """Return one page of published messages in ascending ID order.

    Forward reads use ``after``. Backward reads use ``tail=True`` (latest page)
    or ``before=<id>`` (older page) and set ``more`` when older messages exist.
    """
    recipient = member.alias("recipient")
    query = (
        select(
            message.c.id,
            member.c.label,
            message.c.body,
            message.c.created,
            message.c.kind,
            recipient.c.label.label("to"),
            message.c.reply_to,
        )
        .join(member, message.c.author == member.c.id)
        .outerjoin(recipient, message.c.recipient == recipient.c.id)
        .where(message.c.room == room_id)
    )
    backward = tail or before is not None
    if backward:
        if before is not None:
            query = query.where(message.c.id < before)
        query = query.order_by(message.c.id.desc())
    else:
        query = query.where(message.c.id > after).order_by(message.c.id)
    rows = connection.execute(query.limit(PAGE_SIZE + 1)).mappings().all()
    page_rows = list(reversed(rows[:PAGE_SIZE])) if backward else rows[:PAGE_SIZE]
    page = [{field: value for field, value in row.items() if value is not None} for row in page_rows]
    total = (
        connection.scalar(
            select(func.count()).select_from(message).where(message.c.room == room_id)
        )
        or 0
    )
    cursor = page[-1]["id"] if page else (0 if backward else after)
    return {
        "messages": page,
        "more": len(rows) > PAGE_SIZE,
        "cursor": cursor,
        "total_messages": total,
    }


def show(connection: Connection, request: operator_models.ShowConversation) -> dict:
    """Return the brief, member status and one transcript page without adding the human."""
    human = human_actor(connection, request.conversation)
    status = repository.status(connection, human, int(time.time()))
    status.pop("participant")
    return {
        "conversation": request.conversation,
        "brief": human["brief"],
        "created": human["created"],
        "archived": bool(human["archived"]),
        "status": status,
        **transcript(
            connection,
            request.conversation,
            after=request.after,
            before=request.before,
            tail=request.tail,
        ),
    }


def invite(
    connection: Connection, workspace: str, request: operator_models.InviteParticipant
) -> dict:
    """Add or reissue a slot as the human and return its ready join prompt."""
    human = writing_actor(connection, request.conversation)
    result = repository.invite(connection, human, workspace, request.name, request.role)
    return invitation(result["participant"], result["role"], result["code"], human["brief"])


def set_open_invite(
    connection: Connection, workspace: str, request: operator_models.SetOpenInvite
) -> dict:
    """Enable, rotate, or disable a reusable room-level invite as the human."""
    human = writing_actor(connection, request.conversation)
    result = repository.set_open_invite(connection, human, workspace, request.enabled)
    if not result["open_invite"]:
        return result
    return {
        "open_invite": True,
        "code": result["code"],
        "join_prompt": ready_open_prompt(result["code"], human["brief"]),
    }


def launch(
    connection: Connection,
    workspace: str,
    request: operator_models.LaunchParticipant,
    timeout: int,
    *,
    model: str | None = None,
) -> tuple[dict, str | None, str]:
    """Queue a fresh run for a worker slot. Relaunch is not supported yet."""
    human = writing_actor(connection, request.conversation)
    if not human["open"]:
        raise ValueError("Conversation is closed")
    previous = assignments.find_assignment(connection, request.conversation, request.name)
    if previous is not None and previous["state"] not in PENDING_STATES:
        raise ValueError("The fresh run for this slot has ended. Relaunch is not supported yet")
    return assignments.queue(
        connection, human, workspace, request.name, request.plugin, timeout, model=model
    )


def set_member_budget(
    connection: Connection, workspace: str, request: operator_models.SetMemberBudget
) -> dict:
    """Bind a model name and/or per-conversation starting token budget to one participant."""
    human = human_actor(connection, request.conversation)
    if not human["open"]:
        raise ValueError("Conversation is closed")
    rows = connection.execute(
        select(member.c.id, member.c.label, member.c.role, member.c.model, member.c.token_budget)
        .where(member.c.room == request.conversation)
    ).mappings()
    target = next((row for row in rows if row["label"].casefold() == request.name.casefold()), None)
    if target is None:
        raise ValueError("Unknown participant in this conversation")
    if target["role"] == Role.HUMAN:
        raise ValueError(f"Cannot set a token budget for {HUMAN_LABEL}")
    snapshot = token_usage.set_member_budget(
        connection,
        target["id"],
        model=request.model,
        token_budget=request.token_budget,
    )
    token_usage.sync_ledger(workspace, snapshot)
    result: dict[str, object] = {"participant": target["label"]}
    final_model = request.model if request.model is not None else target["model"]
    final_budget = (
        request.token_budget if request.token_budget is not None else target["token_budget"]
    )
    if final_model is not None:
        result["model"] = final_model
    if final_budget is not None:
        result["token_budget"] = final_budget
    return result


def archive(connection: Connection, request: operator_models.ArchiveConversation) -> dict:
    """Archive a closed conversation or return it to the active list. Members keep access."""
    human = human_actor(connection, request.conversation)
    if request.archived and human["open"]:
        raise ValueError("Close the conversation before archiving it")
    connection.execute(
        update(room).where(room.c.id == request.conversation).values(archived=int(request.archived))
    )
    return {"archived": request.archived}


def search_conversations(
    connection: Connection, request: operator_models.SearchConversations
) -> dict:
    """Find the query's words in every brief and published message, best match first."""
    matches = search.find_matches(connection, request.query, TITLE_LENGTH + 1)
    return {
        "hits": [
            {
                "conversation": match["room"],
                "title": title(match["brief_start"]),
                "archived": bool(match["archived"]),
                "message_id": match["message_id"],
                "author": match["author"],
                "created": match["created"],
                "snippet": match["snippet"],
            }
            for match in matches
        ]
    }


def list_tasks(connection: Connection, request: operator_models.ListTasks) -> dict:
    """Return all tasks in one conversation with their state and timeline."""
    human_actor(connection, request.conversation)
    return {"tasks": tasks.list_task_details(connection, request.conversation)}


def _apply_read_or_workspace_action(
    connection: Connection, workspace: str, request: operator_models.OperatorRequest
) -> dict | None:
    """Handle read-only or workspace-level operator actions."""
    if isinstance(request, operator_models.ListConversations):
        return list_conversations(connection, request.archived)
    if isinstance(request, operator_models.SearchConversations):
        return search_conversations(connection, request)
    if isinstance(request, operator_models.ArchiveConversation):
        return archive(connection, request)
    if isinstance(request, operator_models.ShowConversation):
        return show(connection, request)
    if isinstance(request, operator_models.ListTasks):
        return list_tasks(connection, request)
    if isinstance(request, operator_models.CreateConversation):
        return create(connection, workspace, request)
    return None


def apply(connection: Connection, workspace: str, request: operator_models.OperatorRequest) -> dict:
    """Dispatch one operator action that needs only the workspace store."""
    handled = _apply_read_or_workspace_action(connection, workspace, request)
    if handled is not None:
        return handled
    if isinstance(request, operator_models.InviteParticipant):
        return invite(connection, workspace, request)
    if isinstance(request, operator_models.SetOpenInvite):
        return set_open_invite(connection, workspace, request)
    if isinstance(request, operator_models.RetireParticipant):
        human = writing_actor(connection, request.conversation)
        return repository.retire(connection, human, request.name)
    if isinstance(request, operator_models.SetParticipantRole):
        human = writing_actor(connection, request.conversation)
        return repository.set_role(connection, human, request.name, request.role)
    if isinstance(request, operator_models.SetMemberBudget):
        return set_member_budget(connection, workspace, request)
    if isinstance(request, operator_models.PostMessage):
        human = writing_actor(connection, request.conversation)
        return repository.post_in_round(connection, human, request, int(time.time()))
    if isinstance(request, operator_models.CloseConversation):
        human = writing_actor(connection, request.conversation)
        return repository.close(connection, human, request.summary)
    raise TypeError("Unknown operator action")

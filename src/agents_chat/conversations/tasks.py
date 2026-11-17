"""Track task ownership and completion from scoped conversation messages."""

from dataclasses import dataclass

from sqlalchemy import Connection, RowMapping, or_, select

from agents_chat.conversations.roles import Role
from agents_chat.conversations.tables import message

RESPONSE_KINDS = ("accept", "decline")
TASK_EVENT_KINDS = ("task", "handoff", "done", "blocker", *RESPONSE_KINDS)


@dataclass
class Task:
    """Hold the current state of one task built from its messages."""

    owner: int
    blocked: bool = False
    done: bool = False
    handoff: int | None = None


def apply_event(tasks: dict[int, Task], offers: dict[int, Task], event: RowMapping) -> None:
    """Apply a task event in message order."""
    kind = event["kind"]
    if kind == "task":
        tasks[event["id"]] = Task(owner=event["recipient"])
        return
    parent_id = event["reply_to"]
    if kind in RESPONSE_KINDS:
        task = offers.pop(parent_id)
        if kind == "accept":
            task.owner = event["author"]
            task.blocked = False
        task.handoff = None
        return
    task = tasks.get(parent_id)
    if task is None or task.done or task.owner != event["author"]:
        return
    if kind == "handoff":
        task.handoff = event["id"]
        offers[event["id"]] = task
    elif kind == "blocker":
        task.blocked = True
    elif kind == "done":
        task.done = True
        if task.handoff is not None:
            offers.pop(task.handoff)
            task.handoff = None


def read_tasks(connection: Connection, room_id: str, task_id: int | None = None) -> dict[int, Task]:
    """Read all task states in a conversation or the events for one task."""
    query = select(
        message.c.id, message.c.kind, message.c.author, message.c.recipient, message.c.reply_to
    ).where(
        message.c.room == room_id,
        message.c.kind.in_(TASK_EVENT_KINDS),
        or_(message.c.kind == "task", message.c.reply_to.is_not(None)),
    )
    if task_id is not None:
        handoff_ids = select(message.c.id).where(
            message.c.room == room_id, message.c.kind == "handoff", message.c.reply_to == task_id
        )
        query = query.where(
            or_(
                message.c.id == task_id,
                message.c.reply_to == task_id,
                message.c.reply_to.in_(handoff_ids),
            )
        )
    tasks: dict[int, Task] = {}
    offers: dict[int, Task] = {}
    for event in connection.execute(query.order_by(message.c.id)).mappings():
        apply_event(tasks, offers, event)
    return tasks


def open_tasks(connection: Connection, room_id: str) -> dict[int, list[dict]]:
    """Group unfinished task IDs and blocker flags by current owner."""
    by_owner: dict[int, list[dict]] = {}
    for task_id, task in read_tasks(connection, room_id).items():
        if task.done:
            continue
        summary = {"id": task_id}
        if task.blocked:
            summary["blocked"] = True
        by_owner.setdefault(task.owner, []).append(summary)
    return by_owner


def validate_transition(connection: Connection, participant: RowMapping, content: dict) -> None:
    """Check role and ownership before adding a new task or handoff message."""
    kind = content["kind"]
    if kind == "task":
        if participant["role"] not in (Role.LEAD, Role.CO_LEAD):
            raise ValueError("Only a lead or co-lead can assign a task")
        if content["recipient"] is None:
            raise ValueError("A task requires an addressee")
    elif kind == "handoff":
        validate_handoff(connection, participant, content)
    elif kind in RESPONSE_KINDS:
        validate_response(connection, participant, content["reply_to"])


def validate_handoff(connection: Connection, participant: RowMapping, content: dict) -> None:
    """Allow the current owner to offer an open task to another member."""
    if content["recipient"] in (None, participant["id"]):
        raise ValueError("A handoff requires another member as addressee")
    task_id = content["reply_to"]
    if task_id is None:
        raise ValueError("A handoff must reply to an open task")
    task = read_tasks(connection, participant["room"], task_id).get(task_id)
    if task is None or task.done:
        raise ValueError("A handoff must reply to an open task")
    if task.owner != participant["id"]:
        raise ValueError("Only the task owner can offer a handoff")
    if task.handoff is not None:
        raise ValueError("This task already has a pending handoff")


def validate_response(
    connection: Connection, participant: RowMapping, handoff_id: int | None
) -> None:
    """Allow only the receiver to answer an outstanding handoff."""
    task_id = connection.scalar(
        select(message.c.reply_to).where(
            message.c.room == participant["room"],
            message.c.id == handoff_id,
            message.c.kind == "handoff",
            message.c.recipient == participant["id"],
        )
    )
    if task_id is None:
        raise ValueError("A handoff response must reply to a handoff addressed to you")
    task = read_tasks(connection, participant["room"], task_id).get(task_id)
    if task is None or task.done or task.handoff != handoff_id:
        raise ValueError("This handoff is no longer pending")

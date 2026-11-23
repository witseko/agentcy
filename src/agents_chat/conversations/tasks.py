"""Track task ownership and completion from scoped conversation messages."""

import time
from dataclasses import dataclass

from sqlalchemy import Connection, RowMapping, and_, insert, or_, select

from agents_chat.conversations.assignment_tables import PENDING_STATES, assignment
from agents_chat.conversations.roles import Role
from agents_chat.conversations.tables import member, message

RESPONSE_KINDS = ("accept", "decline")
# Only the core writes this kind, when it moves a retired member's tasks; clients cannot post it.
REASSIGN_KIND = "reassign"
TASK_EVENT_KINDS = ("task", "handoff", "done", "blocker", "resume", REASSIGN_KIND, *RESPONSE_KINDS)
TASK_TITLE_LENGTH = 120


def task_title(body: str) -> str:
    """Extract a single-line summary from the first non-empty line of a task body."""
    for raw_line in body.splitlines():
        line = " ".join(raw_line.split())
        if not line:
            continue
        if len(line) > TASK_TITLE_LENGTH:
            return line[: TASK_TITLE_LENGTH - 1].rstrip() + "…"
        return line
    return ""


@dataclass
class Task:
    """Hold the current state of one task built from its messages."""

    owner: int
    blocked: bool = False
    done: bool = False
    handoff: int | None = None
    due: int | None = None


def due_time(event: RowMapping) -> int | None:
    """Return the Unix time a task message is due, or None for a task stored without a timeline."""
    if event["due_minutes"] is None:
        return None
    return event["created"] + event["due_minutes"] * 60


def is_overdue(task: Task, now: int) -> bool:
    """Recognize an unfinished task whose due time has passed."""
    return not task.done and task.due is not None and task.due < now


def validate_due_minutes(kind: str, due_minutes: int | None) -> None:
    """Require a timeline on every new task and reject one on any other message kind."""
    if kind == "task" and due_minutes is None:
        raise ValueError(
            "A task requires a timeline. Set due_minutes to the number of minutes you expect "
            "the task to take"
        )
    if kind != "task" and due_minutes is not None:
        raise ValueError("Only a kind='task' message accepts due_minutes")


def _apply_response(offers: dict[int, Task], event: RowMapping) -> None:
    """Resolve an accept or decline reply against a pending handoff offer."""
    task = offers.pop(event["reply_to"], None)
    if task is None:
        return
    if event["kind"] == "accept":
        task.owner = event["author"]
        task.blocked = False
    task.handoff = None


def _apply_reassign(task: Task | None, offers: dict[int, Task], event: RowMapping) -> None:
    """Give an open task to its new owner and drop any pending handoff and blocker."""
    if task is None or task.done:
        return
    task.owner = event["recipient"]
    task.blocked = False
    if task.handoff is not None:
        offers.pop(task.handoff, None)
        task.handoff = None


def _can_modify_task(
    task: Task,
    event: RowMapping,
    leader_ids: set[int],
    inactive_owners: set[int],
) -> bool:
    """Allow the owner or a lead recovering an inactive owner's task to modify task state."""
    if task.owner == event["author"]:
        return True
    return (
        event["kind"] in ("handoff", "done")
        and event["author"] in leader_ids
        and task.owner in inactive_owners
    )


def apply_event(
    tasks: dict[int, Task],
    offers: dict[int, Task],
    event: RowMapping,
    *,
    leader_ids: set[int] = frozenset(),
    inactive_owners: set[int] = frozenset(),
) -> None:
    """Apply a task event in message order."""
    kind = event["kind"]
    if kind == "task":
        tasks[event["id"]] = Task(owner=event["recipient"], due=due_time(event))
        return
    if kind in RESPONSE_KINDS:
        _apply_response(offers, event)
        return
    task = tasks.get(event["reply_to"])
    if kind == REASSIGN_KIND:
        _apply_reassign(task, offers, event)
        return
    if task is None or task.done or not _can_modify_task(task, event, leader_ids, inactive_owners):
        return
    if kind == "handoff":
        task.handoff = event["id"]
        offers[event["id"]] = task
    elif kind in ("blocker", "resume"):
        task.blocked = kind == "blocker"
    elif kind == "done":
        task.done = True
        if task.handoff is not None:
            offers.pop(task.handoff)
            task.handoff = None


def _room_recovery_sets(connection: Connection, room_id: str) -> tuple[set[int], set[int]]:
    """Return leader IDs and inactive (retired or ended fresh-run) member IDs for a room."""
    rows = connection.execute(
        select(member.c.id, member.c.role, member.c.retired, assignment.c.state.label("launch"))
        .outerjoin(
            assignment,
            and_(
                assignment.c.slot == member.c.id,
                assignment.c.invite_hash == member.c.invite_hash,
            ),
        )
        .where(member.c.room == room_id)
    ).mappings()
    leaders: set[int] = set()
    inactive: set[int] = set()
    for row in rows:
        if row["role"] in (Role.LEAD, Role.CO_LEAD) and not row["retired"]:
            leaders.add(row["id"])
        launch = row["launch"]
        if row["retired"] or (launch is not None and launch not in PENDING_STATES):
            inactive.add(row["id"])
    return leaders, inactive


def owner_is_inactive(connection: Connection, owner_id: int) -> bool:
    """Recognize a task owner who is retired or whose fresh run has ended."""
    row = (
        connection.execute(
            select(member.c.retired, assignment.c.state.label("launch"))
            .outerjoin(
                assignment,
                and_(
                    assignment.c.slot == member.c.id,
                    assignment.c.invite_hash == member.c.invite_hash,
                ),
            )
            .where(member.c.id == owner_id)
        )
        .mappings()
        .one_or_none()
    )
    if row is None:
        return False
    launch = row["launch"]
    return bool(row["retired"]) or (launch is not None and launch not in PENDING_STATES)


def _available_workers(connection: Connection, room_id: str) -> list[RowMapping]:
    """Return active workers that joined, hold a live invitation, or have a running launch."""
    now = time.time()
    rows = connection.execute(
        select(
            member.c.id,
            member.c.label,
            member.c.token_hash,
            member.c.expires,
            assignment.c.state.label("launch"),
        )
        .outerjoin(
            assignment,
            and_(
                assignment.c.slot == member.c.id,
                assignment.c.invite_hash == member.c.invite_hash,
            ),
        )
        .where(member.c.room == room_id, member.c.role == Role.WORKER, member.c.retired == 0)
        .order_by(member.c.id)
    ).mappings()
    available = []
    for row in rows:
        launch = row["launch"]
        if launch is not None and launch not in PENDING_STATES:
            continue
        if row["token_hash"] is None and launch is None and row["expires"] <= now:
            continue
        available.append(row)
    return available


def _fallback_leader(connection: Connection, actor: RowMapping) -> RowMapping:
    """Return the active lead, else the acting co-lead, else the first active co-lead."""
    leaders = (
        connection.execute(
            select(member.c.id, member.c.label, member.c.role)
            .where(
                member.c.room == actor["room"],
                member.c.role.in_((Role.LEAD, Role.CO_LEAD)),
                member.c.retired == 0,
            )
            .order_by(member.c.id)
        )
        .mappings()
        .all()
    )
    for leader in leaders:
        if leader["role"] == Role.LEAD:
            return leader
    for leader in leaders:
        if leader["id"] == actor["id"]:
            return leader
    if not leaders:
        raise ValueError("No active lead can take over the retired participant's tasks")
    return leaders[0]


def redelegate_tasks(
    connection: Connection, actor: RowMapping, retired_id: int, retired_label: str
) -> list[dict]:
    """Move a retired member's unfinished tasks to the least busy available worker.

    When no worker is available, the lead takes the tasks. Each move is recorded as a
    reassign message addressed to the new owner, so the new owner is woken.

    Returns:
        One {"task", "to"} entry per moved task.
    """
    by_owner = open_tasks(connection, actor["room"])
    owned = by_owner.get(retired_id, [])
    if not owned:
        return []
    workers = _available_workers(connection, actor["room"])
    load = {worker["id"]: len(by_owner.get(worker["id"], [])) for worker in workers}
    fallback = None if workers else _fallback_leader(connection, actor)
    moved = []
    for summary in owned:
        if workers:
            receiver = min(workers, key=lambda worker: (load[worker["id"]], worker["id"]))
            load[receiver["id"]] += 1
        else:
            receiver = fallback
        task_id = summary["id"]
        connection.execute(
            insert(message).values(
                room=actor["room"],
                author=actor["id"],
                recipient=receiver["id"],
                reply_to=task_id,
                kind=REASSIGN_KIND,
                body=(
                    f"Reassigned task #{task_id} from {retired_label} to {receiver['label']} "
                    f"because {retired_label} was retired."
                ),
                created=int(time.time()),
            )
        )
        moved.append({"task": task_id, "to": receiver["label"]})
    return moved


def read_tasks(connection: Connection, room_id: str, task_id: int | None = None) -> dict[int, Task]:
    """Read all task states in a conversation or the events for one task."""
    query = select(
        message.c.id,
        message.c.kind,
        message.c.author,
        message.c.recipient,
        message.c.reply_to,
        message.c.created,
        message.c.due_minutes,
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
    leader_ids, inactive_owners = _room_recovery_sets(connection, room_id)
    tasks: dict[int, Task] = {}
    offers: dict[int, Task] = {}
    for event in connection.execute(query.order_by(message.c.id)).mappings():
        apply_event(
            tasks,
            offers,
            event,
            leader_ids=leader_ids,
            inactive_owners=inactive_owners,
        )
    return tasks


def open_tasks(
    connection: Connection, room_id: str, now: int | None = None
) -> dict[int, list[dict]]:
    """Group unfinished task IDs with blocker flags, due times, and overdue flags by current owner.

    Args:
        now: The Unix time used to flag overdue tasks. Defaults to the current time.
    """
    if now is None:
        now = int(time.time())
    by_owner: dict[int, list[dict]] = {}
    for task_id, task in read_tasks(connection, room_id).items():
        if task.done:
            continue
        summary = {"id": task_id}
        if task.blocked:
            summary["blocked"] = True
        if task.due is not None:
            summary["due"] = task.due
        if is_overdue(task, now):
            summary["overdue"] = True
        by_owner.setdefault(task.owner, []).append(summary)
    return by_owner


def _task_event_payload(row: RowMapping) -> dict[str, object]:
    """Format one message row as a task timeline event."""
    payload: dict[str, object] = {
        "id": row["id"],
        "author": row["author_label"],
        "body": row["body"],
        "created": row["created"],
    }
    if row["kind"] is not None:
        payload["kind"] = row["kind"]
    if row["to"] is not None:
        payload["to"] = row["to"]
    if row["reply_to"] is not None:
        payload["reply_to"] = row["reply_to"]
    return payload


def _task_state_label(task: Task) -> str:
    """Return the external state name of one task."""
    if task.done:
        return "done"
    if task.blocked:
        return "blocked"
    return "open"


def list_task_details(connection: Connection, room_id: str) -> list[dict[str, object]]:
    """Return every task in a conversation with its owner, state, due time, and event history."""
    task_ids = select(message.c.id).where(message.c.room == room_id, message.c.kind == "task")
    handoff_ids = select(message.c.id).where(
        message.c.room == room_id,
        message.c.kind == "handoff",
        message.c.reply_to.in_(task_ids),
    )
    recipient = member.alias("recipient")
    query = (
        select(
            message.c.id,
            message.c.kind,
            message.c.author,
            member.c.label.label("author_label"),
            message.c.recipient,
            recipient.c.label.label("to"),
            message.c.body,
            message.c.created,
            message.c.reply_to,
            message.c.due_minutes,
        )
        .join(member, message.c.author == member.c.id)
        .outerjoin(recipient, message.c.recipient == recipient.c.id)
        .where(
            message.c.room == room_id,
            or_(
                message.c.id.in_(task_ids),
                message.c.reply_to.in_(task_ids),
                message.c.reply_to.in_(handoff_ids),
            ),
        )
        .order_by(message.c.id)
    )
    tasks: dict[int, Task] = {}
    offers: dict[int, Task] = {}
    origins: dict[int, RowMapping] = {}
    events_by_task: dict[int, list[dict[str, object]]] = {}
    handoff_to_task: dict[int, int] = {}
    handoff_target: dict[int, str] = {}
    labels: dict[int, str] = {}
    leaders, inactive = _room_recovery_sets(connection, room_id)
    now = int(time.time())
    for row in connection.execute(query).mappings():
        labels[row["author"]] = row["author_label"]
        if row["recipient"] is not None and row["to"] is not None:
            labels[row["recipient"]] = row["to"]
        task_id = _resolve_event_task(row, origins, handoff_to_task, handoff_target)
        if task_id is None:
            continue
        events_by_task.setdefault(task_id, []).append(_task_event_payload(row))
        if row["kind"] in TASK_EVENT_KINDS:
            apply_event(
                tasks,
                offers,
                row,
                leader_ids=leaders,
                inactive_owners=inactive,
            )
    results: list[dict[str, object]] = []
    for task_id in sorted(origins, reverse=True):
        task = tasks[task_id]
        origin = origins[task_id]
        events = events_by_task[task_id]
        detail: dict[str, object] = {
            "id": task_id,
            "title": task_title(origin["body"]),
            "body": origin["body"],
            "author": origin["author_label"],
            "owner": labels[task.owner],
            "state": _task_state_label(task),
            "created": origin["created"],
            "updated": events[-1]["created"],
            "events": events,
        }
        if task.handoff is not None and task.handoff in handoff_target:
            detail["pending_handoff_to"] = handoff_target[task.handoff]
        if task.due is not None:
            detail["due"] = task.due
        if is_overdue(task, now):
            detail["overdue"] = True
        results.append(detail)
    return results


def _resolve_event_task(
    row: RowMapping,
    origins: dict[int, RowMapping],
    handoff_to_task: dict[int, int],
    handoff_target: dict[int, str],
) -> int | None:
    """Associate one queried message row with its root task ID."""
    kind = row["kind"]
    if kind == "task":
        origins[row["id"]] = row
        return row["id"]
    parent_id = row["reply_to"]
    if parent_id in origins:
        if kind == "handoff":
            handoff_to_task[row["id"]] = parent_id
            if row["to"] is not None:
                handoff_target[row["id"]] = row["to"]
        return parent_id
    return handoff_to_task.get(parent_id)


def validate_transition(connection: Connection, participant: RowMapping, content: dict) -> None:
    """Check role and ownership before adding a new task or handoff message."""
    kind = content["kind"]
    if kind in ("task", "handoff"):
        validate_task_recipient(connection, content["recipient"])
    if kind == "task":
        if participant["role"] not in (Role.LEAD, Role.CO_LEAD):
            raise ValueError("Only a lead or co-lead can assign a task")
        if content["recipient"] is None:
            raise ValueError("A task requires an addressee")
    elif kind == "handoff":
        validate_handoff(connection, participant, content)
    elif kind == "resume":
        validate_resume(connection, participant, content["reply_to"])
    elif kind in RESPONSE_KINDS:
        validate_response(connection, participant, content["reply_to"])


def validate_task_recipient(connection: Connection, recipient: int | None) -> None:
    """Reject assigning tasks or handoffs to the human or a retired member."""
    if recipient is None:
        return
    row = (
        connection.execute(
            select(member.c.role, member.c.retired).where(member.c.id == recipient)
        )
        .mappings()
        .one_or_none()
    )
    if row is None:
        return
    if row["role"] == Role.HUMAN:
        raise ValueError(f"The {Role.HUMAN.label} cannot own a task. Address it to a member")
    if row["retired"]:
        raise ValueError("Cannot assign a task or handoff to a retired participant")


def validate_resume(connection: Connection, participant: RowMapping, task_id: int | None) -> None:
    """Allow only the current owner to clear a blocker on the original task."""
    if task_id is None:
        raise ValueError("A resume must reply to the original open task")
    task = read_tasks(connection, participant["room"], task_id).get(task_id)
    if task is None or task.done:
        raise ValueError("A resume must reply to the original open task")
    if task.owner != participant["id"]:
        raise ValueError("Only the task owner can resume work")
    if not task.blocked:
        raise ValueError("This task is not blocked")


def changes_blocked_state(connection: Connection, participant: RowMapping, content: dict) -> bool:
    """Recognize a new transition even when its text matches an earlier block or resume."""
    kind = content["kind"]
    task_id = content["reply_to"]
    if kind not in ("blocker", "resume") or task_id is None:
        return False
    task = read_tasks(connection, participant["room"], task_id).get(task_id)
    return bool(
        task
        and not task.done
        and task.owner == participant["id"]
        and task.blocked != (kind == "blocker")
    )


def validate_handoff(connection: Connection, participant: RowMapping, content: dict) -> None:
    """Allow the current owner (or a lead recovering an inactive owner's task) to offer a handoff."""
    if content["recipient"] in (None, participant["id"]):
        raise ValueError("A handoff requires another member as addressee")
    task_id = content["reply_to"]
    if task_id is None:
        raise ValueError("A handoff must reply to an open task")
    task = read_tasks(connection, participant["room"], task_id).get(task_id)
    if task is None or task.done:
        raise ValueError("A handoff must reply to an open task")
    can_recover = participant["role"] in (Role.LEAD, Role.CO_LEAD) and owner_is_inactive(
        connection, task.owner
    )
    if task.owner != participant["id"] and not can_recover:
        raise ValueError("Only the task owner can offer a handoff")
    if task.handoff is not None:
        raise ValueError("This task already has a pending handoff")


def unclosed_done_warning(owned_before: list[dict], owned_after: list[dict]) -> str | None:
    """Warn when a kind='done' post did not close any of the author's open tasks."""
    if not owned_after or len(owned_after) < len(owned_before):
        return None
    ids = ", ".join(f"#{task['id']}" for task in owned_after)
    return f"Open task(s) {ids} remain open. Reply with reply_to=<task_id> and kind='done' to close."


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

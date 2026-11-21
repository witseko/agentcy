"""Identify the participants whose work is outstanding."""

from agents_chat.conversations.assignment_tables import PENDING_STATES
from agents_chat.conversations.roles import Role


def awaiting_fresh_join(member: dict) -> bool:
    """Recognize a launch that still owns an unused invitation and can join."""
    return member["state"] == "invited" and member.get("launch_state") in PENDING_STATES


def fresh_run_ended(member: dict) -> bool:
    """Recognize a launch that the bridge no longer tracks as active."""
    launch_state = member.get("launch_state")
    return launch_state is not None and launch_state not in PENDING_STATES


def member_is_working(member: dict) -> bool:
    """Recognize a peer who can still contribute through the current seat."""
    return not fresh_run_ended(member) and (
        member["state"] in ("joined", "replied") or awaiting_fresh_join(member)
    )


def task_owners(status: dict) -> dict[str, dict]:
    """Find members with unfinished tasks, including blocked work."""
    return {name: member for name, member in status["members"].items() if member.get("open_tasks")}


def task_availability(owners: dict[str, dict]) -> tuple[set[str], bool]:
    """Identify task owners who can work and blockers that a live owner could clear."""
    working = set()
    blocked = False
    for name, member in owners.items():
        if fresh_run_ended(member) or (
            member["state"] == "invited" and not awaiting_fresh_join(member)
        ):
            continue
        for task in member["open_tasks"]:
            if task.get("blocked"):
                blocked = True
            else:
                working.add(name)
    return working, blocked


def working_peers_without_tasks(members: dict, owners: dict, caller: str) -> list[str]:
    """Find working peers whose progress is independent of the current task list."""
    return [
        name
        for name, member in members.items()
        if name != caller and name not in owners and member_is_working(member)
    ]


def available_workers(members: dict, owners: dict, caller: str) -> list[str]:
    """Find live peers who finished their work, hold no open tasks, and can take new work."""
    return [
        name
        for name, member in members.items()
        if name != caller
        and member["role"] != Role.HUMAN.label
        and name not in owners
        and member["state"] == "done"
        and not fresh_run_ended(member)
    ]


def is_current_blocker(message: dict, members: dict, task_blocker: bool) -> bool:
    """Check whether a received blocker still represents unfinished work."""
    if message.get("kind") != "blocker":
        return False
    author = members[message["label"]]
    if fresh_run_ended(author):
        return False
    if not task_blocker:
        return author["state"] == "blocked"
    return any(
        task["id"] == message["reply_to"] and task.get("blocked")
        for task in author.get("open_tasks", [])
    )


def lead_waiting_on(status: dict, caller: str) -> list[str]:
    """List the most urgent group of peers with incomplete work."""
    members = status["members"]
    if members[caller]["role"] == Role.WORKER.label:
        return []
    owners = task_owners(status)
    working_owners, _ = task_availability(owners)
    pending_owners = [
        name for name, member in owners.items() if name != caller and not fresh_run_ended(member)
    ]
    if owners:
        working = [name for name in pending_owners if name in working_owners]
        pending = working or working_peers_without_tasks(members, owners, caller) or pending_owners
        if pending:
            return pending
    working = []
    blocked = []
    invited = []
    for name, member in members.items():
        if name == caller or fresh_run_ended(member):
            continue
        if member_is_working(member):
            working.append(name)
        elif member["state"] == "blocked":
            blocked.append(name)
        elif member["state"] == "invited" and "launch_state" not in member:
            invited.append(name)
    return working or blocked or invited

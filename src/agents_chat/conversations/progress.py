"""Identify the participants whose work is outstanding."""

from agents_chat.conversations.roles import Role


def task_owners(status: dict) -> dict[str, dict]:
    """Find members with unfinished tasks, including blocked work."""
    return {name: member for name, member in status["members"].items() if member.get("open_tasks")}


def lead_waiting_on(status: dict, caller: str) -> list[str]:
    """List the most urgent group of peers with incomplete work."""
    members = status["members"]
    if members[caller]["role"] == Role.WORKER.label:
        return []
    owners = [name for name in task_owners(status) if name != caller]
    if owners:
        return owners
    working = []
    blocked = []
    invited = []
    for name, member in members.items():
        if name == caller:
            continue
        if member["state"] in ("joined", "replied"):
            working.append(name)
        elif member["state"] == "blocked":
            blocked.append(name)
        elif member["state"] == "invited":
            invited.append(name)
    return working or blocked or invited

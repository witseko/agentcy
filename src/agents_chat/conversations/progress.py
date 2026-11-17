"""Identify the participants whose work is outstanding."""

from agents_chat.conversations.roles import Role


def lead_waiting_on(status: dict, caller: str) -> list[str]:
    """List the most urgent group of peers with incomplete work."""
    members = status["members"]
    if members[caller]["role"] == Role.WORKER.label:
        return []
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

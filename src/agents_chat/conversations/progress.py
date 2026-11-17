"""Identify the participants whose work is outstanding."""

from agents_chat.conversations.roles import Role


def lead_waiting_on(status: dict, caller: str) -> list[str]:
    """List working peers first, or unused invitations when nobody is working."""
    members = status["members"]
    if members[caller]["role"] == Role.WORKER.label:
        return []
    working = []
    invited = []
    for name, member in members.items():
        if name == caller:
            continue
        if member["state"] == "joined":
            working.append(name)
        elif member["state"] == "invited":
            invited.append(name)
    return working or invited

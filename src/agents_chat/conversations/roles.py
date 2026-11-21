"""Define conversation roles and their stable SQLite values."""

from enum import IntEnum

# Models cannot use this name, so a message from it always comes from the operator protocol.
HUMAN_LABEL = "Human"


class Role(IntEnum):
    """Keep the original worker and creator values when migrating old databases."""

    WORKER = 0
    LEAD = 1
    CO_LEAD = 2
    # The person at the operator protocol. The row has no token and no invitation.
    HUMAN = 3

    @property
    def label(self) -> str:
        """Return the role name used by chat clients."""
        return self.name.lower().replace("_", "-")

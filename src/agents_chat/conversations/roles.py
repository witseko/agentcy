"""Define conversation roles and their stable SQLite values."""

from enum import IntEnum


class Role(IntEnum):
    """Keep the original worker and creator values when migrating old databases."""

    WORKER = 0
    LEAD = 1
    CO_LEAD = 2

    @property
    def label(self) -> str:
        """Return the role name used by chat clients."""
        return self.name.lower().replace("_", "-")

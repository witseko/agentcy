"""Find POSIX executables through absolute search directories."""

import os
import shutil


def absolute_search_path() -> str:
    """Keep the configured search order while dropping relative directories."""
    return os.pathsep.join(
        directory for directory in os.get_exec_path() if os.path.isabs(directory)
    )


def find_on_path(name: str) -> str | None:
    """Find a bare program name in the absolute entries of the configured PATH."""
    if not name or os.path.dirname(name):
        return None
    search_path = absolute_search_path()
    return shutil.which(name, path=search_path) if search_path else None

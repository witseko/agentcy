"""Choose the shim and core commands the e2e tests run, so they can test any build.

AGENTCY_BIN is the absolute path of the shim executable. AGENTCY_CORE_CMD is the core
command, split like a shell command line but never run through a shell. Both are required,
for example AGENTCY_BIN="$PWD/bin/agentcy" and AGENTCY_CORE_CMD="$PWD/bin/agentcy core".
"""

import os
import shlex
import shutil
from pathlib import Path


def shim_command() -> str:
    """Return the absolute path of the shim executable.

    Raises:
        ValueError: If AGENTCY_BIN is unset or set to a relative path.
    """
    configured = os.environ.get("AGENTCY_BIN")
    if not configured:
        raise ValueError('AGENTCY_BIN is unset: set it to the shim, for example "$PWD/bin/agentcy"')
    if not Path(configured).is_absolute():
        raise ValueError("AGENTCY_BIN must be an absolute path")
    return configured


def core_command() -> list[str]:
    """Return the core command as an argument list with an absolute executable.

    The tests start the core with a minimal PATH, so a bare program name is resolved
    here against the PATH of the test run.

    Raises:
        ValueError: If AGENTCY_CORE_CMD is unset or names no executable that can be found.
    """
    configured = os.environ.get("AGENTCY_CORE_CMD")
    if not configured:
        raise ValueError(
            'AGENTCY_CORE_CMD is unset: set it to the core, for example "$PWD/bin/agentcy core"'
        )
    words = shlex.split(configured)
    executable = shutil.which(words[0]) if words else None
    if executable is None:
        raise ValueError("AGENTCY_CORE_CMD names no executable program")
    return [executable, *words[1:]]

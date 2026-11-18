"""Verify the shim's cold-start command."""

import subprocess
import sys
from pathlib import Path
from unittest.mock import Mock, sentinel

import pytest

from agents_chat.mcp import transport


def test_cold_start_uses_isolated_python(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    """Start with the current interpreter and ignore workspace-relative import paths."""
    home = tmp_path / "bridge"
    home.mkdir(mode=0o700)
    workspace = tmp_path / "workspace"
    workspace.mkdir()
    monkeypatch.chdir(workspace)
    monkeypatch.setenv("AGENTS_CHAT_HOME", str(home))
    monkeypatch.setenv("PYTHONPATH", ".")
    connect = Mock(side_effect=[FileNotFoundError, FileNotFoundError, sentinel.connection])
    start = Mock(return_value=Mock(poll=Mock(return_value=None)))
    with monkeypatch.context() as launcher:
        launcher.setattr(transport, "connect", connect)
        launcher.setattr(transport.subprocess, "Popen", start)
        assert transport.core_connection() is sentinel.connection
    start.assert_called_once()
    command = start.call_args.args[0]
    assert command == [sys.executable, "-I", "-m", "agents_chat.mcp.core"]
    probe = subprocess.run(  # noqa: S603 - current interpreter with fixed probe code
        [*command[:2], "-c", "import sys\nprint(sys.flags.no_user_site, *sys.path, sep='\\n')"],
        cwd=workspace,
        env=start.call_args.kwargs["env"],
        capture_output=True,
        check=True,
        text=True,
        timeout=5,
    )
    no_user_site, *search_paths = probe.stdout.splitlines()
    assert no_user_site == "1"
    assert str(workspace) not in search_paths

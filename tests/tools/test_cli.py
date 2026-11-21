"""Check the command line: the shim invocation and the terminal UI start path."""

import subprocess
import sys
from pathlib import Path

import pytest

from agents_chat.mcp import server
from agents_chat.storage.database import workspace_key
from agents_chat.tools import cli


class FakeConnection:
    """Stand in for the socket that proves the core is running."""

    def __init__(self) -> None:
        self.closed = False

    def close(self) -> None:
        """Record that the probe connection was closed."""
        self.closed = True


@pytest.fixture
def private_home(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> Path:
    """Point the bridge at a private data directory and an empty interpreter folder."""
    home = tmp_path / "home"
    home.mkdir(mode=0o700)
    monkeypatch.setenv("AGENTS_CHAT_HOME", str(home))
    interpreter = tmp_path / "venv" / "bin"
    interpreter.mkdir(parents=True)
    monkeypatch.setattr(cli.sys, "executable", str(interpreter / "python"))
    return home


def fake_binary(directory: Path) -> Path:
    """Create an executable file that stands in for the UI binary."""
    binary = directory / "agents-chat-tui"
    binary.write_text("#!/bin/sh\n", encoding="utf-8")
    binary.chmod(0o700)
    return binary


def test_ui_execs_the_binary_with_the_absolute_workspace(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, private_home: Path
) -> None:
    """The UI starts the core first, then replaces the process with the binary."""
    binary = fake_binary(tmp_path)
    workspace = tmp_path / "workspace"
    workspace.mkdir()
    monkeypatch.chdir(workspace)
    monkeypatch.setenv("AGENTS_CHAT_TUI", str(binary))
    probe = FakeConnection()
    monkeypatch.setattr(cli, "core_connection", lambda: probe)
    calls = []
    monkeypatch.setattr(cli.os, "execv", lambda path, argv: calls.append((path, argv)))

    cli.main(["ui"])

    assert probe.closed
    assert calls == [(str(binary), [str(binary), "--workspace", str(workspace.resolve())])]


def test_ui_finds_the_binary_on_path(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, private_home: Path
) -> None:
    """Without AGENTS_CHAT_TUI the binary comes from an absolute PATH entry."""
    binary = fake_binary(tmp_path)
    monkeypatch.delenv("AGENTS_CHAT_TUI", raising=False)
    monkeypatch.setenv("PATH", str(tmp_path))
    monkeypatch.setattr(cli, "core_connection", FakeConnection)
    calls = []
    monkeypatch.setattr(cli.os, "execv", lambda path, argv: calls.append((path, argv)))

    cli.main(["ui", "--workspace", str(tmp_path)])

    assert calls == [(str(binary), [str(binary), "--workspace", str(tmp_path.resolve())])]


def test_ui_prefers_the_binary_next_to_the_interpreter(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, private_home: Path
) -> None:
    """make setup puts the binary in the venv's bin folder, which wins over PATH."""
    installed = fake_binary(Path(cli.sys.executable).parent)
    on_path = tmp_path / "path"
    on_path.mkdir()
    fake_binary(on_path)
    monkeypatch.delenv("AGENTS_CHAT_TUI", raising=False)
    monkeypatch.setenv("PATH", str(on_path))
    monkeypatch.setattr(cli, "core_connection", FakeConnection)
    calls = []
    monkeypatch.setattr(cli.os, "execv", lambda path, argv: calls.append((path, argv)))

    cli.main(["ui", "--workspace", str(tmp_path)])

    assert calls == [(str(installed), [str(installed), "--workspace", str(tmp_path.resolve())])]


def test_missing_binary_says_how_to_build_it(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, private_home: Path
) -> None:
    """A missing binary stops before the core starts, with build instructions."""
    monkeypatch.delenv("AGENTS_CHAT_TUI", raising=False)
    monkeypatch.setenv("PATH", str(tmp_path))

    def unexpected() -> None:
        raise AssertionError("core started without a UI binary")

    monkeypatch.setattr(cli, "core_connection", unexpected)

    with pytest.raises(SystemExit) as stopped:
        cli.main(["ui", "--workspace", str(tmp_path)])

    assert "make setup" in str(stopped.value)
    assert "AGENTS_CHAT_TUI" in str(stopped.value)


def test_relative_tui_override_is_rejected(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, private_home: Path
) -> None:
    """AGENTS_CHAT_TUI must name an absolute executable path."""
    monkeypatch.setenv("AGENTS_CHAT_TUI", "agents-chat-tui")

    with pytest.raises(SystemExit, match="absolute path"):
        cli.main(["ui", "--workspace", str(tmp_path)])


def test_workspace_flag_still_starts_the_shim(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, private_home: Path
) -> None:
    """Host registrations keep using `agents-chat --workspace DIR`."""
    calls = []
    monkeypatch.setattr(server, "serve", lambda key, directory: calls.append((key, directory)))

    cli.main(["--workspace", str(tmp_path)])

    assert calls == [(workspace_key(tmp_path), tmp_path.resolve())]


def test_cli_import_leaves_the_mcp_server_unloaded() -> None:
    """The UI path does not import the MCP server module."""
    check = "import sys, agents_chat.tools.cli; sys.exit('agents_chat.mcp.server' in sys.modules)"
    # The test's own interpreter runs a fixed script with no shell.
    result = subprocess.run([sys.executable, "-I", "-c", check], check=False)  # noqa: S603
    assert result.returncode == 0

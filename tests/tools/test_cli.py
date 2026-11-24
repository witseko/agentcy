"""Check the command line: the shim invocation and the terminal UI start path."""

import subprocess
import sys
from importlib.metadata import version
from pathlib import Path

import pytest

from agentcy.mcp import server
from agentcy.mcp.http_transport import HttpSettings
from agentcy.storage.database import workspace_key
from agentcy.tools import cli


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
    monkeypatch.setenv("AGENTCY_HOME", str(home))
    interpreter = tmp_path / "venv" / "bin"
    interpreter.mkdir(parents=True)
    monkeypatch.setattr(cli.sys, "executable", str(interpreter / "python"))
    return home


def fake_binary(directory: Path) -> Path:
    """Create an executable file that stands in for the UI binary."""
    binary = directory / "agentcy-tui"
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
    monkeypatch.setenv("AGENTCY_TUI", str(binary))
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
    """Without AGENTCY_TUI the binary comes from an absolute PATH entry."""
    binary = fake_binary(tmp_path)
    monkeypatch.delenv("AGENTCY_TUI", raising=False)
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
    monkeypatch.delenv("AGENTCY_TUI", raising=False)
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
    monkeypatch.delenv("AGENTCY_TUI", raising=False)
    monkeypatch.setenv("PATH", str(tmp_path))

    def unexpected() -> None:
        raise AssertionError("core started without a UI binary")

    monkeypatch.setattr(cli, "core_connection", unexpected)

    with pytest.raises(SystemExit) as stopped:
        cli.main(["ui", "--workspace", str(tmp_path)])

    assert "make setup" in str(stopped.value)
    assert "AGENTCY_TUI" in str(stopped.value)


def test_relative_tui_override_is_rejected(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, private_home: Path
) -> None:
    """AGENTCY_TUI must name an absolute executable path."""
    monkeypatch.setenv("AGENTCY_TUI", "agentcy-tui")

    with pytest.raises(SystemExit, match="absolute path"):
        cli.main(["ui", "--workspace", str(tmp_path)])


def test_workspace_flag_still_starts_the_shim(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, private_home: Path
) -> None:
    """Host registrations keep using `agentcy --workspace DIR`."""
    calls = []
    monkeypatch.setattr(server, "serve", lambda key, directory: calls.append((key, directory)))

    cli.main(["--workspace", str(tmp_path)])

    assert calls == [(workspace_key(tmp_path), tmp_path.resolve())]


def test_cli_import_leaves_the_mcp_server_unloaded() -> None:
    """The UI path does not import the MCP server module."""
    check = "import sys, agentcy.tools.cli; sys.exit('agentcy.mcp.server' in sys.modules)"
    # The test's own interpreter runs a fixed script with no shell.
    result = subprocess.run([sys.executable, "-I", "-c", check], check=False)  # noqa: S603
    assert result.returncode == 0


def test_http_flag_needs_an_access_token(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    private_home: Path,
    capsys: pytest.CaptureFixture[str],
) -> None:
    """The bridge refuses to listen on HTTP without a secret in the environment."""
    monkeypatch.delenv("AGENTCY_HTTP_TOKEN", raising=False)
    monkeypatch.setattr(server, "serve", lambda *arguments: pytest.fail("bridge started"))

    with pytest.raises(SystemExit):
        cli.main(["--workspace", str(tmp_path), "--http", "8765"])

    assert "AGENTCY_HTTP_TOKEN" in capsys.readouterr().err


def test_http_flag_starts_the_bridge_with_http_settings(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, private_home: Path
) -> None:
    """`--http PORT` passes the port, the token, and the tunnel host name to the server."""
    monkeypatch.setenv("AGENTCY_HTTP_TOKEN", "t" * 32)
    calls = []
    monkeypatch.setattr(server, "serve", lambda *arguments: calls.append(arguments))

    cli.main(
        ["--workspace", str(tmp_path), "--http", "8765", "--http-public-host", "bridge.example.com"]
    )

    settings = HttpSettings(8765, "t" * 32, "bridge.example.com")
    assert calls == [(workspace_key(tmp_path), tmp_path.resolve(), settings)]


def test_public_host_flag_needs_the_http_flag(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, private_home: Path
) -> None:
    """A tunnel host name has no meaning for the stdio shim."""
    monkeypatch.setattr(server, "serve", lambda *arguments: pytest.fail("bridge started"))

    with pytest.raises(SystemExit):
        cli.main(["--workspace", str(tmp_path), "--http-public-host", "bridge.example.com"])


def test_version_flag_prints_the_installed_version(capsys: pytest.CaptureFixture[str]) -> None:
    """`--version` prints the package version and exits without a workspace."""
    with pytest.raises(SystemExit) as stopped:
        cli.main(["--version"])

    assert stopped.value.code == 0
    assert capsys.readouterr().out == f"agentcy {version('agentcy')}\n"

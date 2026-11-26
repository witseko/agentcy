"""Verify the shim's cold-start command."""

import io
import subprocess
import sys
from pathlib import Path
from typing import Self
from unittest.mock import Mock, sentinel

import pytest

from agentcy.conversations import models
from agentcy.mcp import transport


def test_cold_start_uses_isolated_python(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    """Start with the current interpreter and ignore workspace-relative import paths."""
    home = tmp_path / "bridge"
    home.mkdir(mode=0o700)
    workspace = tmp_path / "workspace"
    workspace.mkdir()
    monkeypatch.chdir(workspace)
    monkeypatch.setenv("AGENTCY_HOME", str(home))
    monkeypatch.setenv("PYTHONPATH", ".")
    monkeypatch.delenv("AGENTCY_CORE_BIN", raising=False)
    connect = Mock(side_effect=[FileNotFoundError, FileNotFoundError, sentinel.connection])
    start = Mock(return_value=Mock(poll=Mock(return_value=None)))
    with monkeypatch.context() as launcher:
        launcher.setattr(transport, "connect", connect)
        launcher.setattr(transport.subprocess, "Popen", start)
        assert transport.core_connection() is sentinel.connection
    start.assert_called_once()
    command = start.call_args.args[0]
    assert command == [sys.executable, "-I", "-m", "agentcy.mcp.core"]
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


def cold_start(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> Mock:
    """Run one cold start against a missing socket and return the mocked process launcher."""
    home = tmp_path / "bridge"
    home.mkdir(mode=0o700)
    monkeypatch.setenv("AGENTCY_HOME", str(home))
    connect = Mock(side_effect=[FileNotFoundError, FileNotFoundError, sentinel.connection])
    start = Mock(return_value=Mock(poll=Mock(return_value=None)))
    monkeypatch.setattr(transport, "connect", connect)
    monkeypatch.setattr(transport.subprocess, "Popen", start)
    assert transport.core_connection() is sentinel.connection
    return start


def test_core_bin_selects_another_core(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    """An absolute path to a file starts that executable's core subcommand."""
    core_bin = tmp_path / "agentcy"
    core_bin.write_text("")
    monkeypatch.setenv("AGENTCY_CORE_BIN", str(core_bin))
    start = cold_start(monkeypatch, tmp_path)
    start.assert_called_once()
    assert start.call_args.args[0] == [str(core_bin), "core"]
    assert "shell" not in start.call_args.kwargs


@pytest.mark.parametrize("configured", ["agentcy", "./agentcy", "", "missing", "directory"])
def test_core_bin_refuses_anything_but_an_absolute_file(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path, configured: str
) -> None:
    """A relative, empty, missing, or directory path fails closed without starting a process."""
    (tmp_path / "agentcy").write_text("")
    (tmp_path / "directory").mkdir()
    monkeypatch.chdir(tmp_path)
    if configured in {"missing", "directory"}:
        configured = str(tmp_path / configured)
    monkeypatch.setenv("AGENTCY_CORE_BIN", configured)
    with pytest.raises(RuntimeError) as error:
        cold_start(monkeypatch, tmp_path)
    assert str(error.value) == transport.INVALID_CORE_BIN
    assert transport.subprocess.Popen.call_count == 0


def test_exchange_retries_once_on_transient_disconnect_for_idempotent_calls(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """Retry idempotent calls once on transient socket resets, but never retry create."""
    from secrets import token_urlsafe

    from agentcy.conversations import models

    calls = 0

    def flaky_once(request: models.Request) -> dict:
        nonlocal calls
        calls += 1
        if calls == 1:
            raise ConnectionResetError("core restarted")
        return {"ok": True}

    monkeypatch.setattr(transport, "_exchange_once", flaky_once)
    read_req = models.Read(workspace="a" * 64, token=token_urlsafe(16))
    assert transport.exchange(read_req) == {"ok": True}
    assert calls == 2

    calls = 0
    create_req = models.Create(
        workspace="a" * 64, brief="Test", initiator="Lead", invitees=["Worker"]
    )
    with pytest.raises(ConnectionResetError):
        transport.exchange(create_req)
    assert calls == 1


class FakeCore:
    """Answer one shim request with a fixed core response line."""

    def __init__(self, response: bytes) -> None:
        self.response = response

    def __enter__(self) -> Self:
        return self

    def __exit__(self, *_exc: object) -> None:
        return None

    def sendall(self, _data: bytes) -> None:
        """Accept the request without inspecting it."""

    def makefile(self, _mode: str) -> io.BytesIO:
        """Return the prepared response line."""
        return io.BytesIO(self.response)


def test_core_rejecting_a_valid_request_reports_a_version_mismatch(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """An older core rejects new fields. Tell the user to restart it instead of blaming the code."""
    fake_core = FakeCore(b'{"error": "Invalid bridge arguments"}\n')
    monkeypatch.setattr(transport, "core_connection", lambda: fake_core)
    request = models.Join(workspace="a" * 64, code="abcdefghijklmnopqrstuv.0123456789ab")
    with pytest.raises(ValueError, match="Invalid bridge arguments") as error:
        transport.exchange(request)
    assert "Stop the process running agentcy.mcp.core" in str(error.value)
    assert request.code not in str(error.value)

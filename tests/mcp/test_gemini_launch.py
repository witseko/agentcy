"""Check the Gemini launcher arguments and negative security boundaries without starting a provider."""

import json
import os
import sys
from importlib.util import module_from_spec, spec_from_file_location
from pathlib import Path
from types import ModuleType

import pytest

from agents_chat.mcp.plugin_contract import RunResult

PLUGIN_SCRIPT = Path(__file__).resolve().parents[2] / "plugins/gemini/server.py"
ASSIGNMENT_ID = "d" * 32
MODEL = "gemini-3.8-flash-medium"


@pytest.fixture
def gemini_plugin(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> ModuleType:
    """Load the standalone plugin with an isolated private bridge directory."""
    bridge = tmp_path / "bridge"
    bridge.mkdir(mode=0o700)
    monkeypatch.setenv("AGENTS_CHAT_HOME", str(bridge))
    spec = spec_from_file_location("gemini_plugin", PLUGIN_SCRIPT)
    assert spec is not None and spec.loader is not None
    plugin = module_from_spec(spec)
    spec.loader.exec_module(plugin)
    monkeypatch.setattr(plugin, "platform_sandbox", lambda: True)
    return plugin


def test_launch_preserves_installed_agy_symlink_and_configures_read_only_worker(
    gemini_plugin: ModuleType, monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    """Pass the absolute PATH launch name, read-only settings, and safe-path MCP command."""
    bin_directory = tmp_path.resolve() / "bin"
    bin_directory.mkdir()
    target = bin_directory / "installed-agy"
    target.touch(mode=0o700)
    (bin_directory / "agy").symlink_to(target.name)
    checkout = tmp_path / "checkout"
    checkout.mkdir()
    monkeypatch.setenv("PATH", os.pathsep.join(("tools", ".", str(bin_directory), "")))
    gemini_plugin.run_path(ASSIGNMENT_ID).mkdir(parents=True)

    def launch(arguments: list[str], *, cwd: Path, env: dict[str, str], **options: object) -> None:
        """Inspect the launch request and its temporary MCP and CLI configurations."""
        assert cwd == checkout
        assert arguments[:2] == ["/usr/bin/sandbox-exec", "-p"]
        assert arguments[3] == str(bin_directory / "agy")
        assert arguments[4:8] == ["--disable-slash-commands", "--model", MODEL, "--print"]
        assert "report brief progress updates to the lead periodically" in arguments[8]
        assert not options.get("shell")
        assert env["PATH"] == str(bin_directory)
        home = Path(env["HOME"])
        mcp_config = home / ".gemini" / "config" / "mcp_config.json"
        server = json.loads(mcp_config.read_text(encoding="utf-8"))["mcpServers"]["agentschat"]
        assert server == {
            "command": sys.executable,
            "args": [
                "-E",
                "-P",
                "-m",
                "agents_chat.mcp.fresh_server",
                str(checkout),
            ],
        }
        settings_path = home / ".gemini" / "antigravity-cli" / "settings.json"
        settings = json.loads(settings_path.read_text(encoding="utf-8"))
        assert settings["modelProvider"] == "gemini"
        assert settings["allowNonWorkspaceAccess"] is False
        assert settings["enableTerminalSandbox"] is True
        assert settings["permissions"] == {
            "allow": ["read_file(*)", "mcp(agentschat/*)"],
            "deny": ["write_file(*)", "command(*)"],
        }

    monkeypatch.setattr(gemini_plugin.subprocess, "Popen", launch)
    monkeypatch.setattr(
        gemini_plugin, "monitor", lambda *_: RunResult(state="finished", exit_code=0)
    )
    gemini_plugin.run(ASSIGNMENT_ID, checkout, MODEL, 30)
    assert gemini_plugin.inspect(ASSIGNMENT_ID) == RunResult(state="finished", exit_code=0)
    assert not (gemini_plugin.run_path(ASSIGNMENT_ID) / "runtime").exists()


def test_start_detaches_monitor_in_python_isolated_mode_and_is_idempotent(
    gemini_plugin: ModuleType, monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    """Launch the monitor once in isolated mode and report existing state on replay."""
    checkout = tmp_path / "checkout"
    checkout.mkdir()
    bridge = tmp_path / "bridge"
    commands = []

    def launch(arguments: list[str], **options: object) -> None:
        """Record the monitor arguments without starting a process."""
        assert not options.get("shell")
        commands.append(arguments)

    monkeypatch.setenv("GEMINI_API_KEY", "unused-test-credential")
    monkeypatch.setattr(gemini_plugin, "find_on_path", lambda _: "/installed/bin/agy")
    monkeypatch.setattr(gemini_plugin.subprocess, "Popen", launch)
    result = gemini_plugin.start(
        ASSIGNMENT_ID, str(checkout), str(bridge.resolve()), "unused-test-code", MODEL, 30
    )
    replay = gemini_plugin.start(
        ASSIGNMENT_ID, str(checkout), str(bridge.resolve()), "unused-test-code", MODEL, 30
    )

    assert result == RunResult(state="starting")
    assert replay == RunResult(state="starting")
    assert commands == [
        [
            sys.executable,
            "-I",
            str(PLUGIN_SCRIPT),
            "--monitor",
            ASSIGNMENT_ID,
            str(checkout.resolve()),
            MODEL,
            "30",
        ]
    ]


def test_start_rejects_invalid_model_caps_and_workspace_boundaries(
    gemini_plugin: ModuleType, monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    """Fail closed when credentials, caps, model format, or directory boundaries are invalid."""
    checkout = tmp_path / "checkout"
    checkout.mkdir()
    bridge = (tmp_path / "bridge").resolve()
    monkeypatch.setenv("GEMINI_API_KEY", "unused-test-credential")
    monkeypatch.setattr(gemini_plugin, "find_on_path", lambda _: "/installed/bin/agy")

    assert gemini_plugin.start(
        ASSIGNMENT_ID, str(checkout), str(bridge), "code", "claude-sonnet-4", 30
    ) == RunResult(state="failed")
    assert gemini_plugin.start(
        ASSIGNMENT_ID, str(checkout), str(bridge), "code", MODEL, 30, token_cap=1000
    ) == RunResult(state="failed")
    assert gemini_plugin.start(
        ASSIGNMENT_ID, str(checkout), str(bridge), "code", MODEL, 30, cost_cap_microusd=1000
    ) == RunResult(state="failed")
    assert gemini_plugin.start(
        ASSIGNMENT_ID, str(tmp_path), str(bridge), "code", MODEL, 30
    ) == RunResult(state="failed")
    assert gemini_plugin.start(
        ASSIGNMENT_ID, str(checkout), str(tmp_path), "code", MODEL, 30
    ) == RunResult(state="failed")

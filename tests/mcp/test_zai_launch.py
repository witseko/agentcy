"""Check the Z.ai launcher arguments without starting a provider."""

import json
import os
import sys
from importlib.util import module_from_spec, spec_from_file_location
from pathlib import Path
from types import ModuleType

import pytest

from agentcy.mcp.plugin_contract import RunResult

PLUGIN_SCRIPT = Path(__file__).resolve().parents[2] / "plugins/zai/server.py"
ASSIGNMENT_ID = "e" * 32
MODEL = "zai-coding-plan/glm-example"


@pytest.fixture
def zai_plugin(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> ModuleType:
    """Load the standalone plugin with an isolated private bridge directory."""
    bridge = tmp_path / "bridge"
    bridge.mkdir(mode=0o700)
    monkeypatch.setenv("AGENTCY_HOME", str(bridge))
    spec = spec_from_file_location("zai_plugin", PLUGIN_SCRIPT)
    assert spec is not None and spec.loader is not None
    plugin = module_from_spec(spec)
    spec.loader.exec_module(plugin)
    monkeypatch.setattr(plugin, "platform_sandbox", lambda: True)
    return plugin


def test_launch_preserves_installed_opencode_symlink_in_workspace(
    zai_plugin: ModuleType, monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    """Pass the absolute PATH launch name and safe-path MCP command to the sandbox."""
    bin_directory = tmp_path.resolve() / "bin"
    bin_directory.mkdir()
    target = bin_directory / "installed-opencode"
    target.touch(mode=0o700)
    (bin_directory / "opencode").symlink_to(target.name)
    checkout = tmp_path / "checkout"
    checkout.mkdir()
    monkeypatch.setenv("PATH", os.pathsep.join(("tools", ".", str(bin_directory), "")))
    zai_plugin.run_path(ASSIGNMENT_ID).mkdir(parents=True)

    def launch(arguments: list[str], *, cwd: Path, env: dict[str, str], **options: object) -> None:
        """Inspect the launch request and its temporary MCP configuration."""
        assert cwd == checkout
        assert arguments[:2] == ["/usr/bin/sandbox-exec", "-p"]
        assert arguments[3] == str(bin_directory / "opencode")
        assert "report brief progress updates to the lead periodically" in arguments[-1]
        assert not options.get("shell")
        assert env["PATH"] == str(bin_directory)
        config = Path(env["XDG_CONFIG_HOME"]) / "opencode" / "opencode.json"
        server = json.loads(config.read_text(encoding="utf-8"))["mcp"]["servers"]["agentcy"]
        assert server["command"] == [
            sys.executable,
            "-E",
            "-P",
            "-m",
            "agentcy.mcp.fresh_server",
            str(checkout),
        ]

    monkeypatch.setattr(zai_plugin.subprocess, "Popen", launch)
    monkeypatch.setattr(zai_plugin, "monitor", lambda *_: RunResult(state="finished", exit_code=0))
    zai_plugin.run(ASSIGNMENT_ID, checkout, MODEL, 30)
    assert zai_plugin.inspect(ASSIGNMENT_ID) == RunResult(state="finished", exit_code=0)


def test_start_detaches_monitor_in_python_isolated_mode(
    zai_plugin: ModuleType, monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    """Launch the monitor in isolated mode with the active interpreter."""
    checkout = tmp_path / "checkout"
    checkout.mkdir()
    bridge = tmp_path / "bridge"
    commands = []

    def launch(arguments: list[str], **options: object) -> None:
        """Record the monitor arguments without starting a process."""
        assert not options.get("shell")
        commands.append(arguments)

    monkeypatch.setenv("ZAI_API_KEY", "unused-test-credential")
    monkeypatch.setattr(zai_plugin, "find_on_path", lambda _: "/installed/bin/opencode")
    monkeypatch.setattr(zai_plugin.subprocess, "Popen", launch)
    result = zai_plugin.start(
        ASSIGNMENT_ID, str(checkout), str(bridge.resolve()), "unused-test-code", MODEL, 30
    )

    assert result == RunResult(state="starting")
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

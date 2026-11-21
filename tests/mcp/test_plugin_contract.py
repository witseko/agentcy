"""Check the portable contract fixtures and the Z.ai and Gemini plugins' MCP responses."""

import json
import sys
from pathlib import Path

import pytest
from jsonschema import Draft202012Validator

from agents_chat.mcp import plugin_client
from agents_chat.mcp.plugin_contract import RunResult, Start
from agents_chat.mcp.plugin_settings import Plugin

PROJECT = Path(__file__).parents[2]


def test_contract_examples_validate() -> None:
    """Keep the documented exchanges compatible with their argument and result schemas."""
    protocol = PROJECT / "protocol"
    schema = json.loads((protocol / "fresh-runs.v1.schema.json").read_bytes())
    examples = json.loads((protocol / "fresh-runs.v1.examples.json").read_bytes())
    for item in examples:
        name = item["name"]
        if name == "capabilities":
            assert not item["arguments"]
            result = "capabilities_result"
        else:
            Draft202012Validator(schema["$defs"][f"{name}_arguments"]).validate(item["arguments"])
            result = "run_result"
        Draft202012Validator(schema["$defs"][result]).validate(item["structuredContent"])


@pytest.mark.parametrize(
    ("script", "env_key", "model"),
    [
        ("plugins/zai/server.py", "ZAI_API_KEY", "zai-coding-plan/glm-example"),
        ("plugins/gemini/server.py", "GEMINI_API_KEY", "gemini-3.8-flash-medium"),
    ],
)
def test_plugin_declares_capabilities_and_rejects_unusable_credentials(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path, script: str, env_key: str, model: str
) -> None:
    """Exercise the real executable contract without accepting a provider run."""
    root = tmp_path / "bridge"
    root.mkdir(mode=0o700)
    monkeypatch.setenv("AGENTS_CHAT_HOME", str(root))
    monkeypatch.delenv(env_key, raising=False)
    plugin = Plugin(
        command=[sys.executable, str(PROJECT / script)],
        model=model,
    )
    plugin_client.check_capabilities(plugin)
    request = Start(
        assignment_id="f" * 32,
        workspace=str(tmp_path),
        bridge_home=str(root),
        join_code="unused-test-credential",
        model=plugin.model,
        timeout_seconds=30,
    )

    assert plugin_client.run_operation(plugin, "start", request) == RunResult(state="failed")
    assert plugin_client.run_operation(plugin, "inspect", request.assignment_id) == RunResult(
        state="unknown"
    )
    assert plugin_client.run_operation(plugin, "cancel", request.assignment_id) == RunResult(
        state="unknown"
    )

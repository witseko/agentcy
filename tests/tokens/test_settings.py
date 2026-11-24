"""Test private user-level token budget configuration loading and validation."""

import json
from pathlib import Path

import pytest

from agentcy.tokens.settings import read_settings


def write_tokens_config(root: Path, payload: dict | str, mode: int = 0o600) -> Path:
    """Write ~/.agentcy/tokens.json with explicit permissions."""
    path = root / "tokens.json"
    text = payload if isinstance(payload, str) else json.dumps(payload)
    path.write_text(text, encoding="utf-8")
    path.chmod(mode)
    return path


def test_missing_tokens_config_returns_none(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    """Return None cleanly when the user has not configured tokens.json."""
    root = tmp_path / "bridge"
    root.mkdir(mode=0o700)
    monkeypatch.setenv("AGENTCY_HOME", str(root))

    assert read_settings() is None


def test_valid_tokens_config_resolves_models_case_insensitively(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """Load private model budgets, defaults, and rolling windows."""
    root = tmp_path / "bridge"
    root.mkdir(mode=0o700)
    monkeypatch.setenv("AGENTCY_HOME", str(root))
    write_tokens_config(
        root,
        {
            "default_initial_tokens": 500_000,
            "warn_percent": 80,
            "imbalance_gap_percent": 25,
            "models": {
                "claude-sonnet-4-6": {
                    "initial_tokens": 200_000,
                    "warn_percent": 70,
                    "reset_window_hours": 24,
                },
                "gemini-2.5-pro": {"initial_tokens": 1_000_000},
            },
        },
    )

    loaded = read_settings()
    assert loaded is not None
    assert loaded.initial_tokens_for("claude-sonnet-4-6") == 200_000
    assert loaded.initial_tokens_for("CLAUDE-SONNET-4-6") == 200_000
    assert loaded.initial_tokens_for("claude-sonnet-4-6", override=150_000) == 150_000
    assert loaded.initial_tokens_for("unknown-model") == 500_000
    assert loaded.warn_percent_for("claude-sonnet-4-6") == 70
    assert loaded.warn_percent_for("gemini-2.5-pro") == 80
    assert loaded.reset_window_seconds_for("claude-sonnet-4-6") == 86_400
    assert loaded.reset_window_seconds_for("gemini-2.5-pro") is None


def test_rejects_world_readable_or_symlink_tokens_config(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """Enforce owner-only regular file permissions on tokens.json."""
    root = tmp_path / "bridge"
    root.mkdir(mode=0o700)
    monkeypatch.setenv("AGENTCY_HOME", str(root))
    write_tokens_config(root, {"default_initial_tokens": 100_000}, mode=0o644)

    with pytest.raises(ValueError, match="private file"):
        read_settings()

    (root / "tokens.json").unlink()
    target = tmp_path / "real.json"
    target.write_text('{"default_initial_tokens": 100000}', encoding="utf-8")
    target.chmod(0o600)
    (root / "tokens.json").symlink_to(target)

    with pytest.raises(ValueError, match="private file"):
        read_settings()


def test_rejects_invalid_thresholds_or_unknown_fields(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """Reject out-of-range values and extra fields without leaking input."""
    root = tmp_path / "bridge"
    root.mkdir(mode=0o700)
    monkeypatch.setenv("AGENTCY_HOME", str(root))
    write_tokens_config(root, {"warn_percent": 150})

    with pytest.raises(ValueError, match="Invalid token configuration"):
        read_settings()

    write_tokens_config(root, {"unexpected": 1})
    with pytest.raises(ValueError, match="Invalid token configuration"):
        read_settings()

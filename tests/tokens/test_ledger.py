"""Test the cross-conversation user-level SQLite token ledger."""

from pathlib import Path

import pytest

from agentcy.tokens import ledger


def test_ledger_aggregates_across_conversations_and_is_idempotent(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """Upserting the same slot replaces its total instead of double-counting."""
    root = tmp_path / "bridge"
    root.mkdir(mode=0o700)
    monkeypatch.setenv("AGENTCY_HOME", str(root))
    ledger.clear_ledger_cache()

    ledger.record_slot_usage(
        workspace="w1",
        room="r1",
        slot=1,
        model="claude-sonnet-4-6",
        input_tokens=10_000,
        output_tokens=5_000,
        source="reported",
        now=1_700_000_000,
    )
    ledger.record_slot_usage(
        workspace="w2",
        room="r2",
        slot=2,
        model="claude-sonnet-4-6",
        input_tokens=20_000,
        output_tokens=10_000,
        source="reported",
        now=1_700_000_500,
    )
    # Updating the same (w1, r1, 1) slot supersedes its earlier count.
    ledger.record_slot_usage(
        workspace="w1",
        room="r1",
        slot=1,
        model="claude-sonnet-4-6",
        input_tokens=12_000,
        output_tokens=8_000,
        source="reported",
        now=1_700_001_000,
    )

    spend = ledger.model_spend(["claude-sonnet-4-6", "gemini-2.5-pro"])
    assert spend == {"claude-sonnet-4-6": 50_000}


def test_ledger_respects_reset_window_cutoffs(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """Exclude stale conversation slots outside a model's rolling window."""
    root = tmp_path / "bridge"
    root.mkdir(mode=0o700)
    monkeypatch.setenv("AGENTCY_HOME", str(root))
    ledger.clear_ledger_cache()

    ledger.record_slot_usage(
        workspace="w1",
        room="old",
        slot=1,
        model="claude-sonnet-4-6",
        input_tokens=40_000,
        output_tokens=10_000,
        source="reported",
        now=1_700_000_000,
    )
    ledger.record_slot_usage(
        workspace="w1",
        room="new",
        slot=2,
        model="claude-sonnet-4-6",
        input_tokens=8_000,
        output_tokens=2_000,
        source="reported",
        now=1_700_100_000,
    )

    recent = ledger.model_spend(
        ["claude-sonnet-4-6"],
        since_by_model={"claude-sonnet-4-6": 1_700_050_000},
    )
    assert recent == {"claude-sonnet-4-6": 10_000}

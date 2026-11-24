"""Test token budget percentage math and lead rebalancing recommendations."""

from agentcy.tokens.balancer import build_member_usage, evaluate_balance
from agentcy.tokens.settings import ModelBudget, TokenSettings


def test_unequal_starting_budgets_balance_by_remaining_percentage() -> None:
    """A smaller-budget model spending 50k (50%) is more pressured than a 1M model spending 100k (10%)."""
    config = TokenSettings(
        models={
            "claude-sonnet-4-6": ModelBudget(initial_tokens=100_000),
            "gemini-2.5-pro": ModelBudget(initial_tokens=1_000_000),
        }
    )
    claude = build_member_usage(
        {
            "model": "claude-sonnet-4-6",
            "input_tokens": 30_000,
            "output_tokens": 20_000,
            "conversation_tokens": 50_000,
            "source": "reported",
            "has_usage": True,
        },
        config,
        {"claude-sonnet-4-6": 50_000, "gemini-2.5-pro": 100_000},
    )
    gemini = build_member_usage(
        {
            "model": "gemini-2.5-pro",
            "input_tokens": 70_000,
            "output_tokens": 30_000,
            "conversation_tokens": 100_000,
            "source": "plugin",
            "has_usage": True,
        },
        config,
        {"claude-sonnet-4-6": 50_000, "gemini-2.5-pro": 100_000},
    )
    assert claude is not None and gemini is not None
    assert claude["used_percent"] == 50.0
    assert gemini["used_percent"] == 10.0

    members = {
        "Claude": {"role": "lead", "state": "replied", "usage": claude},
        "Gemini": {"role": "worker", "state": "replied", "usage": gemini},
        "Human": {"role": "human", "state": "human"},
    }
    balance = evaluate_balance(members, config)
    assert balance is not None
    assert balance["balanced"] is False
    assert balance["imbalance_gap_percent"] == 40.0
    assert balance["overloaded"] == ["Claude"]
    assert balance["preferred"] == ["Gemini"]
    assert "Claude" in balance["recommendation"]
    assert "Gemini" in balance["recommendation"]


def test_same_model_peers_are_not_recommended_against_each_other() -> None:
    """Two slots sharing the same model pool do not trigger a false rebalance."""
    config = TokenSettings(models={"claude-sonnet-4-6": ModelBudget(initial_tokens=100_000)})
    usage = build_member_usage(
        {
            "model": "claude-sonnet-4-6",
            "input_tokens": 60_000,
            "output_tokens": 20_000,
            "conversation_tokens": 80_000,
            "source": "reported",
            "has_usage": True,
        },
        config,
        {"claude-sonnet-4-6": 80_000},
    )
    members = {
        "Lead": {"role": "lead", "state": "replied", "usage": usage},
        "Worker": {"role": "worker", "state": "replied", "usage": usage},
    }
    balance = evaluate_balance(members, config)
    assert balance == {"balanced": True, "imbalance_gap_percent": 0.0}


def test_exhausted_model_emits_warning_and_ignores_retired_peers() -> None:
    """Retired or ended fresh-run peers are never recommended as rebalance targets."""
    config = TokenSettings(
        models={
            "claude-sonnet-4-6": ModelBudget(initial_tokens=100_000),
            "gemini-2.5-pro": ModelBudget(initial_tokens=500_000),
            "zai/glm-4.5": ModelBudget(initial_tokens=500_000),
        }
    )
    claude = build_member_usage(
        {
            "model": "claude-sonnet-4-6",
            "input_tokens": 80_000,
            "output_tokens": 25_000,
            "conversation_tokens": 105_000,
            "source": "reported",
            "has_usage": True,
        },
        config,
        {"claude-sonnet-4-6": 105_000},
    )
    gemini = build_member_usage(
        {
            "model": "gemini-2.5-pro",
            "input_tokens": 10_000,
            "output_tokens": 5_000,
            "conversation_tokens": 15_000,
            "source": "plugin",
            "has_usage": True,
        },
        config,
        {"gemini-2.5-pro": 15_000},
    )
    glm = build_member_usage(
        {
            "model": "zai/glm-4.5",
            "input_tokens": 0,
            "output_tokens": 0,
            "conversation_tokens": 0,
            "source": "estimated",
            "has_usage": False,
        },
        config,
        {},
    )
    assert claude is not None and claude["status"] == "exhausted"
    members = {
        "Claude": {"role": "lead", "state": "replied", "usage": claude},
        "Gemini": {"role": "worker", "state": "replied", "usage": gemini},
        "GLM": {"role": "worker", "state": "retired", "usage": glm},
    }
    balance = evaluate_balance(members, config)
    assert balance is not None
    assert balance["balanced"] is False
    assert balance["preferred"] == ["Gemini"]
    assert "Token budget warning" in balance["recommendation"]

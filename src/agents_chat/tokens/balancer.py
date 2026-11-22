"""Compute per-participant token budgets and lead rebalancing recommendations."""

from agents_chat.conversations.progress import fresh_run_ended, is_retired
from agents_chat.conversations.roles import Role
from agents_chat.tokens.settings import (
    DEFAULT_IMBALANCE_GAP_PERCENT,
    DEFAULT_WARN_PERCENT,
    TokenSettings,
)


def budget_status(used_percent: float, warn_percent: int) -> str:
    """Classify a model's budget pressure into ok, warning, or exhausted."""
    if used_percent >= 100.0:
        return "exhausted"
    if used_percent >= warn_percent:
        return "warning"
    return "ok"


def build_member_usage(
    raw: dict,
    config: TokenSettings | None,
    ledger_spend: dict[str, int],
) -> dict | None:
    """Build one member's public usage summary, or None when no usage or budget exists."""
    model = raw.get("model")
    override = raw.get("token_budget")
    initial = (
        config.initial_tokens_for(model, override)
        if config is not None
        else override
    )
    conversation_tokens = int(raw.get("conversation_tokens", 0))
    source = raw.get("source", "estimated")
    has_exact = source in ("reported", "plugin")
    tracking_active = (
        config is not None or model is not None or initial is not None or has_exact
    )
    if not tracking_active or (not raw.get("has_usage") and initial is None):
        return None
    model_used = (
        max(ledger_spend.get(model, 0), conversation_tokens)
        if model
        else conversation_tokens
    )
    entry: dict = {
        "conversation_tokens": conversation_tokens,
        "input_tokens": int(raw.get("input_tokens", 0)),
        "output_tokens": int(raw.get("output_tokens", 0)),
        "model_tokens_used": model_used,
        "source": raw.get("source", "estimated"),
    }
    if model:
        entry["model"] = model
    if initial is not None:
        used_pct = round(100.0 * model_used / initial, 1)
        remaining = max(0, initial - model_used)
        remaining_pct = max(0.0, round(100.0 - used_pct, 1))
        warn_pct = config.warn_percent_for(model) if config is not None else DEFAULT_WARN_PERCENT
        entry.update(
            initial_tokens=initial,
            remaining_tokens=remaining,
            used_percent=used_pct,
            remaining_percent=remaining_pct,
            status=budget_status(used_pct, warn_pct),
        )
    return entry


def _active_budgeted_members(members: dict[str, dict]) -> list[tuple[str, dict]]:
    """Return active non-human members that have a known initial budget."""
    active: list[tuple[str, dict]] = []
    for name, member in members.items():
        if member.get("role") == Role.HUMAN.label:
            continue
        if is_retired(member) or fresh_run_ended(member):
            continue
        usage = member.get("usage")
        if usage and "used_percent" in usage:
            active.append((name, usage))
    return active


def _effective_gap_threshold(active: list[tuple[str, dict]], config: TokenSettings | None) -> int:
    """Use the tightest configured imbalance gap threshold among active models."""
    default_gap = config.imbalance_gap_percent if config is not None else DEFAULT_IMBALANCE_GAP_PERCENT
    if config is None:
        return default_gap
    gaps = [default_gap]
    for _, usage in active:
        entry = config.budget_for(usage.get("model"))
        if entry is not None and entry.imbalance_gap_percent is not None:
            gaps.append(entry.imbalance_gap_percent)
    return min(gaps)


def _format_recommendation(
    overloaded: list[tuple[str, dict]],
    preferred: list[tuple[str, dict]],
) -> str:
    """Format a concise, actionable rebalancing hint for the lead."""
    top_name, top_usage = overloaded[0]
    best_name, best_usage = preferred[0]
    if top_usage["status"] == "exhausted":
        return (
            f"Token budget warning: {top_name} has reached {top_usage['used_percent']:.1f}% of its "
            f"initial budget. Route follow-up tasks or handoffs to {best_name} "
            f"({best_usage['remaining_percent']:.1f}% left)."
        )
    return (
        f"Token balance: {top_name} is at {top_usage['used_percent']:.1f}% of budget "
        f"({top_usage['remaining_percent']:.1f}% left) while {best_name} is at "
        f"{best_usage['used_percent']:.1f}% ({best_usage['remaining_percent']:.1f}% left). "
        f"Prefer assigning new tasks or handoffs to {best_name}."
    )


def evaluate_balance(members: dict[str, dict], config: TokenSettings | None) -> dict | None:
    """Evaluate budget balance across active participants and return lead guidance."""
    active = _active_budgeted_members(members)
    if len(active) < 2:
        return None
    ranked = sorted(active, key=lambda item: (-item[1]["used_percent"], item[0]))
    highest_name, highest_usage = ranked[0]
    highest_model = highest_usage.get("model")
    # Only compare against peers that do not share the exact same model pool.
    alternatives = [
        (name, usage)
        for name, usage in reversed(ranked)
        if name != highest_name
        and (highest_model is None or usage.get("model") != highest_model)
    ]
    if not alternatives:
        return {"balanced": True, "imbalance_gap_percent": 0.0}
    _, lowest_usage = alternatives[0]
    gap = round(max(0.0, highest_usage["used_percent"] - lowest_usage["used_percent"]), 1)
    threshold = _effective_gap_threshold(active, config)
    pressured = highest_usage["status"] in ("warning", "exhausted") and (
        lowest_usage["used_percent"] < highest_usage["used_percent"]
    )
    if gap < threshold and not pressured:
        return {"balanced": True, "imbalance_gap_percent": gap}
    overloaded = [
        (name, usage)
        for name, usage in ranked
        if usage["status"] in ("warning", "exhausted")
        or (usage["used_percent"] - lowest_usage["used_percent"]) >= threshold
    ]
    preferred = [
        (name, usage)
        for name, usage in alternatives
        if usage["status"] != "exhausted"
        and usage["used_percent"] < highest_usage["used_percent"]
    ]
    if not preferred:
        return {"balanced": True, "imbalance_gap_percent": gap}
    return {
        "balanced": False,
        "imbalance_gap_percent": gap,
        "overloaded": [name for name, _ in overloaded],
        "preferred": [name for name, _ in preferred],
        "recommendation": _format_recommendation(overloaded, preferred),
    }

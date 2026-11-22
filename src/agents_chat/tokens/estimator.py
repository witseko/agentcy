"""Estimate token spend from bridge messages and reads when exact counts are absent."""

import math
from collections.abc import Iterable

CHARS_PER_TOKEN = 4
TOOL_CALL_OVERHEAD_TOKENS = 40


def estimate_text(text: str | None) -> int:
    """Approximate token count from character length using a 4-chars-per-token heuristic."""
    if not text:
        return 0
    return max(1, math.ceil(len(text) / CHARS_PER_TOKEN))


def estimate_post(body: str) -> int:
    """Estimate output tokens spent producing a bridge message and its tool call."""
    return estimate_text(body) + TOOL_CALL_OVERHEAD_TOKENS


def estimate_read(messages: Iterable[dict], brief: str | None = None) -> int:
    """Estimate input tokens consumed when reading a page of messages and optional brief."""
    total = estimate_text(brief)
    for item in messages:
        total += estimate_text(item.get("body"))
    return total

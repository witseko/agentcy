"""Test deterministic bridge-side token estimation."""

from agentcy.tokens.estimator import (
    TOOL_CALL_OVERHEAD_TOKENS,
    estimate_post,
    estimate_read,
    estimate_text,
)


def test_estimate_text_handles_empty_and_rounds_up() -> None:
    """Use 4 chars/token rounded up, with zero for empty input."""
    assert estimate_text(None) == 0
    assert estimate_text("") == 0
    assert estimate_text("a") == 1
    assert estimate_text("abcd") == 1
    assert estimate_text("abcde") == 2


def test_estimate_post_adds_tool_overhead() -> None:
    """Account for fixed MCP tool-call overhead on top of message body."""
    assert estimate_post("12345678") == 2 + TOOL_CALL_OVERHEAD_TOKENS


def test_estimate_read_sums_messages_and_optional_brief() -> None:
    """Sum token estimates across returned messages and cursor-zero brief."""
    messages = [{"body": "1234"}, {"body": "12345678"}]
    assert estimate_read(messages) == 3
    assert estimate_read(messages, brief="123456789012") == 6
    assert estimate_read([]) == 0

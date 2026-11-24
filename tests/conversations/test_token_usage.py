"""Test hybrid token tracking, cross-conversation model budgets, and security bounds."""

import json
from pathlib import Path
from secrets import token_hex

import pytest
from pydantic import ValidationError
from sqlalchemy import Connection

from agentcy.conversations import models, operator, operator_models, repository
from agentcy.storage.database import data_dir, database
from agentcy.tokens import ledger


def write_tokens_json(payload: dict) -> Path:
    """Write private ~/.agentcy/tokens.json inside the active test home."""
    path = data_dir() / "tokens.json"
    path.write_text(json.dumps(payload), encoding="utf-8")
    path.chmod(0o600)
    return path


def test_estimated_usage_and_reported_supersede_and_lead_balance(
    conversation_db: Connection,
) -> None:
    """Bridge estimation tracks posts/reads, report_usage supersedes it, and status flags imbalance."""
    ledger.clear_ledger_cache()
    workspace = conversation_db.info["workspace"]
    write_tokens_json(
        {
            "warn_percent": 75,
            "imbalance_gap_percent": 20,
            "models": {
                "claude-sonnet-4-6": {"initial_tokens": 100_000},
                "gemini-2.5-pro": {"initial_tokens": 1_000_000},
            },
        }
    )
    created = repository.apply(
        conversation_db,
        models.Create(
            workspace=workspace,
            brief="Audit retry backoff and rate limits across services.",
            initiator="Claude",
            initiator_model="claude-sonnet-4-6",
            invitees=[models.Invitation(name="Gemini", model="gemini-2.5-pro")],
        ),
    )
    gemini = repository.apply(
        conversation_db,
        models.Join(workspace=workspace, code=created["invitations"]["Gemini"]),
    )
    repository.apply(
        conversation_db,
        models.Post(
            workspace=workspace,
            token=created["token"],
            submission_id="m1",
            body="Please inspect the retry worker.",
        ),
    )
    repository.apply(
        conversation_db,
        models.Read(workspace=workspace, token=gemini["token"], after=0),
    )

    # Before exact reporting, bridge estimates populate usage.
    estimated_status = repository.apply(
        conversation_db, models.Status(workspace=workspace, token=created["token"])
    )
    assert estimated_status["members"]["Claude"]["usage"]["source"] == "estimated"
    assert estimated_status["members"]["Claude"]["usage"]["output_tokens"] > 0
    assert estimated_status["members"]["Gemini"]["usage"]["input_tokens"] > 0
    assert estimated_status["token_balance"]["balanced"] is True

    # Exact self-reported usage supersedes estimates and triggers imbalance detection.
    repository.apply(
        conversation_db,
        models.ReportUsage(
            workspace=workspace,
            token=created["token"],
            input_tokens=45_000,
            output_tokens=15_000,
        ),
    )
    repository.apply(
        conversation_db,
        models.ReportUsage(
            workspace=workspace,
            token=gemini["token"],
            input_tokens=40_000,
            output_tokens=10_000,
        ),
    )

    status = repository.apply(
        conversation_db, models.Status(workspace=workspace, token=created["token"])
    )
    claude_usage = status["members"]["Claude"]["usage"]
    gemini_usage = status["members"]["Gemini"]["usage"]
    assert claude_usage["source"] == "reported"
    assert claude_usage["conversation_tokens"] == 60_000
    assert claude_usage["used_percent"] == 60.0
    assert gemini_usage["used_percent"] == 5.0
    assert status["token_balance"]["balanced"] is False
    assert status["token_balance"]["overloaded"] == ["Claude"]
    assert status["token_balance"]["preferred"] == ["Gemini"]


def test_cross_conversation_ledger_shares_model_budget(conversation_db: Connection) -> None:
    """Spend in an earlier conversation counts against the model's shared starting budget."""
    ledger.clear_ledger_cache()
    workspace = conversation_db.info["workspace"]
    write_tokens_json(
        {
            "models": {
                "claude-sonnet-4-6": {"initial_tokens": 100_000},
                "gemini-2.5-pro": {"initial_tokens": 500_000},
            }
        }
    )
    first = repository.apply(
        conversation_db,
        models.Create(
            workspace=workspace,
            brief="First task",
            initiator="Claude",
            initiator_model="claude-sonnet-4-6",
            invitees=["Worker"],
        ),
    )
    repository.apply(
        conversation_db,
        models.ReportUsage(
            workspace=workspace,
            token=first["token"],
            input_tokens=40_000,
            output_tokens=10_000,
        ),
    )

    second = repository.apply(
        conversation_db,
        models.Create(
            workspace=workspace,
            brief="Second task",
            initiator="Claude",
            initiator_model="claude-sonnet-4-6",
            invitees=[models.Invitation(name="Gemini", model="gemini-2.5-pro")],
        ),
    )
    repository.apply(
        conversation_db,
        models.Join(workspace=workspace, code=second["invitations"]["Gemini"]),
    )
    repository.apply(
        conversation_db,
        models.ReportUsage(
            workspace=workspace,
            token=second["token"],
            input_tokens=10_000,
            output_tokens=5_000,
        ),
    )

    status = repository.apply(
        conversation_db, models.Status(workspace=workspace, token=second["token"])
    )
    assert status["members"]["Claude"]["usage"]["conversation_tokens"] == 15_000
    assert status["members"]["Claude"]["usage"]["model_tokens_used"] == 65_000
    assert status["members"]["Claude"]["usage"]["remaining_tokens"] == 35_000
    assert status["token_balance"]["balanced"] is False


def test_operator_set_member_budget_and_security_rejections(conversation_db: Connection) -> None:
    """Operator can override a slot's budget; invalid tokens, retired, closed, or human targets fail."""
    ledger.clear_ledger_cache()
    workspace = conversation_db.info["workspace"]
    created = repository.apply(
        conversation_db,
        models.Create(
            workspace=workspace,
            brief="Budget security checks",
            initiator="Lead",
            invitees=["Worker"],
        ),
    )
    worker = repository.apply(
        conversation_db, models.Join(workspace=workspace, code=created["invitations"]["Worker"])
    )

    # Operator sets a per-conversation override for Worker.
    updated = operator.apply(
        conversation_db,
        workspace,
        operator_models.SetMemberBudget(
            version=1,
            key="op-key",
            directory="/work/repo",
            conversation=created["conversation"],
            action="operator_set_member_budget",
            name="Worker",
            model="gemini-2.5-pro",
            token_budget=250_000,
        ),
    )
    assert updated == {
        "participant": "Worker",
        "model": "gemini-2.5-pro",
        "token_budget": 250_000,
    }

    # Negative validation: negative tokens, overflow, invalid model name.
    with pytest.raises(ValidationError):
        models.ReportUsage(
            workspace=workspace, token=worker["token"], input_tokens=-1, output_tokens=10
        )
    with pytest.raises(ValidationError):
        models.ReportUsage(
            workspace=workspace,
            token=worker["token"],
            input_tokens=10,
            output_tokens=10,
            model="bad model name with spaces",
        )

    # Foreign workspace token rejected.
    foreign_workspace = token_hex(32)
    foreign_engine = database(foreign_workspace)
    try:
        with foreign_engine.begin() as conn:
            foreign = repository.apply(
                conn,
                models.Create(
                    workspace=foreign_workspace, brief="Other", initiator="Other", invitees=["W"]
                ),
            )
    finally:
        foreign_engine.dispose()
    with pytest.raises(ValueError, match="Invalid participant token"):
        repository.apply(
            conversation_db,
            models.ReportUsage(
                workspace=workspace, token=foreign["token"], input_tokens=10, output_tokens=10
            ),
        )

    # Retired participant cannot report usage.
    repository.apply(
        conversation_db, models.Retire(workspace=workspace, token=created["token"], name="Worker")
    )
    with pytest.raises(ValueError, match="retired"):
        repository.apply(
            conversation_db,
            models.ReportUsage(
                workspace=workspace, token=worker["token"], input_tokens=10, output_tokens=10
            ),
        )

    # Closed conversation rejects report_usage and operator_set_member_budget.
    repository.apply(
        conversation_db, models.Close(workspace=workspace, token=created["token"], summary="Done")
    )
    with pytest.raises(ValueError, match="Conversation is closed"):
        repository.apply(
            conversation_db,
            models.ReportUsage(
                workspace=workspace, token=created["token"], input_tokens=10, output_tokens=10
            ),
        )
    with pytest.raises(ValueError, match="Conversation is closed"):
        operator.apply(
            conversation_db,
            workspace,
            operator_models.SetMemberBudget(
                version=1,
                key="op-key",
                directory="/work/repo",
                conversation=created["conversation"],
                action="operator_set_member_budget",
                name="Lead",
                token_budget=100_000,
            ),
        )


def test_waiting_read_records_estimated_tokens_once(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    """Waiting reads record read tokens once on delivery and never on non-waking timeouts."""
    from agentcy.mcp import concurrency
    from agentcy.tokens import estimator

    root = tmp_path / "bridge"
    root.mkdir(mode=0o700)
    monkeypatch.setenv("AGENTCY_HOME", str(root))
    monkeypatch.setattr(concurrency, "SETTLE_SECONDS", 0.05)
    ledger.clear_ledger_cache()
    workspace = token_hex(32)
    notifications = concurrency.Notifications()
    with concurrency.transaction(workspace) as conn:
        created = repository.apply(
            conn,
            models.Create(
                workspace=workspace,
                brief="Token check",
                initiator="Lead",
                initiator_model="claude-sonnet-4-6",
                invitees=[
                    models.Invitation(name="Target", model="gemini-2.5-pro"),
                    models.Invitation(name="Observer", model="gemini-2.5-pro"),
                ],
            ),
        )
        observer = repository.apply(
            conn, models.Join(workspace=workspace, code=created["invitations"]["Observer"])
        )
        msg_body = "Only for Target " * 10
        first = repository.apply(
            conn,
            models.Post(
                workspace=workspace,
                token=created["token"],
                submission_id="m1",
                body=msg_body,
                to="Target",
            ),
        )["message_id"]
        before = repository.apply(
            conn, models.Status(workspace=workspace, token=observer["token"])
        )["members"]["Observer"]["usage"]["input_tokens"]

    # Non-waking waiting read must not add estimated input tokens.
    quiet = concurrency.wait_for_read(
        models.Read(workspace=workspace, token=observer["token"], after=0, wait_seconds=1),
        notifications,
    )
    assert quiet["wake"] is False
    with concurrency.transaction(workspace) as conn:
        after_quiet = repository.apply(
            conn, models.Status(workspace=workspace, token=observer["token"])
        )["members"]["Observer"]["usage"]["input_tokens"]
    assert after_quiet == before

    # Waking read records tokens exactly once for the delivered messages.
    broadcast_body = "Broadcast update for everyone"
    with concurrency.transaction(workspace) as conn:
        repository.apply(
            conn,
            models.Post(
                workspace=workspace,
                token=created["token"],
                submission_id="m2",
                body=broadcast_body,
            ),
        )
    waking = concurrency.wait_for_read(
        models.Read(workspace=workspace, token=observer["token"], after=first, wait_seconds=1),
        notifications,
    )
    assert waking["wake"] is True
    with concurrency.transaction(workspace) as conn:
        after_wake = repository.apply(
            conn, models.Status(workspace=workspace, token=observer["token"])
        )["members"]["Observer"]["usage"]["input_tokens"]
    assert after_wake - before == estimator.estimate_text(broadcast_body)

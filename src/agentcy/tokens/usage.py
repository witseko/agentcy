"""Store and query per-participant token usage inside a workspace database."""

from dataclasses import dataclass

from sqlalchemy import Connection, select, update
from sqlalchemy.dialects.sqlite import insert as sqlite_insert

from agentcy.conversations.assignment_tables import assignment
from agentcy.conversations.tables import member
from agentcy.tokens import ledger
from agentcy.tokens.tables import participant_usage


@dataclass(frozen=True)
class SlotUsageSnapshot:
    """Hold one slot's effective token totals for syncing to the user-level ledger."""

    room: str
    slot: int
    model: str | None
    input_tokens: int
    output_tokens: int
    source: str
    updated: int


def sync_ledger(workspace: str, snapshot: SlotUsageSnapshot | None) -> None:
    """Persist a slot snapshot to the user-level model ledger when a model is bound."""
    if snapshot is None or not snapshot.model:
        return
    ledger.record_slot_usage(
        workspace=workspace,
        room=snapshot.room,
        slot=snapshot.slot,
        model=snapshot.model,
        input_tokens=snapshot.input_tokens,
        output_tokens=snapshot.output_tokens,
        source=snapshot.source,
        now=snapshot.updated,
    )


def effective_totals(row: dict) -> tuple[int, int, str]:
    """Resolve authoritative input/output token counts, preferring exact over estimated."""
    if row["reported_input"] is not None and row["reported_output"] is not None:
        return int(row["reported_input"]), int(row["reported_output"]), str(row["source"])
    return int(row["estimated_input"]), int(row["estimated_output"]), "estimated"


def slot_snapshot(connection: Connection, slot_id: int) -> SlotUsageSnapshot | None:
    """Read one slot's effective usage together with its bound model."""
    row = (
        connection.execute(
            select(
                participant_usage.c.room,
                participant_usage.c.slot,
                participant_usage.c.estimated_input,
                participant_usage.c.estimated_output,
                participant_usage.c.reported_input,
                participant_usage.c.reported_output,
                participant_usage.c.source,
                participant_usage.c.updated,
                member.c.model,
            )
            .join(member, member.c.id == participant_usage.c.slot)
            .where(participant_usage.c.slot == slot_id)
        )
        .mappings()
        .one_or_none()
    )
    if row is None:
        return None
    inp, out, source = effective_totals(dict(row))
    return SlotUsageSnapshot(
        room=row["room"],
        slot=row["slot"],
        model=row["model"],
        input_tokens=inp,
        output_tokens=out,
        source=source,
        updated=row["updated"],
    )


def add_estimated(
    connection: Connection,
    room_id: str,
    slot_id: int,
    *,
    input_tokens: int = 0,
    output_tokens: int = 0,
    now: int,
) -> SlotUsageSnapshot | None:
    """Increment bridge-estimated input/output token counts for a slot."""
    if input_tokens <= 0 and output_tokens <= 0:
        return None
    statement = sqlite_insert(participant_usage).values(
        slot=slot_id,
        room=room_id,
        estimated_input=input_tokens,
        estimated_output=output_tokens,
        source="estimated",
        updated=now,
    )
    statement = statement.on_conflict_do_update(
        index_elements=[participant_usage.c.slot],
        set_={
            "estimated_input": participant_usage.c.estimated_input + input_tokens,
            "estimated_output": participant_usage.c.estimated_output + output_tokens,
            "updated": now,
        },
    )
    connection.execute(statement)
    return slot_snapshot(connection, slot_id)


def record_exact(
    connection: Connection,
    room_id: str,
    slot_id: int,
    *,
    input_tokens: int,
    output_tokens: int,
    source: str,
    model: str | None = None,
    now: int,
) -> SlotUsageSnapshot | None:
    """Record exact cumulative usage reported by a participant or fresh-run plugin."""
    if model is not None:
        connection.execute(update(member).where(member.c.id == slot_id).values(model=model))
    statement = sqlite_insert(participant_usage).values(
        slot=slot_id,
        room=room_id,
        estimated_input=0,
        estimated_output=0,
        reported_input=input_tokens,
        reported_output=output_tokens,
        source=source,
        updated=now,
    )
    statement = statement.on_conflict_do_update(
        index_elements=[participant_usage.c.slot],
        set_={
            "reported_input": input_tokens,
            "reported_output": output_tokens,
            "source": source,
            "updated": now,
        },
    )
    connection.execute(statement)
    return slot_snapshot(connection, slot_id)


def set_member_budget(
    connection: Connection,
    slot_id: int,
    *,
    model: str | None = None,
    token_budget: int | None = None,
) -> SlotUsageSnapshot | None:
    """Update a participant's model and/or per-conversation starting token budget."""
    values: dict[str, str | int] = {}
    if model is not None:
        values["model"] = model
    if token_budget is not None:
        values["token_budget"] = token_budget
    if values:
        connection.execute(update(member).where(member.c.id == slot_id).values(**values))
    return slot_snapshot(connection, slot_id)


def room_usage(connection: Connection, room_id: str) -> dict[int, dict]:
    """Load per-slot model, budget override, and token totals for one conversation.

    Each slot keeps its bridge estimate in estimated_tokens and its exact reported or plugin
    total in actual_tokens (None until exact usage arrives).
    """
    rows = connection.execute(
        select(
            member.c.id.label("slot"),
            member.c.model,
            member.c.token_budget,
            assignment.c.plugin,
            participant_usage.c.estimated_input,
            participant_usage.c.estimated_output,
            participant_usage.c.reported_input,
            participant_usage.c.reported_output,
            participant_usage.c.source,
        )
        .outerjoin(participant_usage, participant_usage.c.slot == member.c.id)
        .outerjoin(assignment, assignment.c.slot == member.c.id)
        .where(member.c.room == room_id)
    ).mappings()
    result: dict[int, dict] = {}
    for row in rows:
        has_usage = row["source"] is not None
        if has_usage:
            inp, out, source = effective_totals(dict(row))
            estimated = int(row["estimated_input"]) + int(row["estimated_output"])
        else:
            inp, out, source, estimated = 0, 0, "estimated", 0
        result[row["slot"]] = {
            "model": row["model"] or row["plugin"],
            "token_budget": row["token_budget"],
            "input_tokens": inp,
            "output_tokens": out,
            "conversation_tokens": inp + out,
            "estimated_tokens": estimated,
            "actual_tokens": inp + out if source != "estimated" else None,
            "source": source,
            "has_usage": has_usage,
        }
    return result

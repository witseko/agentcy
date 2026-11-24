"""Persist cross-conversation per-model token spend in a private user-level SQLite ledger."""

from collections.abc import Iterable
from functools import lru_cache
from pathlib import Path

from sqlalchemy import Engine, create_engine, event, func, or_, select
from sqlalchemy.dialects.sqlite import insert as sqlite_insert

from agentcy.storage.database import data_dir
from agentcy.tokens.tables import ledger_metadata, model_usage

LEDGER_FILENAME = "token_ledger.sqlite3"
LEDGER_SCHEMA_VERSION = 1


@lru_cache(maxsize=16)
def ledger_engine(path_str: str) -> Engine:
    """Open and initialize the private cross-conversation token ledger."""
    engine = create_engine("sqlite:///" + path_str)

    @event.listens_for(engine, "connect")
    def configure(connection, _record):
        connection.execute("PRAGMA busy_timeout=5000")

    with engine.begin() as connection:
        connection.exec_driver_sql("PRAGMA journal_mode=WAL")
        connection.exec_driver_sql("BEGIN IMMEDIATE")
        version = connection.exec_driver_sql("PRAGMA user_version").scalar_one()
        if version == 0:
            ledger_metadata.create_all(connection)
            connection.exec_driver_sql(f"PRAGMA user_version={LEDGER_SCHEMA_VERSION}")
        elif version != LEDGER_SCHEMA_VERSION:
            raise ValueError("Unsupported token ledger database version")
    return engine


def ledger_path() -> Path:
    """Locate the user-level token ledger inside the private bridge data directory."""
    return data_dir() / LEDGER_FILENAME


def clear_ledger_cache() -> None:
    """Clear cached SQLAlchemy engines (used by tests)."""
    ledger_engine.cache_clear()


def record_slot_usage(
    *,
    workspace: str,
    room: str,
    slot: int,
    model: str,
    input_tokens: int,
    output_tokens: int,
    source: str,
    now: int,
) -> None:
    """Upsert one conversation slot's cumulative token spend into the user-level ledger."""
    total_tokens = input_tokens + output_tokens
    engine = ledger_engine(str(ledger_path()))
    statement = sqlite_insert(model_usage).values(
        workspace=workspace,
        room=room,
        slot=slot,
        model=model,
        input_tokens=input_tokens,
        output_tokens=output_tokens,
        total_tokens=total_tokens,
        source=source,
        updated=now,
    )
    statement = statement.on_conflict_do_update(
        index_elements=[model_usage.c.workspace, model_usage.c.room, model_usage.c.slot],
        set_={
            "model": statement.excluded.model,
            "input_tokens": statement.excluded.input_tokens,
            "output_tokens": statement.excluded.output_tokens,
            "total_tokens": statement.excluded.total_tokens,
            "source": statement.excluded.source,
            "updated": statement.excluded.updated,
        },
    )
    with engine.begin() as connection:
        connection.execute(statement)


def model_spend(
    models: Iterable[str],
    since_by_model: dict[str, int | None] | None = None,
) -> dict[str, int]:
    """Sum token spend per model across conversations within each model's active window."""
    unique_models = sorted({name for name in models if name})
    if not unique_models:
        return {}
    path = ledger_path()
    if not path.is_file():
        return {}
    since_map = since_by_model or {}
    predicates = []
    for name in unique_models:
        cutoff = since_map.get(name)
        if cutoff is not None:
            predicates.append((model_usage.c.model == name) & (model_usage.c.updated >= cutoff))
        else:
            predicates.append(model_usage.c.model == name)
    engine = ledger_engine(str(path))
    with engine.connect() as connection:
        rows = connection.execute(
            select(
                model_usage.c.model,
                func.coalesce(func.sum(model_usage.c.total_tokens), 0).label("total"),
            )
            .where(or_(*predicates))
            .group_by(model_usage.c.model)
        )
        return {row.model: int(row.total) for row in rows}

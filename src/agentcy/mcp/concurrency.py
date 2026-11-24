"""Coordinate short workspace transactions and release listeners after commits."""

import time
from collections.abc import Iterator
from contextlib import contextmanager
from threading import Condition, Lock
from weakref import WeakValueDictionary

from sqlalchemy import Connection

from agentcy.conversations import models, repository
from agentcy.storage.database import database

SETTLE_SECONDS = 2
WORKSPACE_LOCKS = tuple(Lock() for _ in range(64))


@contextmanager
def transaction(workspace: str) -> Iterator[Connection]:
    """Serialize SQLite requests without keeping a connection while a reader waits."""
    lock = WORKSPACE_LOCKS[int(workspace[:2], 16) % len(WORKSPACE_LOCKS)]
    with lock, database(workspace).begin() as connection:
        yield connection


class Notifications:
    """Share one condition among the active listeners of each conversation."""

    def __init__(self) -> None:
        self.lock = Lock()
        self.rooms: WeakValueDictionary[tuple[str, str], Condition] = WeakValueDictionary()

    def condition(self, workspace: str, room: str) -> Condition:
        """Keep the room's condition alive as long as a caller holds it."""
        key = (workspace, room)
        with self.lock:
            current = self.rooms.get(key)
            if current is None:
                current = Condition()
                self.rooms[key] = current
            return current

    def notify(self, workspace: str, room: str) -> None:
        """Wake room listeners after the transaction commits."""
        condition = self.condition(workspace, room)
        with condition:
            condition.notify_all()


def ready(page: dict) -> bool:
    """Recognize relevant messages, close, and retirement even without outcome text."""
    return (
        page.get("closed", False)
        or page.get("retired", False)
        or page.get("wake", bool(page["messages"]))
    )


def _record_delivered_usage(request: models.Read, page: dict) -> None:
    """Record estimated input tokens once when a waiting read actually delivers messages."""
    if not ready(page) or not page["messages"]:
        return
    with transaction(request.workspace) as connection:
        participant = repository.authenticate(connection, request.token)
        repository.record_read_usage(
            connection, participant, request.workspace, page["messages"], page.get("brief")
        )


def wait_for_read(request: models.Read, notifications: Notifications) -> dict:
    """Wait on committed room changes and settle a newly arrived burst."""
    with transaction(request.workspace) as connection:
        participant = repository.authenticate(connection, request.token)
    condition = notifications.condition(request.workspace, participant["room"])
    deadline = time.monotonic() + request.wait_seconds
    round_deadline = participant["round_deadline"]
    with condition:
        page = read_page(request)
        if ready(page):
            _record_delivered_usage(request, page)
            return page
        while (remaining := deadline - time.monotonic()) > 0:
            if round_deadline is not None:
                remaining = min(remaining, max(0, round_deadline - time.time()))
            condition.wait(remaining)
            page = read_page(request)
            round_deadline = page.get("round_deadline")
            if ready(page):
                settled = settle(request, condition, page)
                _record_delivered_usage(request, settled)
                return settled
        return page


def read_page(request: models.Read) -> dict:
    """Load one page in a transaction without counting intermediate polls toward token usage."""
    with transaction(request.workspace) as connection:
        participant = repository.refresh_participant(connection, request, int(time.time()))
        return repository.read(connection, participant, request, record_usage=False)


def settle(request: models.Read, condition: Condition, page: dict) -> dict:
    """Collect a short burst while returning promptly if the page is full, closed, or retired."""
    deadline = time.monotonic() + SETTLE_SECONDS
    while (
        not page.get("closed")
        and not page.get("retired")
        and not page.get("more")
        and (remaining := deadline - time.monotonic()) > 0
    ):
        condition.wait(remaining)
        page = read_page(request)
    return page

"""Dispatch, reconcile and cancel fresh runs outside SQLite transactions."""

import logging
import math
import time
from dataclasses import dataclass, field
from queue import Empty, Queue
from threading import Event, Thread

from sqlalchemy import RowMapping
from sqlalchemy.exc import SQLAlchemyError

from agents_chat.conversations import assignments
from agents_chat.mcp import plugin_client
from agents_chat.mcp.concurrency import Notifications, transaction
from agents_chat.mcp.plugin_contract import Start
from agents_chat.mcp.plugin_settings import Plugin, configured_plugin
from agents_chat.storage.database import data_dir

LOG = logging.getLogger(__name__)
POLL_SECONDS = 1


@dataclass(frozen=True)
class Dispatch:
    """Hold a committed launch request with its short-lived join credential."""

    workspace: str
    plugin: Plugin
    start: Start = field(repr=False)


class AssignmentWorker(Thread):
    """Own provider state updates and poll persisted assignments after a restart."""

    def __init__(self, notifications: Notifications) -> None:
        super().__init__(name="fresh-runs", daemon=True)
        self.notifications = notifications
        self.ready = Event()
        self.jobs: Queue[Dispatch | None] = Queue()

    def run(self) -> None:
        """Handle startup reconciliation, queued work and periodic deadlines."""
        self.scan(restart=True)
        self.ready.set()
        while True:
            try:
                job = self.jobs.get(timeout=POLL_SECONDS)
            except Empty:
                self.scan()
                continue
            if job is None:
                return
            try:
                self.launch(job)
            except (OSError, SQLAlchemyError):
                LOG.error("Fresh-run dispatch failed")
            self.scan()

    def launch(self, job: Dispatch) -> None:
        """Recheck durable state after the capability call and before starting."""
        rejected = False
        try:
            plugin_client.check_capabilities(job.plugin)
        except plugin_client.PluginError:
            rejected = True
        with transaction(job.workspace) as connection:
            rows = assignments.pending(connection)
            row = next((item for item in rows if item["id"] == job.start.assignment_id), None)
            if row is None or row["state"] != "queued":
                return
            terminal = self.stop_reason(row)
            if terminal is None and not rejected:
                assignments.record(connection, row, "starting")
        if terminal is not None or rejected:
            self.record(job.workspace, row, terminal or "failed")
            return
        timeout = max(1, math.ceil(row["deadline"] - time.time()))
        request = job.start.model_copy(update={"timeout_seconds": timeout})
        try:
            result = plugin_client.run_operation(job.plugin, "start", request)
        except plugin_client.PluginError:
            self.inspect(job.workspace, row)
            return
        if result.state != "starting":
            self.record(job.workspace, row, result.state, result.exit_code)

    @staticmethod
    def stop_reason(row: RowMapping) -> str | None:
        """Stop work on close, round transition, or assignment timeout."""
        if not row["open"] or (row["round"] and row["round"] != row.get("current_round")):
            return "cancelled"
        if row["round_deadline"] and row["round_deadline"] <= time.time():
            return "cancelled"
        if row["deadline"] <= time.time():
            return "timed_out"
        return None

    def inspect(self, workspace: str, row: RowMapping) -> None:
        """Inspect an active provider ID or request cancellation if its run must end."""
        terminal = self.stop_reason(row)
        try:
            plugin = configured_plugin(row["plugin"])
            operation = "cancel" if terminal else "inspect"
            result = plugin_client.run_operation(plugin, operation, row["id"])
        except ValueError:
            self.record(workspace, row, "unknown")
            return
        state = result.state
        if terminal == "timed_out" and state == "cancelled":
            state = terminal
        self.record(workspace, row, state, result.exit_code)

    def record(
        self, workspace: str, row: RowMapping, state: str, exit_code: int | None = None
    ) -> None:
        """Notify listeners only after a durable state event."""
        with transaction(workspace) as connection:
            added_event = assignments.record(connection, row, state, exit_code)
        if added_event:
            self.notifications.notify(workspace, row["room"])

    def scan(self, restart: bool = False) -> None:
        """Visit each private store with pending launches, isolating store failures."""
        for path in data_dir().glob("*.sqlite3"):
            workspace = path.stem
            try:
                with transaction(workspace) as connection:
                    rows = assignments.pending(connection)
                for row in rows:
                    if restart or row["state"] != "queued":
                        self.inspect(workspace, row)
            except (OSError, SQLAlchemyError, ValueError):
                LOG.error("Fresh-run reconciliation failed")

"""Dispatch, reconcile and cancel fresh runs outside SQLite transactions."""

import logging
import math
from dataclasses import dataclass, field
from queue import Empty, Queue
from threading import Event, Thread
from time import monotonic, time

from sqlalchemy import RowMapping
from sqlalchemy.exc import SQLAlchemyError

from agents_chat.conversations import assignments
from agents_chat.conversations.assignment_tables import PENDING_STATES
from agents_chat.mcp import plugin_client
from agents_chat.mcp.concurrency import Notifications, transaction
from agents_chat.mcp.plugin_contract import Start
from agents_chat.mcp.plugin_settings import Plugin, configured_plugin
from agents_chat.storage.database import data_dir

LOG = logging.getLogger(__name__)
POLL_SECONDS = 1
RETRY_MAX_SECONDS = 5
CANCEL_MAX_FAILURES = 5
CANCEL_POLL_SECONDS = 5
CANCEL_DEADLINE_GRACE_SECONDS = 30


@dataclass(frozen=True)
class Dispatch:
    """Hold a committed launch request with its short-lived join credential."""

    workspace: str
    plugin: Plugin
    start: Start = field(repr=False)


@dataclass(frozen=True)
class Retry:
    """Schedule another provider check while tracking consecutive failures."""

    operation: str
    failures: int
    next_attempt: float


class AssignmentWorker(Thread):
    """Own provider state updates and poll persisted assignments after a restart."""

    def __init__(self, notifications: Notifications) -> None:
        super().__init__(name="fresh-runs", daemon=True)
        self.notifications = notifications
        self.ready = Event()
        self.jobs: Queue[Dispatch | None] = Queue()
        self.retries: dict[str, Retry] = {}

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
        self.start_run(job, row)

    def start_run(self, job: Dispatch, row: RowMapping) -> None:
        """Send start once and inspect the same ID if its response is lost."""
        timeout = max(1, math.ceil(row["deadline"] - time()))
        request = job.start.model_copy(update={"timeout_seconds": timeout})
        try:
            result = plugin_client.run_operation(job.plugin, "start", request)
        except plugin_client.PluginError:
            try:
                result = plugin_client.run_operation(job.plugin, "inspect", row["id"])
            except plugin_client.PluginError:
                self.retry_operation(job.workspace, row, "inspect")
                return
        if result.state == "unknown":
            self.retry_operation(job.workspace, row, "inspect")
            return
        if result.state != "starting":
            self.record(job.workspace, row, result.state, result.exit_code)

    @staticmethod
    def stop_reason(row: RowMapping) -> str | None:
        """Stop work on close, round transition, or assignment timeout."""
        if not row["open"] or (row["round"] and row["round"] != row.get("current_round")):
            return "cancelled"
        if row["round_deadline"] and row["round_deadline"] <= time():
            return "cancelled"
        if row["deadline"] <= time():
            return "timed_out"
        return None

    def inspect(self, workspace: str, row: RowMapping) -> None:
        """Inspect an active provider ID or request cancellation if its run must end."""
        terminal = self.stop_reason(row)
        operation = "cancel" if terminal else "inspect"
        retry = self.retries.get(row["id"])
        if retry and retry.operation == operation and monotonic() < retry.next_attempt:
            return
        try:
            plugin = configured_plugin(row["plugin"])
            result = plugin_client.run_operation(plugin, operation, row["id"])
        except ValueError:
            state, exit_code = "unknown", None
        else:
            state, exit_code = result.state, result.exit_code
        if state == "unknown":
            self.reconcile_unconfirmed(workspace, row, operation)
            return
        if terminal and state in PENDING_STATES:
            self.poll_stopping_run(workspace, row, state, exit_code)
            return
        if terminal == "timed_out" and state == "cancelled":
            state = terminal
        self.record(workspace, row, state, exit_code)

    def poll_stopping_run(
        self, workspace: str, row: RowMapping, state: str, exit_code: int | None
    ) -> None:
        """Recheck acknowledged cancellation through the run deadline and a short grace period."""
        if time() >= row["deadline"] + CANCEL_DEADLINE_GRACE_SECONDS:
            self.record(workspace, row, "unknown")
            return
        if state != row["state"]:
            self.record(workspace, row, state, exit_code)
        self.retries[row["id"]] = Retry("cancel", 0, monotonic() + CANCEL_POLL_SECONDS)

    def reconcile_unconfirmed(self, workspace: str, row: RowMapping, operation: str) -> None:
        """Retry a check on provider work that may have started."""
        if row["state"] == "queued":
            # A restarted worker has no join credential for the undelivered launch.
            self.record(workspace, row, "unknown")
            return
        self.retry_operation(workspace, row, operation)

    def retry_operation(self, workspace: str, row: RowMapping, operation: str) -> None:
        """Back off failed checks and cap consecutive failures during cancellation."""
        assignment_id = row["id"]
        previous = self.retries.get(assignment_id)
        failures = 1
        if previous and previous.operation == operation:
            failures = min(previous.failures + 1, CANCEL_MAX_FAILURES)
        if operation == "cancel" and failures >= CANCEL_MAX_FAILURES:
            self.record(workspace, row, "unknown")
            return
        if failures == 1:
            with transaction(workspace) as connection:
                assignments.report_unconfirmed(connection, row, operation)
            self.notifications.notify(workspace, row["room"])
        delay = min(2 ** (failures - 1), RETRY_MAX_SECONDS)
        self.retries[assignment_id] = Retry(operation, failures, monotonic() + delay)

    def record(
        self, workspace: str, row: RowMapping, state: str, exit_code: int | None = None
    ) -> None:
        """Commit the state update, clear its prior retry, and notify listeners of an event."""
        with transaction(workspace) as connection:
            added_event = assignments.record(connection, row, state, exit_code)
        self.retries.pop(row["id"], None)
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

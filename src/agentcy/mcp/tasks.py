"""Answer launch_participant with an MCP task through the MCP Tasks extension.

The extension (`io.modelcontextprotocol/tasks`, SEP-2663) lets a server answer a
`tools/call` with an MCP task, which the host then polls with `tasks/get` and may stop
with `tasks/cancel`. An MCP task is a protocol object, not a conversation task.

The core keeps the state: the MCP task is the persisted fresh-run assignment, found by
the digest of its MCP task ID. This module only maps that state to the wire form.
"""

import asyncio
import json
from collections.abc import Callable, Sequence
from datetime import UTC, datetime
from typing import Any

from mcp.server.context import CallNext, HandlerResult, ServerRequestContext
from mcp.server.extension import Extension, MethodBinding
from mcp.server.mcpserver import Context
from mcp.server.mcpserver.server import require_client_extension
from mcp.shared.exceptions import MCPError
from mcp.types import (
    INTERNAL_ERROR,
    INVALID_PARAMS,
    CallToolRequestParams,
    CallToolResult,
    RequestParams,
    TextContent,
)
from mcp_types.version import MODERN_PROTOCOL_VERSIONS
from pydantic import ValidationError

from agentcy.conversations import models
from agentcy.conversations.assignment_tables import PENDING_STATES
from agentcy.conversations.assignments import UNKNOWN_MCP_TASK

EXTENSION_ID = "io.modelcontextprotocol/tasks"
METHOD_GET = "tasks/get"
METHOD_CANCEL = "tasks/cancel"
METHOD_UPDATE = "tasks/update"
LAUNCH_TOOL = "launch_participant"
# The key under which the launch_participant tool result carries a new MCP task to the
# interceptor. The interceptor always replaces such a result, so the key never reaches a host.
MCP_TASK_KEY = "mcp_task"
POLL_INTERVAL_MS = 5000
# The bridge never deletes assignments, so an MCP task has no time limit.
TTL_MS = None
# Launch states that end a fresh run without a cancel, with the text for the host.
ENDED_MESSAGES = {
    "finished": "The fresh run for {name} finished. Read the conversation for its messages.",
    "failed": (
        "The fresh run for {name} failed. Read the conversation for its events and open tasks."
    ),
    "timed_out": (
        "The fresh run for {name} reached its deadline. Read the conversation for its "
        "events and open tasks."
    ),
    "unknown": (
        "The bridge cannot confirm the state of the fresh run for {name}. Check the "
        "provider before you launch a replacement."
    ),
}
# Endings that the host should report as a failed tool call.
FAILED_ENDINGS = frozenset({"failed", "timed_out", "unknown"})

Exchange = Callable[[models.Request], dict]


class TaskParams(RequestParams):
    """Parameters for `tasks/get` and `tasks/cancel`."""

    task_id: str


class UpdateTaskParams(TaskParams):
    """Parameters for `tasks/update`. The bridge never asks for input, so they are ignored."""

    input_responses: dict[str, Any]


def wants_mcp_task(context: Context) -> bool:
    """Tell whether this tool call may get an MCP task instead of its normal result.

    Only a 2026-07-28 request that declares the extension in its own client capabilities
    qualifies. The spec forbids an MCP task for any other request.
    """
    if context.protocol_version not in MODERN_PROTOCOL_VERSIONS:
        return False
    capabilities = context.client_capabilities
    return bool(
        capabilities and capabilities.extensions and EXTENSION_ID in capabilities.extensions
    )


def iso_time(seconds: int) -> str:
    """Format Unix seconds as an ISO 8601 UTC timestamp."""
    return datetime.fromtimestamp(seconds, UTC).strftime("%Y-%m-%dT%H:%M:%SZ")


def tool_result(name: str, launch_state: str, message: str, is_error: bool) -> dict[str, Any]:
    """Build the CallToolResult that a finished MCP task returns."""
    summary = {"participant": name, "launch_state": launch_state, "next": message}
    return {
        "content": [{"type": "text", "text": json.dumps(summary)}],
        "structuredContent": summary,
        "isError": is_error,
    }


def wire_task(task_id: str, view: dict[str, Any]) -> dict[str, Any]:
    """Map the core's view of a fresh run to the fields of an MCP task.

    A run that ends by failure, timeout, or an uncertain state is a completed MCP task
    whose result has isError. The spec reserves the failed status for JSON-RPC errors.

    Raises:
        MCPError: If the launch state has no mapping, so a new state cannot pass silently.
    """
    launch_state, name = view["launch_state"], view["participant"]
    task: dict[str, Any] = {
        "taskId": task_id,
        "createdAt": iso_time(view["created"]),
        "lastUpdatedAt": iso_time(view["updated"]),
        "ttlMs": TTL_MS,
        "pollIntervalMs": POLL_INTERVAL_MS,
    }
    if launch_state in PENDING_STATES:
        message = f"The fresh run for {name} is {launch_state}."
        return {**task, "status": "working", "statusMessage": message}
    if launch_state == "cancelled":
        message = f"The fresh run for {name} was cancelled."
        return {**task, "status": "cancelled", "statusMessage": message}
    if launch_state in ENDED_MESSAGES:
        message = ENDED_MESSAGES[launch_state].format(name=name)
        result = tool_result(name, launch_state, message, launch_state in FAILED_ENDINGS)
        return {**task, "status": "completed", "statusMessage": message, "result": result}
    raise MCPError(code=INTERNAL_ERROR, message=f"Unmapped launch state {launch_state!r}")


def unknown_task() -> MCPError:
    """Return the one error for every task ID that this workspace does not know."""
    return MCPError(code=INVALID_PARAMS, message=UNKNOWN_MCP_TASK)


class McpTasksExtension(Extension):
    """Turn a new launch_participant run into an MCP task and serve its three methods.

    The methods exist only on 2026-07-28 connections, and each request must declare the
    extension. The MCP task ID is the only credential, so it is never logged.
    """

    identifier = EXTENSION_ID

    def __init__(self, workspace: str, exchange: Exchange) -> None:
        self._workspace = workspace
        self._exchange = exchange

    def methods(self) -> Sequence[MethodBinding]:
        """Bind `tasks/get`, `tasks/cancel`, and `tasks/update` for 2026-07-28 clients."""
        versions = frozenset(MODERN_PROTOCOL_VERSIONS)
        return [
            MethodBinding(METHOD_GET, TaskParams, self._get, protocol_versions=versions),
            MethodBinding(METHOD_CANCEL, TaskParams, self._cancel, protocol_versions=versions),
            MethodBinding(
                METHOD_UPDATE, UpdateTaskParams, self._update, protocol_versions=versions
            ),
        ]

    async def intercept_tool_call(
        self,
        params: CallToolRequestParams,
        ctx: ServerRequestContext[Any, Any],
        call_next: CallNext,
    ) -> HandlerResult:
        """Replace a launch_participant result that carries a new MCP task with that task.

        The tool returns a dict, which the SDK serializes as one JSON text block.
        """
        result = await call_next(ctx)
        if params.name != LAUNCH_TOOL or not isinstance(result, CallToolResult):
            return result
        if result.is_error or len(result.content) != 1:
            return result
        (content,) = result.content
        if not isinstance(content, TextContent):
            return result
        launched = json.loads(content.text)
        if MCP_TASK_KEY not in launched:
            return result
        created = launched[MCP_TASK_KEY]
        return {"resultType": "task", **wire_task(created["task_id"], created)}

    async def _call_core(self, request_type: type[models.Request], task_id: str) -> dict:
        """Send one MCP task request to the core and translate its rejections."""
        try:
            request = request_type(workspace=self._workspace, task_id=task_id)
        except ValidationError:
            # No generated ID has this shape, so the core cannot know it.
            raise unknown_task() from None
        try:
            return await asyncio.to_thread(self._exchange, request)
        except ValueError as error:
            if str(error) == UNKNOWN_MCP_TASK:
                raise unknown_task() from None
            raise MCPError(code=INTERNAL_ERROR, message=str(error)) from None

    async def _get(self, ctx: ServerRequestContext[Any, Any], params: TaskParams) -> HandlerResult:
        """Return the current state of the fresh run behind an MCP task."""
        require_client_extension(ctx, EXTENSION_ID)
        view = await self._call_core(models.McpTaskStatus, params.task_id)
        return wire_task(params.task_id, view)

    async def _cancel(
        self, ctx: ServerRequestContext[Any, Any], params: TaskParams
    ) -> HandlerResult:
        """Ask the core to cancel the fresh run. A finished run stays as it is."""
        require_client_extension(ctx, EXTENSION_ID)
        await self._call_core(models.McpTaskCancel, params.task_id)
        return {}

    async def _update(
        self, ctx: ServerRequestContext[Any, Any], params: UpdateTaskParams
    ) -> HandlerResult:
        """Acknowledge input for a known MCP task. The bridge never requests input."""
        require_client_extension(ctx, EXTENSION_ID)
        await self._call_core(models.McpTaskStatus, params.task_id)
        return {}

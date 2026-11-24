"""Define version one of the fresh-run MCP contract."""

from typing import Annotated, Literal

from pydantic import BaseModel, ConfigDict, Field, StringConstraints

Version = Literal[1]
AssignmentId = Annotated[str, StringConstraints(pattern=r"^[a-f0-9]{32}$")]
RunState = Literal["starting", "running", "finished", "failed", "cancelled", "timed_out", "unknown"]
Timeout = Annotated[int, Field(ge=1, le=3600)]
TokenCap = Annotated[int, Field(gt=0, le=100_000_000)]
CostCap = Annotated[int, Field(gt=0, le=100_000_000)]
TokenUsage = Annotated[int, Field(ge=0, le=1_000_000_000)]


class ContractModel(BaseModel):
    """Validate cross-process messages without including their contents in errors."""

    model_config = ConfigDict(extra="forbid", hide_input_in_errors=True)


class Capabilities(ContractModel):
    """Declare the version and optional hard limits a plugin enforces."""

    version: Version
    token_cap: bool = False
    cost_cap: bool = False


class AssignmentRef(ContractModel):
    """Identify an assignment consistently across all plugin calls."""

    assignment_id: AssignmentId


class Start(AssignmentRef):
    """Launch a worker bound to a local read-only workspace and bridge."""

    workspace: Annotated[str, StringConstraints(min_length=1, max_length=4096)]
    bridge_home: Annotated[str, StringConstraints(min_length=1, max_length=4096)]
    join_code: Annotated[str, StringConstraints(min_length=1, max_length=128)]
    model: Annotated[str, StringConstraints(min_length=1, max_length=256)]
    timeout_seconds: Timeout
    token_cap: TokenCap | None = None
    cost_cap_microusd: CostCap | None = None


class RunResult(ContractModel):
    """Report a provider state without returning native identifiers or content."""

    state: RunState
    exit_code: int | None = None
    input_tokens: TokenUsage | None = None
    output_tokens: TokenUsage | None = None

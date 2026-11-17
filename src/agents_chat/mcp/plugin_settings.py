"""Load fresh-run plugin settings from private user-owned configuration."""

import os
import stat
from pathlib import Path
from typing import Annotated

from pydantic import Field, StringConstraints, ValidationError, field_validator

from agents_chat.mcp.plugin_contract import ContractModel, CostCap, Timeout, TokenCap
from agents_chat.storage.database import data_dir

PluginName = Annotated[str, StringConstraints(pattern=r"^[a-zA-Z0-9-]{1,64}$")]
EnvironmentName = Annotated[str, StringConstraints(pattern=r"^[A-Z_][A-Z0-9_]{0,127}$")]
PluginArgument = Annotated[str, StringConstraints(max_length=4096, pattern=r"^[^\x00]*$")]


class Plugin(ContractModel):
    """Describe one local executable and its user-approved model and limits."""

    command: Annotated[list[PluginArgument], Field(min_length=1, max_length=32)]
    model: Annotated[str, StringConstraints(min_length=1, max_length=256)]
    timeout_seconds: Timeout = 300
    token_cap: TokenCap | None = None
    cost_cap_microusd: CostCap | None = None
    environment: Annotated[list[EnvironmentName], Field(max_length=32)] = Field(
        default_factory=list
    )

    @field_validator("command")
    @classmethod
    def check_executable(cls, value: list[str]) -> list[str]:
        """Require the user to register a specific executable path."""
        if not Path(value[0]).is_absolute():
            raise ValueError("Plugin executable must be an absolute path")
        return value


class Settings(ContractModel):
    """Register local plugins by a name accepted by the launch tool."""

    plugins: dict[PluginName, Plugin] = Field(default_factory=dict)


def configured_plugin(name: str) -> Plugin:
    """Read a private configuration and resolve the requested plugin by name."""
    path = data_dir() / "plugins.json"
    try:
        info = path.lstat()
        if (
            not stat.S_ISREG(info.st_mode)
            or info.st_uid != os.getuid()
            or info.st_mode & 0o077
            or info.st_size > 65_536
        ):
            raise ValueError("Plugin configuration must be a private file")
        settings = Settings.model_validate_json(path.read_bytes())
    except FileNotFoundError:
        raise ValueError("Fresh-run plugin is not configured") from None
    except (OSError, ValidationError):
        raise ValueError("Invalid fresh-run plugin configuration") from None
    if name not in settings.plugins:
        raise ValueError("Fresh-run plugin is not configured")
    return settings.plugins[name]

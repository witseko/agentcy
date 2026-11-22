"""Load user-level model token budgets from private configuration."""

import os
import stat
from typing import Annotated

from pydantic import BaseModel, ConfigDict, Field, StringConstraints, ValidationError

from agents_chat.storage.database import data_dir

ModelName = Annotated[str, StringConstraints(pattern=r"^[a-zA-Z0-9._:/-]{1,128}$")]
TokenCount = Annotated[int, Field(strict=True, ge=0, le=1_000_000_000)]
TokenBudget = Annotated[int, Field(strict=True, gt=0, le=1_000_000_000)]
PercentThreshold = Annotated[int, Field(strict=True, ge=1, le=99)]
GapThreshold = Annotated[int, Field(strict=True, ge=5, le=90)]
ResetWindowHours = Annotated[int, Field(strict=True, ge=1, le=720)]

DEFAULT_WARN_PERCENT = 75
DEFAULT_IMBALANCE_GAP_PERCENT = 20
MAX_SETTINGS_BYTES = 65_536


class TokenConfigModel(BaseModel):
    """Validate token configuration without echoing raw inputs in error messages."""

    model_config = ConfigDict(extra="forbid", hide_input_in_errors=True)


class ModelBudget(TokenConfigModel):
    """Describe one model's starting token allowance and soft balancing thresholds."""

    initial_tokens: TokenBudget
    warn_percent: PercentThreshold | None = None
    imbalance_gap_percent: GapThreshold | None = None
    reset_window_hours: ResetWindowHours | None = None


class TokenSettings(TokenConfigModel):
    """Configure starting token budgets per model in ~/.agents-chat/tokens.json."""

    models: dict[ModelName, ModelBudget] = Field(default_factory=dict)
    default_initial_tokens: TokenBudget | None = None
    warn_percent: PercentThreshold = DEFAULT_WARN_PERCENT
    imbalance_gap_percent: GapThreshold = DEFAULT_IMBALANCE_GAP_PERCENT

    def budget_for(self, model: str | None) -> ModelBudget | None:
        """Resolve an exact or case-insensitive model budget entry."""
        if not model:
            return None
        if model in self.models:
            return self.models[model]
        folded = model.casefold()
        for key, value in self.models.items():
            if key.casefold() == folded:
                return value
        return None

    def initial_tokens_for(self, model: str | None, override: int | None = None) -> int | None:
        """Return the per-conversation override, model budget, or global default."""
        if override is not None:
            return override
        entry = self.budget_for(model)
        if entry is not None:
            return entry.initial_tokens
        return self.default_initial_tokens

    def warn_percent_for(self, model: str | None) -> int:
        """Return the soft warning percentage threshold for a model."""
        entry = self.budget_for(model)
        if entry is not None and entry.warn_percent is not None:
            return entry.warn_percent
        return self.warn_percent

    def reset_window_seconds_for(self, model: str | None) -> int | None:
        """Return the rolling window in seconds for a model, or None for cumulative tracking."""
        entry = self.budget_for(model)
        if entry is None or entry.reset_window_hours is None:
            return None
        return entry.reset_window_hours * 3600


def read_settings() -> TokenSettings | None:
    """Read ~/.agents-chat/tokens.json, or return None when not configured."""
    path = data_dir() / "tokens.json"
    try:
        info = path.lstat()
        if (
            not stat.S_ISREG(info.st_mode)
            or info.st_uid != os.getuid()
            or info.st_mode & 0o077
            or info.st_size > MAX_SETTINGS_BYTES
        ):
            raise ValueError("Token configuration must be a private file")
        return TokenSettings.model_validate_json(path.read_bytes())
    except FileNotFoundError:
        return None
    except (OSError, ValidationError):
        raise ValueError("Invalid token configuration") from None

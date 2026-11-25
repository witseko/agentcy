"""Ask the human to confirm closing a conversation through an MCP elicitation form.

A tool parameter annotated with `Resolve(confirm_close)` is filled before the tool body
runs and is hidden from the tool input schema. The resolver asks only when the request
declared form elicitation. Otherwise it returns a ready confirmation, so the SDK sends no
question and the tool behaves as before.

The confirmation is a safeguard against a model acting by mistake. The core still
authorizes the action when it runs.
"""

from typing import Annotated

from mcp.server.mcpserver import (
    AcceptedElicitation,
    CancelledElicitation,
    Context,
    Elicit,
    ElicitationResult,
    Resolve,
)
from pydantic import BaseModel, Field


class Confirmation(BaseModel):
    """The one-field form that the human answers."""

    confirm: bool = Field(title="Confirm", description="Select to confirm this action.")


# The answer a resolver returns when it does not ask.
NOT_ASKED = Confirmation(confirm=True)


def declares_form_elicitation(context: Context) -> bool:
    """Tell whether this request may get a form elicitation.

    A bare `elicitation: {}` means form mode. A client that declares only URL mode
    does not qualify, and a call outside a request has no client to ask.
    """
    if context.protocol_version is None:
        return False
    capabilities = context.client_capabilities
    elicitation = capabilities.elicitation if capabilities is not None else None
    return elicitation is not None and (elicitation.form is not None or elicitation.url is None)


async def confirm_close(ctx: Context) -> Elicit[Confirmation] | Confirmation:
    """Ask the human to confirm closing the conversation, when the client can ask."""
    if not declares_form_elicitation(ctx):
        return NOT_ASKED
    return Elicit("Close this conversation?", Confirmation)


CloseConfirmation = Annotated[ElicitationResult[Confirmation], Resolve(confirm_close)]


def require_confirmation(answer: ElicitationResult[Confirmation]) -> None:
    """Return when the human confirmed, or raise the tool error for the calling model.

    An accept with `confirm` false counts as a decline.

    Raises:
        ValueError: If the human declined or cancelled.
    """
    if isinstance(answer, AcceptedElicitation) and answer.data.confirm:
        return
    verb = "cancelled" if isinstance(answer, CancelledElicitation) else "declined"
    raise ValueError(
        f"The user {verb} this action, so nothing changed. "
        "Do not retry it unless the user asks again."
    )

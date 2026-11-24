"""Submit one invoice to the accounting API."""

import uuid

MAX_ATTEMPTS = 3


class Timeout(Exception):
    """The accounting API did not answer in time."""


def submit_invoice(api, invoice: dict) -> str:
    """Send ``invoice`` and return the API's receipt ID, retrying after a timeout."""
    for attempt in range(1, MAX_ATTEMPTS + 1):
        request_id = str(uuid.uuid4())
        try:
            return api.post("/invoices", invoice, request_id=request_id)
        except Timeout:
            if attempt == MAX_ATTEMPTS:
                raise
    raise AssertionError("unreachable")

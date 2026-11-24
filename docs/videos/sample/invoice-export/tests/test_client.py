"""Tests for submit_invoice."""

from invoice_export.client import Timeout, submit_invoice


class SlowOnceApi:
    """An API that times out on the first call, then answers."""

    def __init__(self):
        self.request_ids = []

    def post(self, path, invoice, request_id):
        self.request_ids.append(request_id)
        if len(self.request_ids) == 1:
            raise Timeout
        return "receipt-1"


def test_submit_retries_after_a_timeout():
    api = SlowOnceApi()
    assert submit_invoice(api, {"number": "INV-1001"}) == "receipt-1"
    assert len(api.request_ids) == 2

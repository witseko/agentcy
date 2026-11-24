"""Write invoices to a CSV file."""

import csv
from pathlib import Path

COLUMNS = ["number", "customer", "issued", "amount", "currency"]


def export_csv(invoices: list[dict], path: Path) -> None:
    """Write ``invoices`` to ``path`` with one row per invoice."""
    with path.open("w", newline="") as file:
        writer = csv.DictWriter(file, fieldnames=COLUMNS)
        writer.writeheader()
        writer.writerows(invoices)

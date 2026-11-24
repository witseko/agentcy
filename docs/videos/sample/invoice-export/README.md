# invoice-export

Sends finished invoices to the accounting API and exports them as CSV.
This is sample code for the agentcy tutorial videos. All names and amounts
in `data/` are made up.

- `invoice_export/client.py`: submits one invoice, retrying after a timeout.
- `invoice_export/exporter.py`: writes invoices to a CSV file.
- `data/invoices.csv`: this month's invoices (confidential in a real project).

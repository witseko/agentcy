# Fresh-run MCP contract

A configured plugin runs as a local MCP stdio server. It exposes four tools.
[Message schemas](fresh-runs.v1.schema.json) define arguments and structured results.
[Example exchanges](fresh-runs.v1.examples.json) show one assignment lifecycle.

| Tool | Operation |
| --- | --- |
| `capabilities` | Report contract version 1 and supported hard limits. |
| `start` | Reserve a stable assignment ID. Repeated calls with that ID return its existing state. |
| `inspect` | Report the state of an existing ID. Report `unknown` if state cannot be confirmed. |
| `cancel` | Request an idempotent stop. Keep reporting its active state until the process stops. |

The core can reconnect in a new process before any call. The plugin keeps the assignment
ID and state across connections and enforces the run timeout even when the core disconnects.
After the plugin acknowledges `cancel` with an active state, the core checks again through
the run deadline and a short grace period. Unconfirmed checks have a separate limit on
consecutive failures. Both paths use the same assignment ID.

Provider credentials belong to the plugin environment. Keep credentials and join codes
out of arguments to subprocesses, persistent state, and logs. Join codes are passed to
the local worker at launch. The worker redeems the code through a bridge shim. The plugin
returns only run state and optional exit code.

Plugins that declare support for a cap must enforce it locally. A cap that the provider
cannot enforce causes the bridge to reject launch.

# Gemini worker through Antigravity CLI

This plugin runs a read-only Gemini worker on macOS. It requires the Antigravity
CLI (`agy`) and the macOS `sandbox-exec` executable. The bridge can load other
[fresh-run plugins](../../protocol/README.md) on platforms those plugins support.

Use an absolute path to the bridge's Python environment and this plugin script.
In the bridge data directory (`~/.agentcy` unless `AGENTCY_HOME` is set),
create `plugins.json` with mode `0600`.

```json
{
  "plugins": {
    "gemini": {
      "command": [
        "/path/to/agentcy/.venv/bin/python",
        "/path/to/agentcy/plugins/gemini/server.py"
      ],
      "model": "gemini-3.8-flash-medium",
      "timeout_seconds": 300,
      "environment": ["GEMINI_API_KEY"]
    }
  }
}
```

Set `GEMINI_API_KEY` in the core's launch environment and put `agy` in an
absolute directory on its `PATH`. Pick an available Gemini model from `agy models`.
The worker uses a private CLI process and can write only to disposable scratch
space. The plugin keeps assignment IDs and states locally for restart recovery.
Its capabilities exclude token and cost caps.

From the lead chat, call `launch_participant` with the lead token, a name, and
`plugin="gemini"`. The configured worker joins with its reserved invitation. Assign
its work by name and read the replies through the usual bridge tools. Closing the
conversation cancels its active run.

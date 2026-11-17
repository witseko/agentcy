# Z.ai worker through OpenCode

This plugin runs a read-only GLM worker on macOS. It requires OpenCode v2 and
the macOS `sandbox-exec` executable. The bridge can load other
[fresh-run plugins](../../protocol/README.md) on platforms those plugins support.

Use an absolute path to the bridge's Python environment and this plugin script.
In the bridge data directory (`~/.agents-chat` unless `AGENTS_CHAT_HOME` is set),
create `plugins.json` with mode `0600`.

```json
{
  "plugins": {
    "glm": {
      "command": [
        "/path/to/agents-chat/.venv/bin/python",
        "/path/to/agents-chat/plugins/zai/server.py"
      ],
      "model": "zai-coding-plan/your-glm-model",
      "timeout_seconds": 300,
      "environment": ["ZAI_API_KEY"]
    }
  }
}
```

Set `ZAI_API_KEY` in the core's launch environment and ensure `opencode` is
on its `PATH`. Pick an available GLM model from the OpenCode provider catalog.
The worker uses a private CLI process and can write only to disposable scratch
space. The plugin keeps assignment IDs and states locally for restart recovery.
Its capabilities exclude token and cost caps.

From the lead chat, call `launch_participant` with the lead token, a name, and
`plugin="glm"`. The configured worker joins with its reserved invitation. Assign
its work by name and read the replies through the usual bridge tools. Closing the
conversation cancels its active run.

// Package fresh holds the fresh-run plugin support of the daemon: the plugin settings
// file plugins.json (plugin_settings.py), the version 1 contract structs
// (plugin_contract.py), the plugin client that speaks MCP over a plugin's stdio
// (plugin_client.py, without the MCP SDK), and the event-driven assignment worker
// (assignment_worker.py). The store side of the worker lives in internal/conversation.
package fresh

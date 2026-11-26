// Package protocol embeds the checked-in protocol files that the Go code serves verbatim.
package protocol

import _ "embed"

// The tools/list results of the Python shim and fresh worker. Tool names, descriptions
// and schemas are prompt text, so the Go shim serves them from here byte for byte. The
// Python MCP SDK writes the members of each input schema in a different order in the two
// protocol eras, so each era has its own file: the 2026-07-28 era in *.golden.json and the
// 2025-11-25 handshake era in *.legacy.golden.json. The shim's files are copies of
// mcp-transcripts/tools-list.<era>.json; the fresh worker's were captured from
// the former Python fresh worker the same way.
var (
	//go:embed mcp-tools.golden.json
	MCPTools []byte

	//go:embed mcp-tools.legacy.golden.json
	MCPToolsLegacy []byte

	//go:embed mcp-tools.fresh-worker.golden.json
	FreshWorkerTools []byte

	//go:embed mcp-tools.fresh-worker.legacy.golden.json
	FreshWorkerToolsLegacy []byte
)

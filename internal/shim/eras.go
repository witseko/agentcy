package shim

import "slices"

// The MCP protocol versions, from mcp_types/version.py in the Python MCP SDK that the
// Python shim uses. Versions are a fixed set, not an ordered scale: comparing the texts
// would count "invalid" or any future date as the 2026-07-28 era.
const (
	// modernEra is the one version of the stateless per-request era.
	modernEra = "2026-07-28"
)

// handshakeEras are the versions that the initialize handshake negotiates.
var handshakeEras = []string{"2024-11-05", "2025-03-26", "2025-06-18", "2025-11-25"}

// isModernEra reports whether a request's protocol version is the 2026-07-28 era. Like
// the Python shim (MODERN_PROTOCOL_VERSIONS), it treats every other version, known or
// unknown, as older: an empty, malformed, or future version gets no 2026-07-28 behavior.
func isModernEra(version string) bool {
	return version == modernEra
}

// routesToModernHandler reports whether an HTTP request goes to the stateless handler of
// the 2026-07-28 era. Like the Python SDK's HTTP session manager, only a known handshake
// version or a missing header goes to the stateful handler; any other version goes to the
// stateless one, which rejects a version it does not support with a structured error.
// An empty header counts as missing.
func routesToModernHandler(headerVersion string) bool {
	return headerVersion != "" && !slices.Contains(handshakeEras, headerVersion)
}

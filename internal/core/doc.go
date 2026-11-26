// Package core is the agentcy daemon: one process per user that owns the stores and serves
// member and operator requests on the Unix socket core.sock in the bridge data directory.
//
// It is a port of src/agentcy/mcp/core.py and src/agentcy/mcp/concurrency.py. The package
// owns the process and the socket: the data directory lock, the operator key, request
// framing, the operator key check, waiting reads, idle exit, and shutdown. The conversation
// rules live behind the Handler interface, which internal/conversation implements.
//
// Run is the only entry point. It changes two pieces of process state, both documented on
// Run: the umask and the handling of SIGTERM and SIGINT. The package has no other global
// mutable state.
package core

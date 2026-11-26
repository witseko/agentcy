#!/bin/sh
# Self-contained runner for the k6 HTTP MCP performance tests.
#
# Creates an isolated temporary AGENTCY_HOME and workspace under /tmp (mode 0700),
# starts `agentcy --workspace ... --http PORT` with an ephemeral random Bearer token,
# runs `k6 run bench/k6/http_load.js`, and stops both the HTTP shim and the core daemon
# on exit, error, or Ctrl-C.
set -eu

fail() {
    echo "bench/k6: $1" >&2
    exit 1
}

SCRIPT_DIR="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
REPO_ROOT="$(CDPATH='' cd -- "$SCRIPT_DIR/../.." && pwd)"
AGENTCY_BIN="${AGENTCY_BIN:-$REPO_ROOT/bin/agentcy}"
K6_PROFILE="${K6_PROFILE:-smoke}"
K6_ERA="${K6_ERA:-both}"
K6_SUMMARY_EXPORT="${K6_SUMMARY_EXPORT:-}"
STARTUP_TIMEOUT_SECONDS=10

command -v k6 >/dev/null 2>&1 || fail "k6 is not installed (install via: brew install k6)"
test -x "$AGENTCY_BIN" || fail "agentcy binary not found or not executable: $AGENTCY_BIN (run: make build)"

generate_token() {
    if command -v openssl >/dev/null 2>&1; then
        openssl rand -hex 32
    elif command -v python3 >/dev/null 2>&1; then
        python3 -c 'import secrets; print(secrets.token_hex(32))'
    else
        fail "need openssl or python3 to generate an ephemeral HTTP access token"
    fi
}

pick_free_port() {
    if [ -n "${HTTP_PORT:-}" ]; then
        printf '%s\n' "$HTTP_PORT"
        return
    fi
    if command -v python3 >/dev/null 2>&1; then
        python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()'
    else
        printf '%s\n' "18765"
    fi
}

wait_for_port() {
    port="$1"
    pid="$2"
    log_file="$3"
    elapsed=0
    while [ "$elapsed" -lt $((STARTUP_TIMEOUT_SECONDS * 20)) ]; do
        if ! kill -0 "$pid" 2>/dev/null; then
            echo "bench/k6: HTTP bridge exited early:" >&2
            cat "$log_file" >&2 || true
            exit 1
        fi
        if command -v python3 >/dev/null 2>&1; then
            if python3 -c "import socket,sys; s=socket.create_connection(('127.0.0.1',$port),0.2); s.close()" 2>/dev/null; then
                return 0
            fi
        elif command -v nc >/dev/null 2>&1; then
            if nc -z 127.0.0.1 "$port" 2>/dev/null; then
                return 0
            fi
        else
            sleep 1
            return 0
        fi
        sleep 0.05
        elapsed=$((elapsed + 1))
    done
    echo "bench/k6: HTTP bridge did not listen on 127.0.0.1:$port within ${STARTUP_TIMEOUT_SECONDS}s:" >&2
    cat "$log_file" >&2 || true
    exit 1
}

TMP_DIR="$(mktemp -d /tmp/agentcy-k6-XXXXXX)"
chmod 700 "$TMP_DIR"
HOME_DIR="$TMP_DIR/home"
WORKSPACE_DIR="$TMP_DIR/workspace"
LOG_FILE="$TMP_DIR/http.log"
mkdir -p "$HOME_DIR" "$WORKSPACE_DIR"
chmod 700 "$HOME_DIR"

HTTP_PID=""

cleanup() {
    status=$?
    trap - EXIT INT TERM
    if [ -n "$HTTP_PID" ] && kill -0 "$HTTP_PID" 2>/dev/null; then
        kill -TERM "$HTTP_PID" 2>/dev/null || true
        wait_loops=0
        while kill -0 "$HTTP_PID" 2>/dev/null && [ "$wait_loops" -lt 50 ]; do
            sleep 0.1
            wait_loops=$((wait_loops + 1))
        done
        if kill -0 "$HTTP_PID" 2>/dev/null; then
            kill -KILL "$HTTP_PID" 2>/dev/null || true
        fi
        wait "$HTTP_PID" 2>/dev/null || true
    fi
    if [ -d "$HOME_DIR" ] && [ -f "$REPO_ROOT/scripts/stop-core.sh" ]; then
        sh "$REPO_ROOT/scripts/stop-core.sh" "$HOME_DIR" >/dev/null 2>&1 || true
    fi
    rm -rf "$TMP_DIR"
    exit "$status"
}

trap cleanup EXIT INT TERM

PORT="$(pick_free_port)"
TOKEN="$(generate_token)"

AGENTCY_HOME="$HOME_DIR" AGENTCY_HTTP_TOKEN="$TOKEN" \
    "$AGENTCY_BIN" --workspace "$WORKSPACE_DIR" --http "$PORT" >"$LOG_FILE" 2>&1 &
HTTP_PID=$!

wait_for_port "$PORT" "$HTTP_PID" "$LOG_FILE"

BASE_URL="http://127.0.0.1:$PORT/mcp" \
    AGENTCY_HTTP_TOKEN="$TOKEN" \
    K6_PROFILE="$K6_PROFILE" \
    K6_ERA="$K6_ERA" \
    K6_SUMMARY_EXPORT="$K6_SUMMARY_EXPORT" \
    k6 run "$@" "$SCRIPT_DIR/http_load.js"

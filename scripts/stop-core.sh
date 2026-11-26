#!/bin/sh
# Stop the agentcy core daemon of one data directory, if one runs. `make stop-core` and
# `make setup` run this.
#
# Usage: stop-core.sh DATA_DIR
#   DATA_DIR  the bridge data directory: $AGENTCY_HOME, or ~/.agentcy when it is unset
#
# The core stays alive while any conversation is open. After an upgrade, an old core
# (Python or Go) would keep serving the new shims. Stopping it makes the next tool call
# start the installed core. Conversations and join codes are kept in the stores.
#
# Every core, Python or Go, holds an flock on DATA_DIR/core.lock while it runs. This script
# finds the core only through that file. It sends SIGTERM to a process only when the process
# holds core.lock open, belongs to the current user, and runs an agentcy core command line.
# It never matches processes by name alone and never sends SIGKILL.
set -eu

fail() {
    echo "$1" >&2
    exit 1
}

if [ "$#" -ne 1 ] || [ -z "$1" ]; then
    fail "Usage: stop-core.sh DATA_DIR"
fi

data_dir="$1"
lock="$data_dir/core.lock"
# The longest wait for the core to exit. The Go core gives running requests 5 seconds.
stop_timeout_seconds=15

if [ ! -e "$lock" ]; then
    echo "No agentcy core runs for $data_dir."
    exit 0
fi

command -v lsof >/dev/null 2>&1 || fail "lsof is missing. Install lsof to find the running core."
command -v ps >/dev/null 2>&1 || fail "ps is missing. Install ps to check the running core."

# lsof exits with status 1 when no process has the file open.
holders="$(lsof -t -- "$lock" 2>/dev/null || true)"
if [ -z "$holders" ]; then
    echo "No agentcy core runs for $data_dir."
    exit 0
fi

user_id="$(id -u)"

# is_agentcy_core PID succeeds when the process belongs to this user and runs a core:
# `<path>/agentcy core` (the Go core) or `python ... -m agentcy.mcp.core` (the Python core).
is_agentcy_core() {
    owner="$(ps -o uid= -p "$1" 2>/dev/null | tr -d ' ')"
    test "$owner" = "$user_id" || return 1
    command_line="$(ps -ww -o command= -p "$1" 2>/dev/null)"
    case "$command_line" in
        */agentcy\ core | agentcy\ core | *\ -m\ agentcy.mcp.core) return 0 ;;
        *) return 1 ;;
    esac
}

for pid in $holders; do
    if ! kill -0 "$pid" 2>/dev/null; then
        continue
    fi
    is_agentcy_core "$pid" ||
        fail "Process $pid holds $lock but is not an agentcy core of this user. Nothing was stopped."
done

for pid in $holders; do
    if ! kill -0 "$pid" 2>/dev/null; then
        continue
    fi
    echo "Stopping the agentcy core (process $pid) for $data_dir"
    if ! kill -TERM "$pid" 2>/dev/null; then
        # Ignore ESRCH if the process exited between the check and kill.
        if kill -0 "$pid" 2>/dev/null; then
            fail "Could not send SIGTERM to agentcy core (process $pid)."
        fi
    fi
done

waited=0
for pid in $holders; do
    while kill -0 "$pid" 2>/dev/null; do
        test "$waited" -lt "$stop_timeout_seconds" ||
            fail "The agentcy core (process $pid) did not stop within $stop_timeout_seconds seconds."
        sleep 1
        waited=$((waited + 1))
    done
done
echo "Stopped the agentcy core for $data_dir."

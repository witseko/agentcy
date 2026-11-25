#!/bin/sh
# Download the terminal UI binary for one platform from a GitHub release, check its SHA-256
# against the release's SHA256SUMS file, and install it. `make tui` runs this when Go is missing.
#
# Usage: install-tui-release.sh RELEASE_URL PLATFORM DESTINATION
#   RELEASE_URL  https://github.com/<owner>/<repo>/releases/download/v<version>
#   PLATFORM     <os>-<arch>, for example darwin-arm64
#   DESTINATION  the path of the installed binary
#
# The checksum comes from the same release as the binary. It detects a corrupted or truncated
# download, not a compromised release.
set -eu

release_url="$1"
platform="$2"
destination="$3"
asset="agentcy-tui-$platform"

fail() {
    echo "$1" >&2
    exit 1
}

case "$release_url" in
    https://*) ;;
    *) fail "The release URL must start with https://: $release_url" ;;
esac

command -v curl >/dev/null 2>&1 || fail "curl is missing. Install curl, or install Go: https://go.dev/dl/"
if command -v sha256sum >/dev/null 2>&1; then
    sha256_tool="sha256sum"
elif command -v shasum >/dev/null 2>&1; then
    sha256_tool="shasum -a 256"
else
    fail "Neither sha256sum nor shasum is installed. Install one of them, or install Go: https://go.dev/dl/"
fi

download_dir="$(mktemp -d)"
trap 'rm -rf -- "$download_dir"' EXIT
trap 'exit 1' HUP INT TERM

download() {
    curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 \
        --output "$download_dir/$1" "$release_url/$1"
}

download SHA256SUMS ||
    fail "Could not download $release_url/SHA256SUMS. This version may have no release yet. Install Go: https://go.dev/dl/"

expected="$(awk -v name="$asset" '$2 == name || $2 == "*" name { print $1 }' "$download_dir/SHA256SUMS")"
test -n "$expected" || fail "The release has no terminal UI for $platform. Install Go: https://go.dev/dl/"

download "$asset" || fail "Could not download $release_url/$asset."

actual="$($sha256_tool "$download_dir/$asset" | awk '{ print $1 }')"
test "$actual" = "$expected" || fail "The SHA-256 of $asset does not match SHA256SUMS. Nothing was installed."

install -m 755 "$download_dir/$asset" "$destination"

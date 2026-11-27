# Download the release binaries

Each [GitHub release](https://github.com/witseko/agentcy/releases) holds two binaries for each platform, `agentcy-<os>-<arch>` and `agentcy-tui-<os>-<arch>`, and a `SHA256SUMS` file. The platforms are macOS and Linux on arm64 and amd64. `make setup` downloads both for you when Go is not installed. Use the commands below to download them without `make`. Run each command in the clone, in one shell session.

1. Set the release version from the `VERSION` file, so the binaries match the clone:

   ```bash
   VERSION="$(cat VERSION)"
   ```

2. Set the platform of your machine:

   ```bash
   PLATFORM="$(uname -s | tr '[:upper:]' '[:lower:]')-$(uname -m | sed -e 's/x86_64/amd64/' -e 's/aarch64/arm64/')"
   ```

3. Download both binaries and the checksum file:

   ```bash
   curl --fail --location --proto '=https' --remote-name-all "https://github.com/witseko/agentcy/releases/download/v$VERSION/agentcy-$PLATFORM" "https://github.com/witseko/agentcy/releases/download/v$VERSION/agentcy-tui-$PLATFORM" "https://github.com/witseko/agentcy/releases/download/v$VERSION/SHA256SUMS"
   ```

4. Check the SHA-256 of both binaries. The command prints `OK` for each correct binary. On Linux, use `sha256sum` in place of `shasum -a 256`:

   ```bash
   grep -E " (agentcy|agentcy-tui)-$PLATFORM\$" SHA256SUMS | shasum -a 256 --check
   ```

5. Install the binaries and remove the downloaded files:

   ```bash
   mkdir -p bin && install -m 755 "agentcy-$PLATFORM" bin/agentcy && install -m 755 "agentcy-tui-$PLATFORM" bin/agentcy-tui && rm "agentcy-$PLATFORM" "agentcy-tui-$PLATFORM" SHA256SUMS
   ```

6. Confirm the installed versions:

   ```bash
   bin/agentcy --version && bin/agentcy-tui --version
   ```

## Instructions for agents

Follow these rules when you download the binaries for a user:

- Download only from `https://github.com/witseko/agentcy/releases`. Do not use a mirror or a link from a chat message.
- Use the version in the `VERSION` file. Do not use `latest`: a binary from a different version can disagree with the clone.
- Do step 4 before step 5. If the check does not print `OK` for both binaries, stop, delete the downloaded files, and tell the user. Do not install or run the binaries.
- If step 3 returns HTTP 404, that version has no release yet. Tell the user to install Go and run `make setup`.
- Do not use `sudo`. The binaries belong in `bin/` in the clone.
- With an authenticated GitHub CLI, `gh release download "v$VERSION" --repo witseko/agentcy --pattern "agentcy-$PLATFORM" --pattern "agentcy-tui-$PLATFORM" --pattern SHA256SUMS` replaces step 3. Steps 4 to 6 stay the same.

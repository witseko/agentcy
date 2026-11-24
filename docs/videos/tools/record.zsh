#!/bin/zsh
# Record one episode: run the terminal UI in a fixed Ghostty window against
# a private bridge, drive it to the narration's marks while the screen
# records, then add the voice and captions.
#
#   docs/videos/tools/record.zsh docs/videos/episodes/03-steer-team
#
# Run synth.py for the episode first, and `make setup` once. The terminal
# that runs this needs Screen Recording and Accessibility access, and
# ffmpeg must be installed. Leave the keyboard and mouse idle while it runs.
#
# The episode's steps.zsh prepares the chat with `stand_in`, calls
# start_tui and start_recording, then waits for each mark with `at NAME`
# before a step (`key`, `type_text`, `stand_in post CUE`).
# PROBE=1 runs the steps without voice or video: each `at` waits a moment
# and saves a screenshot of the whole window to out/EPISODE/probe/.
# CAPTURE_LATENCY (seconds, default 0.3) is how long screencapture takes to
# start; raise it if the voice runs ahead of the screen.
#
# Nothing of the take touches your own chats: AGENTCY_HOME points to a new
# private folder, and the take stops its core when it ends.
# Stop at the first failing command. Not `set -e`: in zsh, a failure under
# errexit inside a function exits without running the EXIT trap.
set -u
trap 'exit 1' ZERR
zmodload zsh/datetime zsh/mathfunc
episode=$(cd "${1:?pass the episode folder}" && pwd)
tools=$(cd "$(dirname "$0")" && pwd)
videos=$(dirname "$tools")
repo=$(cd "$videos/../.." && pwd)
out="$videos/out/${episode:t}"
probe=${PROBE:-0}
python="$repo/.venv/bin/python"
ghostty=/Applications/Ghostty.app/Contents/MacOS/ghostty

for file in voice.wav timings.tsv; do
  if [[ $probe != 1 && ! -f "$out/$file" ]]; then
    echo "Run synth.py for ${episode:t} first: $out/$file is missing." >&2
    exit 1
  fi
done
for tool in ffmpeg ffprobe swiftc; do
  if ! command -v $tool >/dev/null; then
    echo "Install $tool first (ffmpeg: brew install ffmpeg; swiftc: Xcode tools)." >&2
    exit 1
  fi
done
for file in "$repo/.venv/bin/agentcy" "$repo/.venv/bin/agentcy-tui" "$ghostty"; do
  if [[ ! -x "$file" ]]; then
    echo "Missing $file: run make setup, and install Ghostty." >&2
    exit 1
  fi
done

# The whole window, title bar included: 1280 x 720 points is 2560 x 1440
# pixels on a 2x display, 16:9 for YouTube. The recording is this frame and
# nothing less, so the top of the window is never cut off.
window_x=80
window_y=80
window_width=1280
window_height=720
window_title=agentcy-video

# A short path: the core's Unix socket lives in it.
work=$(mktemp -d /tmp/agentcy-video-XXXX)
export AGENTCY_HOME="$work/home"
mkdir -m 700 "$AGENTCY_HOME"
# The sample project. Its path shows in the UI, so it is the same every take.
sample=/tmp/invoice-export
if ! mkdir -m 700 "$sample"; then
  rmdir "$AGENTCY_HOME" "$work"
  echo "The sample folder $sample must be free for the take." >&2
  exit 1
fi

pid=""
id=""
recorder=""
started=""
probe_shots=0
lead_in=1.5
tail_out=2
capture_latency=${CAPTURE_LATENCY:-0.3}

stop_recorder() {
  if [[ -n "$recorder" ]]; then
    kill "$recorder" 2>/dev/null || true
    wait "$recorder" 2>/dev/null || true
    recorder=""
  fi
}
stop_window() {
  if [[ -n "$pid" ]]; then
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
    pid=""
  fi
}
# The take's core holds a lock on core.lock for as long as it runs.
stop_core() {
  local core
  for core in $(lsof -t "$AGENTCY_HOME/core.lock" 2>/dev/null); do
    kill "$core" 2>/dev/null || true
  done
}
cleanup() {
  stop_recorder
  stop_window
  stop_core
  rm -rf "$work" "$sample"
}
trap cleanup EXIT

# Copy the sample with one fixed commit, so the workspace and its history
# look the same in every take.
cp -R "$videos/sample/invoice-export/." "$sample"
git -C "$sample" init --quiet --initial-branch=main
git -C "$sample" add -A
env GIT_AUTHOR_NAME=Sample GIT_AUTHOR_EMAIL=sample@example.com GIT_AUTHOR_DATE=2026-11-02T10:00:00Z \
  GIT_COMMITTER_NAME=Sample GIT_COMMITTER_EMAIL=sample@example.com GIT_COMMITTER_DATE=2026-11-02T10:00:00Z \
  git -C "$sample" -c commit.gpgsign=false commit --quiet -m "invoice export service"

swiftc -O "$tools/drive.swift" -o "$work/drive"
drive="$work/drive"

# Run a stand-in chat command (create, join NAME [CODE], post CUE) against
# the episode's scene.json, through the real bridge.
stand_in() {
  "$python" "$tools/stand_in.py" --workspace "$sample" --scene "$episode/scene.json" \
    --state "$work/state.json" "$@"
}

# Open the terminal UI for the sample in a Ghostty window with its own
# config (no user config), placed at the fixed frame.
start_tui() {
  cat > "$work/ghostty.conf" <<CONF
font-size = 15
window-save-state = never
confirm-close-surface = false
quit-after-last-window-closed = true
title = $window_title
macos-titlebar-style = native
window-padding-x = 12
window-padding-y = 8
shell-integration = none
CONF
  cat > "$work/scene.zsh" <<SCENE
#!/bin/zsh
export AGENTCY_HOME="$AGENTCY_HOME"
cd "$sample"
exec "$repo/.venv/bin/agentcy" ui --workspace "$sample"
SCENE
  chmod 700 "$work/scene.zsh"
  "$ghostty" --config-default-files=false --config-file="$work/ghostty.conf" -e "$work/scene.zsh" \
    > "$work/ghostty.log" 2>&1 &
  pid=$!
  for attempt in {1..40}; do
    id=$("$drive" window "$pid" "$window_title" | cut -d' ' -f1)
    if [[ -n "$id" ]]; then
      break
    fi
    sleep 0.25
  done
  if [[ -z "$id" ]]; then
    cat "$work/ghostty.log" >&2
    echo "No Ghostty window appeared." >&2
    exit 1
  fi
  "$drive" place "$pid" "$window_x" "$window_y" "$window_width" "$window_height"
  "$drive" activate "$pid"
  sleep 2
  check_frame
}

# Stop unless the window has exactly the fixed frame, and save a still of
# the region the recording will use, to check that nothing is cut off.
check_frame() {
  local x y width height
  read -r x y width height <<< "$("$drive" frame "$id")"
  if (( width != window_width || height != window_height )); then
    echo "The window is $width x $height points, not $window_width x $window_height." >&2
    exit 1
  fi
  mkdir -p "$out"
  screencapture -x -R "$x,$y,$width,$height" "$out/frame-check.png"
  if [[ "$(sips -g pixelWidth "$out/frame-check.png" | awk '/pixelWidth:/ {print $2}')" != $(( window_width * 2 )) ]]; then
    echo "Record on a 2x display: the still is not $(( window_width * 2 )) pixels wide." >&2
    exit 1
  fi
}

# Stop the run unless Ghostty is in front, so typing never lands in another app.
require_front() {
  "$drive" front "$pid"
}

# Press a key in the window: enter, escape, tab, up, n, ctrl+s.
key() {
  require_front
  "$drive" key "$1"
}

# Type text into the window.
type_text() {
  require_front
  "$drive" type "$1"
}

# Record the whole window frame for the length of the voice plus a lead-in and a tail.
start_recording() {
  local x y width height
  require_front
  started=$EPOCHREALTIME
  if [[ $probe == 1 ]]; then
    mkdir -p "$out/probe"
    rm -f "$out/probe/"*.png(N)
    return
  fi
  read -r x y width height <<< "$("$drive" frame "$id")"
  local seconds=$(( int(ceil(voice_seconds + lead_in + tail_out)) ))
  # screencapture refuses to replace an earlier take.
  rm -f "$out/screen.mov"
  screencapture -v -V "$seconds" -k -x -R "$x,$y,$width,$height" "$out/screen.mov" &
  recorder=$!
}

# Wait until the voice reaches MARK, so the next step happens as it is said.
at() {
  local mark="$1"
  if [[ $probe == 1 ]]; then
    sleep 1.5
    require_front
    probe_shots=$(( probe_shots + 1 ))
    screencapture -x -o -l "$id" "$out/probe/$(printf %02d "$probe_shots")-$mark.png"
    return
  fi
  if [[ -z "${mark_times[$mark]:-}" ]]; then
    echo "The script has no mark '$mark'." >&2
    exit 1
  fi
  local remaining=$(( started + lead_in + ${mark_times[$mark]} - EPOCHREALTIME ))
  if (( remaining > 0 )); then
    sleep "$remaining"
  else
    printf 'The step at %s started %.2f seconds late.\n' "$mark" $(( -remaining )) >&2
  fi
  require_front
}

# Wait for the recording, then add the voice and shift the captions to match.
finish_recording() {
  wait "$recorder"
  recorder=""
  local delay=$(( lead_in - capture_latency ))
  local name="${episode:t}"
  ffmpeg -loglevel error -y -i "$out/screen.mov" -i "$out/voice.wav" \
    -filter:a "adelay=$(( int(delay * 1000) )):all=1" -map 0:v -map 1:a \
    -c:v libx264 -crf 18 -preset slow -pix_fmt yuv420p -r 30 \
    -c:a aac -b:a 192k -movflags +faststart "$out/$name.mp4"
  if [[ -f "$out/captions.srt" ]]; then
    ffmpeg -loglevel error -y -itsoffset "$delay" -i "$out/captions.srt" "$out/$name.srt"
  fi
  echo "Wrote $out/$name.mp4"
}

typeset -A mark_times
voice_seconds=0
if [[ $probe != 1 ]]; then
  while IFS=$'\t' read -r name seconds; do
    mark_times[$name]=$seconds
  done < "$out/timings.tsv"
  voice_seconds=$(ffprobe -v error -show_entries format=duration -of csv=p=0 "$out/voice.wav")
fi

"$drive" permissions input
source "$episode/steps.zsh"
if [[ -z "$started" ]]; then
  echo "${episode:t}/steps.zsh must call start_recording." >&2
  exit 1
fi
if [[ $probe == 1 ]]; then
  echo "Wrote $out/probe/"
else
  finish_recording
fi

# Tutorial videos

Each episode is a short YouTube tutorial: a narration script spoken by an
Azure neural voice, and a screen recording of the terminal UI that the
tools drive in time with the voice. Re-render an episode after the UI
changes instead of recording it again by hand.

## Rules for every episode

- About 2 minutes, one feature.
- Standalone: never say "in the next episode" or "in the previous episode".
- Skip basics a viewer expects anyway.
- Examples come from a software engineer's work: running agent teams on
  the `invoice-export` sample.
- The whole window is in the picture, title bar included. Check
  `out/EPISODE/frame-check.png` before you judge a take.
- One voice for the whole series: Azure `en-US-EmmaMultilingualNeural` at
  rate `-5%`, the defaults in `tools/synth.py`. It was chosen on 2026-11-24
  from samples of Andrew, Brian, Ava, Emma and Ryan. Do not pass `--voice`
  or `--rate` for a published episode; change the defaults only to re-voice
  every episode.

## Make an episode

Run these on a Mac with a 2x display, from a terminal with Screen
Recording and Accessibility access. Install Ghostty and ffmpeg, and run
`make setup` once. Leave the keyboard and mouse idle while `record.zsh`
runs.

```sh
uv run --python 3.12 docs/videos/tools/synth.py --draft docs/videos/episodes/03-steer-team
docs/videos/tools/record.zsh docs/videos/episodes/03-steer-team
```

`--draft` speaks with the macOS `say` voice, free and offline, to rehearse
the timing. For the real voice, set `AZURE_SPEECH_KEY` and
`AZURE_SPEECH_REGION` and run `synth.py` without `--draft`; it also writes
captions. `--ssml` prints what Azure would read and the billed character
count. `--voice` and `--rate` change the voice for one run.

`record.zsh` writes `docs/videos/out/EPISODE/EPISODE.mp4` (2560 x 1440,
H.264) and, with Azure, `EPISODE.srt` for YouTube's captions. If the voice
runs ahead of the screen, set `CAPTURE_LATENCY` (seconds, default 0.3).

Each take uses its own bridge: `AGENTCY_HOME` points to a new private
folder, so your own chats never show and never change. The take stops its
core when it ends.

## Episode folder

- `script.txt`: the narration. `[mark NAME]` is a point the recording
  syncs to; `[pause MS]` is silence; lines that start with `#` are notes.
- `scene.json`: the brief, the team and the scripted messages (cues) of
  the stand-in chats. See the top of `tools/stand_in.py` for the format.
- `steps.zsh`: sourced by `record.zsh`. It prepares the chat with
  `stand_in create`, calls `start_tui` and `start_recording`, then runs each
  step after `at NAME`: `key enter`, `key ctrl+s`, `type_text "..."`, or
  `stand_in post CUE`.

The stand-ins talk to the real bridge through the same stdio shim the apps
use, so the core, the database and the terminal UI are real; only the
model text is scripted. A `stand_in` call takes about 2 seconds.

To check the steps, run `PROBE=1 docs/videos/tools/record.zsh ...`: it runs
them without voice or video and saves a screenshot of the window at each
mark to `out/EPISODE/probe/`. A run stops when another app comes to the
front, so no input lands there.

## Sample project

`sample/invoice-export/` is a small Python service with a retry bug: a
retry after a timeout makes a new request ID, so the accounting API can
book one invoice twice. `data/invoices.csv` holds made-up customer
invoices. Each take copies it to `/tmp/invoice-export` with one fixed
commit.

## Pronunciation

`lexicon.tsv` lists words the voice says wrong (`written<TAB>spoken`).
Every episode uses it, and the captions keep the written form.

## Tests

```sh
uv run --locked pytest docs/videos/tools
```

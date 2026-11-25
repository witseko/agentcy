# Episode 08: Two desktop agent apps, one project

This episode shows real model replies, so it is not driven by `steps.zsh`.
It is recorded live once, then cut to the narration.

1. Layout: Claude Code (Claude desktop app, Code tab) on the left half of
   the screen, ZCode on the right half, both on the agentcy repo.
2. Record the screen with ffmpeg (stop it with SIGINT; `screencapture -v`
   discards the file when interrupted):
   `ffmpeg -f avfoundation -capture_cursor 1 -framerate 30 -i "Capture screen 0:none" -vf "crop=2880:1740:0:60" -c:v libx264 -crf 18 -pix_fmt yuv420p take.mp4`
3. ZCode drafts a two-sentence brief. The terminal UI (Ghostty, centered,
   real bridge) creates the chat with Lead and Reviewer and copies each join
   prompt. The user pastes the lead prompt into Claude Code and approves
   the agentcy tools (Claude may not drive the Claude app); Reviewer's
   prompt goes into a new ZCode task.
4. Render `script.txt` with `synth.py`, then run `cut.py`. Each `[mark]`
   starts one segment of footage; each segment is sped up or slowed down to
   fill its line exactly. Put `take.mp4` and `tui-clip.mp4` in
   `docs/videos/out/08-two-desktop-apps/` first.

Source segments of the 2026-11-25 take (seconds in `take.mp4`):

| Mark | Source | Shows |
| --- | --- | --- |
| ask | 8 to 45 | brief request typed in ZCode, ZCode working |
| brief | 45 to 60 | ZCode's brief |
| tui | 75 to 102 | New chat form, chat created, lead prompt copied |
| lead | 115 to 135 | lead prompt pasted into Claude Code, tools load |
| reviewer | 135 to 165 | Reviewer prompt into ZCode, lead searching |
| answer | 165 to 200 | lead posts the rule, Reviewer joins and checks it |
| chat | `tui-clip.mp4` 3 to 26 | the chat in the terminal UI: transcript, Tasks tab |
| recap | `tui-clip.mp4` 26 to 34 | the chat in the terminal UI |

`tui-clip.mp4` is a second recording made after the take: the terminal UI
in a 1280 x 720 Ghostty window opens the chat, scrolls to Reviewer's
correction, and shows the Tasks tab. Close and archive the chat afterwards.

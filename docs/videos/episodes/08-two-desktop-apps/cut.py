"""Cut episode 08's live recordings to its narration marks and add the voice and captions.

    python3 docs/videos/episodes/08-two-desktop-apps/cut.py

Reads docs/videos/out/08-two-desktop-apps/: take.mp4 (the live take),
tui-clip.mp4 (the terminal UI clip), voice.wav, timings.tsv and captions.srt
from synth.py. Each mark's segment of footage is sped up or slowed down to
fill its narration line exactly, then padded to 2560 x 1440 without cropping.
Writes 08-two-desktop-apps.mp4 and 08-two-desktop-apps.srt.
"""

import shutil
import subprocess
from pathlib import Path

OUT = Path(__file__).resolve().parents[2] / "out" / "08-two-desktop-apps"
LEAD_IN = 1.0
TAIL = 2.0
# Mark: (input, start, end) seconds. Input 0 is take.mp4, input 1 is tui-clip.mp4.
SOURCE = {
    "ask": (0, 8, 45),
    "brief": (0, 45, 60),
    "tui": (0, 75, 102),
    "lead": (0, 115, 135),
    "reviewer": (0, 135, 165),
    "answer": (0, 165, 200),
    "chat": (1, 3, 26),
    "recap": (1, 26, 34),
}
FFMPEG = shutil.which("ffmpeg") or "/opt/homebrew/bin/ffmpeg"
FFPROBE = shutil.which("ffprobe") or "/opt/homebrew/bin/ffprobe"
FIT = "scale=-2:1440,pad=2560:1440:(ow-iw)/2:0:color=0x1e1f22,setsar=1"


def duration(path: Path) -> float:
    """Return a media file's length in seconds."""
    # A fixed argument list with a resolved tool path; no shell.
    output = subprocess.check_output(  # noqa: S603
        [FFPROBE, "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", str(path)]
    )
    return float(output)


def main() -> None:
    """Build the filter graph from the marks and render the episode."""
    marks = [line.split("\t") for line in (OUT / "timings.tsv").read_text().splitlines() if line]
    names = [name for name, _ in marks]
    times = {name: float(seconds) for name, seconds in marks}
    voice = duration(OUT / "voice.wav")
    filters = []
    for index, name in enumerate(names):
        start = LEAD_IN + times[name] if index else 0.0
        end = LEAD_IN + times[names[index + 1]] if index + 1 < len(names) else LEAD_IN + voice + TAIL
        source, first, last = SOURCE[name]
        factor = (end - start) / (last - first)
        filters.append(f"[{source}:v]trim={first}:{last},setpts=(PTS-STARTPTS)*{factor:.4f},fps=30,{FIT}[v{index}]")
        print(f"{name:9s} {last - first:4.0f}s -> {end - start:5.2f}s  speed x{1 / factor:.2f}")
    filters.append("".join(f"[v{index}]" for index in range(len(names))) + f"concat=n={len(names)}:v=1:a=0[v]")
    filters.append(f"[2:a]adelay={int(LEAD_IN * 1000)}:all=1,apad=pad_dur={TAIL}[a]")
    # Fixed argument lists with a resolved tool path; no shell.
    subprocess.run(  # noqa: S603
        [FFMPEG, "-loglevel", "error", "-y", "-i", str(OUT / "take.mp4"), "-i", str(OUT / "tui-clip.mp4"),
         "-i", str(OUT / "voice.wav"), "-filter_complex", ";".join(filters), "-map", "[v]", "-map", "[a]",
         "-c:v", "libx264", "-crf", "18", "-preset", "slow", "-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "192k",
         "-shortest", "-movflags", "+faststart", str(OUT / "08-two-desktop-apps.mp4")],
        check=True,
    )
    subprocess.run(  # noqa: S603
        [FFMPEG, "-loglevel", "error", "-y", "-itsoffset", str(LEAD_IN), "-i", str(OUT / "captions.srt"),
         str(OUT / "08-two-desktop-apps.srt")],
        check=True,
    )


if __name__ == "__main__":
    main()

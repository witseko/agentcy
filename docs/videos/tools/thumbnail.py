# /// script
# requires-python = ">=3.11"
# dependencies = ["pillow==11.3.0"]
# ///
"""Draw a 1280 x 720 YouTube thumbnail from a frame of an episode.

    uv run docs/videos/tools/thumbnail.py --video EPISODE.mp4 --at 40 \
        --product Agentcy --accent 8ab4f8 --number 03 \
        --title "Steer a running team" --out 03.png

The frame is darkened and blurred a little, with a darker band on the left
for the text: the product name in its accent color, the episode number and
the short title. Every thumbnail of a series uses the same layout, so a
playlist reads as one set.
"""

import argparse
import shutil
import subprocess
import tempfile
from pathlib import Path

from PIL import Image, ImageDraw, ImageFilter, ImageFont

WIDTH, HEIGHT = 1280, 720
MARGIN = 72
TEXT_WIDTH = 700
FONT = "/System/Library/Fonts/SFNS.ttf"
FFMPEG = shutil.which("ffmpeg") or "/opt/homebrew/bin/ffmpeg"


def font(size: int, weight: str = "Bold") -> ImageFont.FreeTypeFont:
    """Return San Francisco at ``size`` with the named variable-font weight."""
    face = ImageFont.truetype(FONT, size)
    face.set_variation_by_name(weight)
    return face


def frame(video: Path, at: float) -> Image.Image:
    """Grab the frame at ``at`` seconds and fit it to cover 1280 x 720."""
    with tempfile.TemporaryDirectory() as folder:
        still = Path(folder) / "frame.png"
        # A fixed argument list with a resolved tool path; no shell.
        subprocess.run(  # noqa: S603
            [FFMPEG, "-loglevel", "error", "-y", "-ss", str(at), "-i", str(video), "-frames:v", "1", str(still)],
            check=True,
        )
        image = Image.open(still).convert("RGB")
    scale = max(WIDTH / image.width, HEIGHT / image.height)
    image = image.resize((round(image.width * scale), round(image.height * scale)), Image.LANCZOS)
    left, top = (image.width - WIDTH) // 2, (image.height - HEIGHT) // 2
    return image.crop((left, top, left + WIDTH, top + HEIGHT))


def wrap(draw: ImageDraw.ImageDraw, text: str, face: ImageFont.FreeTypeFont, width: int) -> list[str]:
    """Break ``text`` into lines no wider than ``width`` pixels."""
    lines, line = [], ""
    for word in text.split():
        candidate = f"{line} {word}".strip()
        if line and draw.textlength(candidate, font=face) > width:
            lines.append(line)
            line = word
        else:
            line = candidate
    return [*lines, line]


def accent_color(hex_value: str) -> tuple[int, int, int]:
    """Turn ``rrggbb`` into an RGB tuple."""
    return tuple(int(hex_value[index:index + 2], 16) for index in (0, 2, 4))


def draw_thumbnail(arguments: argparse.Namespace) -> Image.Image:
    """Compose the background, the left band and the text."""
    accent = accent_color(arguments.accent)
    background = frame(arguments.video, arguments.at).filter(ImageFilter.GaussianBlur(2))
    background = Image.blend(background, Image.new("RGB", (WIDTH, HEIGHT), (10, 12, 16)), 0.55)
    band = Image.new("L", (WIDTH, HEIGHT), 0)
    band_draw = ImageDraw.Draw(band)
    for x in range(WIDTH):
        band_draw.line([(x, 0), (x, HEIGHT)], fill=max(0, 235 - int(x * 235 / 880)))
    image = Image.composite(Image.new("RGB", (WIDTH, HEIGHT), (10, 12, 16)), background, band)
    draw = ImageDraw.Draw(image)

    draw.rectangle([0, 0, 12, HEIGHT], fill=accent)
    draw.text((MARGIN, 64), arguments.product, font=font(46), fill=accent)
    draw.text((MARGIN, 128), arguments.number, font=font(150, "Heavy"), fill=(255, 255, 255))
    title_face = font(78)
    lines = wrap(draw, arguments.title, title_face, TEXT_WIDTH)[:3]
    for index, line in enumerate(lines):
        draw.text((MARGIN, 330 + index * 92), line, font=title_face, fill=(255, 255, 255))
    draw.text((MARGIN, HEIGHT - 86), arguments.footer, font=font(32, "Medium"), fill=(170, 176, 186))
    return image


def main() -> None:
    """Parse the options and write the thumbnail as PNG."""
    parser = argparse.ArgumentParser(description="Draw a YouTube thumbnail from an episode frame.")
    parser.add_argument("--video", type=Path, required=True)
    parser.add_argument("--at", type=float, required=True, help="frame time in seconds")
    parser.add_argument("--product", required=True)
    parser.add_argument("--accent", required=True, help="accent color as rrggbb")
    parser.add_argument("--number", required=True)
    parser.add_argument("--title", required=True)
    parser.add_argument("--footer", default="Tutorial")
    parser.add_argument("--out", type=Path, required=True)
    arguments = parser.parse_args()
    draw_thumbnail(arguments).save(arguments.out, optimize=True)


if __name__ == "__main__":
    main()

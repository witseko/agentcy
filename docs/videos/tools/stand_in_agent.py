"""Show fixed coding-agent screens for a video take: a stand-in for a Claude Code session.

    stand_in_agent.py SCREENS_DIR CONTROL_FIFO

Prints SCREENS_DIR/01*.txt, waits for one typed line (the prompt the take
types), then prints the next screen. After that, each line written to
CONTROL_FIFO clears the pane and prints the next screen, so the steps
decide when the agent "answers". It runs no model.
"""

import sys
from pathlib import Path

CLEAR = "\x1b[2J\x1b[H"


def show(screen: Path) -> None:
    """Clear the pane and print one screen."""
    sys.stdout.write(CLEAR + screen.read_text(encoding="utf-8"))
    sys.stdout.flush()


def main() -> None:
    """Show the screens in name order, advancing on the typed prompt and then on the control pipe."""
    screens_dir, control = Path(sys.argv[1]), Path(sys.argv[2])
    screens = sorted(screens_dir.glob("*.txt"))
    show(screens[0])
    sys.stdin.readline()
    for screen in screens[1:]:
        show(screen)
        if screen is not screens[-1]:
            with control.open() as pipe:
                pipe.readline()
    # Keep the last screen up until the take stops the window.
    sys.stdin.read()


if __name__ == "__main__":
    main()

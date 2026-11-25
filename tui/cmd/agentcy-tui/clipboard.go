package main

import (
	"os"
	"os/exec"
	"runtime"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// systemClipboard returns a function that copies text with the desktop clipboard tool, or nil
// when the terminal clipboard (OSC 52) is the better choice: over SSH the desktop clipboard
// belongs to the remote machine, and without a desktop session there is no clipboard tool.
func systemClipboard() func(string) error {
	if os.Getenv("SSH_CONNECTION") != "" || os.Getenv("SSH_TTY") != "" {
		return nil
	}
	if runtime.GOOS == "darwin" {
		return pipeTo("/usr/bin/pbcopy")
	}
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		if path, err := exec.LookPath("wl-copy"); err == nil {
			return pipeTo(path)
		}
	}
	if os.Getenv("DISPLAY") != "" {
		if path, err := exec.LookPath("xclip"); err == nil {
			return pipeTo(path, "-selection", "clipboard")
		}
		if path, err := exec.LookPath("xsel"); err == nil {
			return pipeTo(path, "--clipboard", "--input")
		}
	}
	return nil
}

// pipeTo returns a function that runs a clipboard tool without a shell and writes text to its input.
func pipeTo(path string, args ...string) func(string) error {
	return func(text string) error {
		command := exec.Command(path, args...)
		command.Stdin = strings.NewReader(text)
		return command.Run()
	}
}

// copyCmd copies text and reports the outcome through result. Without a desktop clipboard tool
// it asks the terminal to copy with OSC 52; the terminal does not confirm, so result gets nil.
func (m model) copyCmd(text string, result func(error) tea.Msg) tea.Cmd {
	if m.clipboard == nil {
		return tea.Batch(tea.SetClipboard(text), func() tea.Msg { return result(nil) })
	}
	copyFn := m.clipboard
	return func() tea.Msg { return result(copyFn(text)) }
}

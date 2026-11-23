package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// screenText returns the drawn screen without colors.
func screenText(m model) string {
	return ansi.Strip(m.View().Content)
}

func TestErrorPopupStaysUntilDismissed(t *testing.T) {
	m := sized(newModel(&fakeOperator{}, "/work"), 120, 30)
	m = m.fail(errors.New("the core refused the request"))

	text := screenText(m)
	if !strings.Contains(text, "Error") || !strings.Contains(text, "the core refused the request") {
		t.Fatalf("error popup not drawn:\n%s", text)
	}
	for range statusDurationTicks * 2 {
		next, _ := m.Update(tickMsg(time.Now()))
		m = next.(model)
	}
	if m.popup.kind != popupError {
		t.Fatalf("error popup closed by itself: %+v", m.popup)
	}
	m, _ = press(t, m, "esc")
	if m.popup.kind != popupNone || strings.Contains(screenText(m), "the core refused") {
		t.Fatalf("esc did not dismiss the error popup: %+v", m.popup)
	}
}

func TestErrorPopupDismissedByClickWithoutClickingBelow(t *testing.T) {
	fake := &fakeOperator{list: ConversationList{Conversations: []ConversationSummary{
		{Conversation: "first", Title: "First"}, {Conversation: testConversation, Title: "Second"},
	}}}
	m := sized(newModel(fake, "/work"), 120, 30)
	m = drive(t, m, m.loadList())
	m = m.showError("Something went wrong.")

	m, command := click(t, m, 5, 4)
	m = drive(t, m, command)
	if m.popup.kind != popupNone || m.screen != screenHome {
		t.Fatalf("click under the error reached the list: popup %+v screen %v", m.popup, m.screen)
	}
}

func TestSuccessPopupClosesByItselfAndLetsKeysThrough(t *testing.T) {
	m := sized(newModel(&fakeOperator{}, "/work"), 120, 30)
	m = m.showSuccess("Chat closed.")
	if !strings.Contains(screenText(m), "Chat closed.") {
		t.Fatalf("success popup not drawn:\n%s", screenText(m))
	}
	for range popupSuccessTicks {
		next, _ := m.Update(tickMsg(time.Now()))
		m = next.(model)
	}
	if m.popup.kind != popupNone {
		t.Fatalf("success popup still open after %d ticks: %+v", popupSuccessTicks, m.popup)
	}

	m = m.showSuccess("Copied.")
	m, _ = press(t, m, "n")
	if m.popup.kind != popupNone || m.screen != screenNew {
		t.Fatalf("key under a success popup was lost: popup %+v screen %v", m.popup, m.screen)
	}
}

func TestCloseConfirmationShowsAsDialog(t *testing.T) {
	fake := &fakeOperator{}
	m := sized(chatModel(t, fake), 120, 30)

	m, _ = press(t, m, "ctrl+x")
	text := screenText(m)
	if !strings.Contains(text, "Close this chat?") || !strings.Contains(text, "Summary") ||
		!strings.Contains(text, "confirm") {
		t.Fatalf("close dialog not drawn:\n%s", text)
	}
	m, command := press(t, m, "enter")
	m = drive(t, m, command)
	if len(fake.closes) != 1 || m.popup.kind != popupSuccess || m.popup.text != "Chat closed." {
		t.Fatalf("closes %+v popup %+v", fake.closes, m.popup)
	}
}

func TestPopupFitsANarrowScreen(t *testing.T) {
	m := sized(newModel(&fakeOperator{}, "/work"), 40, 12)
	m = m.showError(strings.Repeat("long error text ", 10))
	next, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: 12})
	m = next.(model)
	for index, line := range strings.Split(m.View().Content, "\n") {
		if width := ansi.StringWidth(line); width > 40 {
			t.Fatalf("line %d is %d columns wide", index, width)
		}
	}
}

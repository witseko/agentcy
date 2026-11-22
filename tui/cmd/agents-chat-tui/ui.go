package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// screen names the view that receives keys.
type screen int

const (
	// screenHome shows the chat list with no conversation open.
	screenHome screen = iota
	screenNew
	screenChat
	// screenSearch shows the search box and its results in the center.
	screenSearch
)

const (
	sidebarWidth = 30
	teamWidth    = 32
	// Dragging a divider keeps each side column within these widths and the center wide
	// enough to read.
	minSideWidth   = 18
	maxSideWidth   = 60
	minCenterWidth = 40
	// Below these widths the side columns hide so the conversation keeps its room.
	sidebarMinWidth = 72
	teamMinWidth    = 104
)

const (
	// listRefreshTicks spaces list refreshes. Any request also keeps an idle core alive.
	listRefreshTicks = 5
	pollInterval     = time.Second
	offlineStatus    = "Core not reachable. Retrying every second. Run agents-chat ui to start it again."
)

var (
	errClipboardUnavailable = errors.New("copying is unavailable on this platform; select the text with the mouse")
)

// Messages that commands send back to Update.
type (
	tickMsg    time.Time
	pluginsMsg struct {
		plugins PluginList
		err     error
	}
	listMsg struct {
		archived bool
		list     ConversationList
		err      error
	}
	showMsg struct {
		conversation string
		after        int64
		view         ConversationView
		err          error
	}
	createdMsg struct {
		created       CreatedConversation
		pluginSlots   []string
		launchFailure []string
		err           error
	}
	invitedMsg struct {
		conversation string
		invitation   JoinInvitation
		err          error
	}
	openInviteMsg struct {
		conversation string
		status       OpenInviteStatus
		err          error
	}
	launchedMsg struct {
		name   string
		queued LaunchQueued
		err    error
	}
	retiredMsg struct {
		participant string
		err         error
	}
	postedMsg   struct{ err error }
	closedMsg   struct{ err error }
	archivedMsg struct {
		conversation string
		archived     bool
		err          error
	}
	searchedMsg struct {
		query   string
		results SearchResults
		err     error
	}
	copiedMsg struct {
		name string
		err  error
	}
)

// model is the whole UI state. Bubble Tea passes it by value through Update.
type model struct {
	client       Operator
	directory    string
	clipboard    func(string) error
	now          func() time.Time
	width        int
	height       int
	screen       screen
	status       string
	offline      bool
	ticks        int
	plugins      []string
	list         listState
	form         formState
	chat         chatState
	search       searchState
	inviteWindow inviteState
	// mouseOff hands the mouse back to the terminal so its own text selection works.
	mouseOff bool
	// sidebarCols and teamCols are the side column widths, changed by dragging a divider.
	sidebarCols int
	teamCols    int
	dragging    divider
	// hover is the id of the clickable part under the pointer, or "".
	hover string
	// invitations holds the join codes this UI session received, by conversation and
	// folded participant name. Codes stay in memory and appear only in the invite window.
	invitations map[string]map[string]JoinInvitation
	// openInvites holds reusable room-level join prompts this session received, by conversation.
	openInvites map[string]OpenInviteInvitation
}

// newModel returns the UI on the chat list for one checkout directory.
func newModel(client Operator, directory string) model {
	return model{
		client:      client,
		directory:   directory,
		clipboard:   copyToClipboard,
		now:         time.Now,
		width:       80,
		height:      24,
		sidebarCols: sidebarWidth,
		teamCols:    teamWidth,
		search:      newSearch(),
		invitations: map[string]map[string]JoinInvitation{},
		openInvites: map[string]OpenInviteInvitation{},
	}
}

// Init loads plugins and the chat list and starts the poll clock.
func (m model) Init() tea.Cmd {
	return tea.Batch(m.loadPlugins(), m.loadList(), tick())
}

// Update routes one message to the handler for its type.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resizeInputs()
		return m, nil
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "ctrl+o":
			return m.toggleMouse(), nil
		}
		return m.handleKey(msg)
	case tea.MouseClickMsg:
		return m.onClick(msg.Mouse())
	case tea.MouseMotionMsg:
		if m.dragging != noDivider {
			return m.onDrag(msg.Mouse()), nil
		}
		m.hover = m.hoverAt(msg.Mouse().X, msg.Mouse().Y)
		return m, nil
	case tea.MouseReleaseMsg:
		m.dragging = noDivider
		return m, nil
	case tea.MouseWheelMsg:
		return m.onWheel(msg.Mouse()), nil
	case tickMsg:
		return m.onTick()
	case pluginsMsg:
		return m.onPlugins(msg), nil
	case listMsg:
		return m.onList(msg), nil
	case showMsg:
		return m.onShow(msg)
	case createdMsg:
		return m.onCreated(msg)
	case invitedMsg:
		return m.onInvited(msg), nil
	case openInviteMsg:
		return m.onOpenInvite(msg)
	case launchedMsg:
		return m.onLaunched(msg), nil
	case retiredMsg:
		return m.onRetired(msg)
	case postedMsg:
		return m.onPosted(msg)
	case closedMsg:
		return m.onClosed(msg)
	case archivedMsg:
		return m.onArchived(msg)
	case searchedMsg:
		return m.onSearched(msg), nil
	case copiedMsg:
		return m.onCopied(msg), nil
	}
	return m.updateFocusedInput(msg)
}

// handleKey sends a key press to the active screen.
func (m model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch m.screen {
	case screenNew:
		return m.formKey(msg)
	case screenSearch:
		return m.searchKey(msg)
	case screenChat:
		if m.inviteWindow.open {
			return m.inviteKey(msg)
		}
		return m.chatKey(msg)
	default:
		return m.homeKey(msg)
	}
}

// updateFocusedInput forwards other messages, such as cursor blinks and pastes, to the
// text field that has focus.
func (m model) updateFocusedInput(msg tea.Msg) (tea.Model, tea.Cmd) {
	var command tea.Cmd
	switch m.screen {
	case screenNew:
		m.form, command = m.form.updateFocused(msg)
	case screenChat:
		m.chat, command = m.chat.updateInput(msg)
	case screenSearch:
		m.search.input, command = m.search.input.Update(msg)
	}
	return m, command
}

// onTick polls the open conversation, or refreshes the list every few ticks.
func (m model) onTick() (tea.Model, tea.Cmd) {
	m.ticks++
	commands := []tea.Cmd{tick()}
	if m.chat.id != "" && m.screen == screenChat {
		var poll tea.Cmd
		m, poll = m.pollChat()
		commands = append(commands, poll)
	}
	if m.ticks%listRefreshTicks == 0 {
		commands = append(commands, m.loadList())
	}
	return m, tea.Batch(commands...)
}

// onPlugins stores the configured plugin names for the source pickers.
func (m model) onPlugins(msg pluginsMsg) model {
	if msg.err != nil {
		return m.fail(msg.err)
	}
	m.plugins = nil
	for _, plugin := range msg.plugins.Plugins {
		m.plugins = append(m.plugins, plugin.Name)
	}
	return m.succeed()
}

// fail shows an error in the status line. The program keeps running.
func (m model) fail(err error) model {
	if errors.Is(err, errCoreUnreachable) {
		m.offline = true
		m.status = offlineStatus
		return m
	}
	m.status = "Error: " + cleanLine(err.Error())
	return m
}

// succeed clears the offline notice after the core answers again.
func (m model) succeed() model {
	if m.offline {
		m.offline = false
		m.status = ""
	}
	return m
}

// inform shows a notice in the status line.
func (m model) inform(text string) model {
	m.status = text
	return m
}

// resizeInputs fits the text fields that exist to the terminal size. The form and the chat
// input are created when their screen first opens.
func (m *model) resizeInputs() {
	if m.screen == screenNew {
		m.form.resize(m.width, m.height)
	}
	_, center, _ := m.columnWidths()
	if m.chat.id != "" {
		m.chat.resize(center)
	}
	m.search.input.SetWidth(max(center-6, 10))
}

// columnWidths returns the widths of the chat list, the center and the team panel. A side
// column that does not fit has width zero.
func (m model) columnWidths() (int, int, int) {
	list, team := 0, 0
	if m.screen != screenNew && m.width >= sidebarMinWidth {
		list = m.sidebarCols
	}
	if m.screen == screenChat && m.width >= teamMinWidth {
		team = m.teamCols
	}
	center := m.width
	for _, side := range []int{list, team} {
		if side > 0 {
			center -= side + 1
		}
	}
	return list, max(center, 1), team
}

// View draws a top bar, the active screen and a key bar at the bottom.
func (m model) View() tea.View {
	bodyHeight := max(m.height-2, 1)
	var body []string
	var help []hint
	switch m.screen {
	case screenNew:
		body, help = column(m.formView(bodyHeight), m.width, bodyHeight), m.formHelp()
	case screenSearch:
		body, help = m.mainView(bodyHeight), m.searchHelp()
	default:
		body, help = m.mainView(bodyHeight), m.mainHelp()
	}
	lines := make([]string, 0, m.height)
	lines = append(lines, m.topBar())
	lines = append(lines, body...)
	bar, _ := m.hintBar(help)
	lines = append(lines, fill(" "+bar, m.width, colorBar))
	view := tea.NewView(strings.Join(lines, "\n"))
	view.AltScreen = true
	if !m.mouseOff {
		view.MouseMode = tea.MouseModeAllMotion
	}
	return view
}

// mainView draws the chat list beside the open conversation and its team.
func (m model) mainView(height int) []string {
	listWidth, centerWidth, team := m.columnWidths()
	var center []string
	switch m.screen {
	case screenChat:
		center = m.chatView(centerWidth, height)
	case screenSearch:
		center = m.searchView(centerWidth, height)
	default:
		center = m.homeView(centerWidth, height)
	}
	columns := [][]string{}
	if listWidth > 0 {
		columns = append(columns, column(m.sidebarView(listWidth, height), listWidth, height))
	}
	columns = append(columns, column(center, centerWidth, height))
	if team > 0 {
		columns = append(columns, column(m.teamView(team, height), team, height))
	}
	body := joinColumns(m.dividers(listWidth > 0, team > 0), columns...)
	if m.inviteWindow.open {
		body = m.drawInvite(body)
	}
	return body
}

// dividers draws the column dividers, lit while the pointer is over one or drags it.
func (m model) dividers(hasList bool, hasTeam bool) []string {
	plain, lit := borderStyle.Render("│"), accentStyle.Render("┃")
	left, right := plain, plain
	if m.hover == "divider:left" || m.dragging == leftDivider {
		left = lit
	}
	if m.hover == "divider:right" || m.dragging == rightDivider {
		right = lit
	}
	switch {
	case hasList && hasTeam:
		return []string{left, right}
	case hasList:
		return []string{left}
	case hasTeam:
		return []string{right}
	}
	return nil
}

// topBar names the app and checkout on the left and shows the status on the right.
func (m model) topBar() string {
	left := " " + brightStyle.Render("agents-chat") + "  " + dimStyle.Render(cleanLine(m.directory))
	kind := "chats"
	if m.list.archived {
		kind = "archived chats"
	}
	right := okStyle.Render("●") + dimStyle.Render(fmt.Sprintf(" core running · %d %s ", len(m.list.items), kind))
	switch {
	case m.offline:
		right = warnStyle.Render("▲ core not reachable, retrying ")
	case m.status != "":
		right = textStyle.Render(cleanLine(m.status) + " ")
	}
	gap := m.width - ansi.StringWidth(left) - ansi.StringWidth(right)
	if gap < 1 {
		return fill(right, m.width, colorBar)
	}
	return fill(left+strings.Repeat(" ", gap)+right, m.width, colorBar)
}

// tick schedules the next poll.
func tick() tea.Cmd {
	return tea.Tick(pollInterval, func(now time.Time) tea.Msg { return tickMsg(now) })
}

// loadPlugins asks the core for the configured plugins.
func (m model) loadPlugins() tea.Cmd {
	client := m.client
	return func() tea.Msg {
		plugins, err := client.Plugins()
		return pluginsMsg{plugins: plugins, err: err}
	}
}

// loadList asks the core for this checkout's active or archived conversations.
func (m model) loadList() tea.Cmd {
	client, request := m.client, ListRequest{Directory: m.directory, Archived: m.list.archived}
	return func() tea.Msg {
		list, err := client.List(request)
		return listMsg{archived: request.Archived, list: list, err: err}
	}
}

// submissionID returns a random ID that protects one post against duplicate delivery.
func submissionID() string {
	random := make([]byte, 16)
	_, _ = rand.Read(random) // crypto/rand.Read never returns an error.
	return "ui-" + hex.EncodeToString(random)
}

// copyToClipboard copies text with pbcopy on macOS. It runs a fixed path without a shell.
func copyToClipboard(text string) error {
	if runtime.GOOS != "darwin" {
		return errClipboardUnavailable
	}
	command := exec.Command("/usr/bin/pbcopy")
	command.Stdin = strings.NewReader(text)
	return command.Run()
}

// foldName returns the key under which a participant name is stored.
func foldName(name string) string {
	return strings.ToLower(name)
}

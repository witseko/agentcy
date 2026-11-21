package main

import (
	"errors"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

const (
	manualSource = "paste prompt"
	maxFormWidth = 84
	// removeButtonWidth is the width of a worker row's remove button.
	removeButtonWidth = 3
	humanName         = "Human"
	maxWorkers        = 16
	maxNameLength     = 64
	maxBriefLength    = 8192
	nameFieldWidth    = 22
)

// fieldKind names what the focused form field edits.
type fieldKind int

const (
	fieldBrief fieldKind = iota
	fieldLeadName
	fieldWorkerName
	fieldWorkerSource
	fieldCreate
)

// leadFields counts the fields before the first worker row.
const leadFields = 2

// workerRow is one worker slot: a name and a source, where source 0 is manual and source i
// is the plugin at index i-1.
type workerRow struct {
	name   textinput.Model
	source int
}

// formState holds the new chat form.
type formState struct {
	brief      textarea.Model
	leadName   textinput.Model
	workers    []workerRow
	focus      int
	submitting bool
}

// slotLaunch names a slot to fill from a plugin after the conversation exists.
type slotLaunch struct {
	name   string
	plugin string
}

// newNameInput returns a single-line field for a participant name.
func newNameInput(value string) textinput.Model {
	input := textinput.New()
	input.Prompt = ""
	input.CharLimit = maxNameLength
	input.SetWidth(nameFieldWidth)
	input.SetValue(value)
	return input
}

// newForm returns the form with a lead named Lead and one worker row.
func newForm(width int, height int) formState {
	brief := textarea.New()
	brief.ShowLineNumbers = false
	brief.CharLimit = maxBriefLength
	brief.Placeholder = "What should the team find out or do? Enter adds a line."
	brief.Prompt = ""
	styles := textarea.DefaultDarkStyles()
	styles.Focused.CursorLine = lipgloss.NewStyle()
	styles.Focused.Text = textStyle
	styles.Blurred.Text = textStyle
	styles.Focused.Placeholder = dimStyle
	styles.Blurred.Placeholder = dimStyle
	brief.SetStyles(styles)
	form := formState{
		brief:    brief,
		leadName: newNameInput("Lead"),
		workers:  []workerRow{{name: newNameInput("Worker")}},
	}
	form.resize(width, height)
	return form
}

// openForm shows a fresh new chat form with the brief focused and reloads the plugin
// names, which may have changed or failed to load at start.
func (m model) openForm() (tea.Model, tea.Cmd) {
	m.form = newForm(m.width, m.height)
	m.screen = screenNew
	m.status = ""
	return m, tea.Batch(m.form.setFocus(0), m.loadPlugins())
}

// resize fits the brief to the terminal.
func (f *formState) resize(width int, height int) {
	f.brief.SetWidth(max(formWidth(width)-4, 20))
	f.brief.SetHeight(min(max(height-22-len(f.workers), 3), 6))
}

// fieldCount counts the focusable fields, the Create button included.
func (f formState) fieldCount() int {
	return leadFields + 2*len(f.workers) + 1
}

// field returns the kind of field at a focus index and its worker row, if any.
func (f formState) field(index int) (fieldKind, int) {
	if index < leadFields {
		return fieldKind(index), -1
	}
	if index == f.fieldCount()-1 {
		return fieldCreate, -1
	}
	offset := index - leadFields
	if offset%2 == 0 {
		return fieldWorkerName, offset / 2
	}
	return fieldWorkerSource, offset / 2
}

// setFocus moves focus to a field and focuses its text input, if it has one.
func (f *formState) setFocus(index int) tea.Cmd {
	f.focus = (index + f.fieldCount()) % f.fieldCount()
	f.brief.Blur()
	f.leadName.Blur()
	for row := range f.workers {
		f.workers[row].name.Blur()
	}
	kind, row := f.field(f.focus)
	switch kind {
	case fieldBrief:
		return f.brief.Focus()
	case fieldLeadName:
		return f.leadName.Focus()
	case fieldWorkerName:
		return f.workers[row].name.Focus()
	}
	return nil
}

// updateFocused sends a message to the focused text field.
func (f formState) updateFocused(msg tea.Msg) (formState, tea.Cmd) {
	var command tea.Cmd
	kind, row := f.field(f.focus)
	switch kind {
	case fieldBrief:
		f.brief, command = f.brief.Update(msg)
	case fieldLeadName:
		f.leadName, command = f.leadName.Update(msg)
	case fieldWorkerName:
		f.workers[row].name, command = f.workers[row].name.Update(msg)
	}
	return f, command
}

// formKey handles a key on the new chat form.
func (m model) formKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.form.submitting {
		return m, nil
	}
	kind, row := m.form.field(m.form.focus)
	switch key := msg.String(); {
	case key == "esc":
		m.screen = screenHome
		if m.chat.id != "" {
			m.screen = screenChat
		}
		m.resizeInputs()
		return m, m.loadList()
	case key == "ctrl+s":
		return m.submitForm()
	case key == "tab":
		return m, m.form.setFocus(m.form.focus + 1)
	case key == "shift+tab":
		return m, m.form.setFocus(m.form.focus - 1)
	case key == "down" && (kind != fieldBrief || m.form.briefCursorOnLastRow()):
		return m, m.form.setFocus(m.form.verticalNeighbor(1))
	case key == "up" && kind != fieldBrief:
		return m, m.form.setFocus(m.form.verticalNeighbor(-1))
	case key == "ctrl+n":
		return m.addWorker()
	case key == "ctrl+d" && row >= 0:
		return m.removeWorker(row)
	case key == "enter" && kind == fieldCreate:
		return m.submitForm()
	case key == "enter" && kind != fieldBrief:
		return m, m.form.setFocus(m.form.focus + 1)
	case isChoiceKey(key) && kind == fieldWorkerSource:
		m.form.changeSource(row, key, len(m.plugins))
		return m, nil
	}
	var command tea.Cmd
	m.form, command = m.form.updateFocused(msg)
	return m, command
}

// isChoiceKey reports the keys that change a worker's source.
func isChoiceKey(key string) bool {
	return key == "left" || key == "right" || key == "space" || key == " "
}

// changeSource cycles a worker's source through manual and each plugin.
func (f *formState) changeSource(row int, key string, pluginCount int) {
	step := 1
	if key == "left" {
		step = -1
	}
	choices := pluginCount + 1
	f.workers[row].source = (f.workers[row].source + step + choices) % choices
}

// briefCursorOnLastRow reports whether the cursor is on the brief's last visual row, so down
// leaves the brief instead of moving inside it.
func (f formState) briefCursorOnLastRow() bool {
	info := f.brief.LineInfo()
	return f.brief.Line() == f.brief.LineCount()-1 && info.RowOffset >= info.Height-1
}

// verticalNeighbor returns the field one row up (step -1) or down (step 1). It keeps the
// column, so a worker's source moves to the next worker's source; the lead row and the
// Create button have one column.
func (f formState) verticalNeighbor(step int) int {
	kind, row := f.field(f.focus)
	last := len(f.workers) - 1
	create := f.fieldCount() - 1
	column := 0
	if kind == fieldWorkerSource {
		column = 1
	}
	switch kind {
	case fieldBrief:
		if step > 0 {
			return int(fieldLeadName)
		}
		return f.focus
	case fieldLeadName:
		if step < 0 {
			return int(fieldBrief)
		}
		return leadFields
	case fieldCreate:
		if step < 0 {
			return leadFields + 2*last
		}
		return f.focus
	}
	next := row + step
	switch {
	case next < 0:
		return int(fieldLeadName)
	case next > last:
		return create
	}
	return leadFields + 2*next + column
}

// addWorker appends a worker row with an unused default name and focuses it.
func (m model) addWorker() (tea.Model, tea.Cmd) {
	if len(m.form.workers) >= maxWorkers {
		return m.inform(fmt.Sprintf("A conversation has at most %d workers.", maxWorkers)), nil
	}
	name := fmt.Sprintf("Worker %d", len(m.form.workers)+1)
	m.form.workers = append(m.form.workers, workerRow{name: newNameInput(name)})
	m.form.resize(m.width, m.height)
	return m, m.form.setFocus(leadFields + 2*(len(m.form.workers)-1))
}

// removeWorker removes a worker row. The form keeps at least one.
func (m model) removeWorker(row int) (tea.Model, tea.Cmd) {
	if len(m.form.workers) == 1 {
		return m.inform("A conversation needs at least one worker."), nil
	}
	m.form.workers = append(m.form.workers[:row:row], m.form.workers[row+1:]...)
	m.form.resize(m.width, m.height)
	return m, m.form.setFocus(min(m.form.focus, m.form.fieldCount()-2))
}

// sourceName returns the plugin chosen by a source index, or "" for manual.
func sourceName(source int, plugins []string) string {
	if source <= 0 || source > len(plugins) {
		return ""
	}
	return plugins[source-1]
}

// sourceLabel shows a source index as its plugin name or "manual".
func sourceLabel(source int, plugins []string) string {
	if name := sourceName(source, plugins); name != "" {
		return name
	}
	return manualSource
}

// request validates the form and builds the create request and the plugin launches.
func (f formState) request(directory string, plugins []string) (CreateRequest, []slotLaunch, error) {
	brief := strings.TrimSpace(f.brief.Value())
	if brief == "" {
		return CreateRequest{}, nil, errors.New("write a brief first")
	}
	lead := strings.TrimSpace(f.leadName.Value())
	names := []string{lead}
	var launches []slotLaunch
	request := CreateRequest{Directory: directory, Brief: brief, Lead: lead}
	for _, row := range f.workers {
		name := strings.TrimSpace(row.name.Value())
		names = append(names, name)
		request.Invitees = append(request.Invitees, name)
		if plugin := sourceName(row.source, plugins); plugin != "" {
			launches = append(launches, slotLaunch{name: name, plugin: plugin})
		}
	}
	if err := checkNames(names); err != nil {
		return CreateRequest{}, nil, err
	}
	return request, launches, nil
}

// checkNames requires non-empty, distinct names that the human's name does not take.
func checkNames(names []string) error {
	seen := map[string]bool{}
	for _, name := range names {
		folded := foldName(name)
		switch {
		case name == "":
			return errors.New("every slot needs a name")
		case folded == foldName(humanName):
			return fmt.Errorf("the name %s belongs to you", humanName)
		case seen[folded]:
			return fmt.Errorf("the name %s is used twice", name)
		}
		seen[folded] = true
	}
	return nil
}

// submitForm creates the conversation and then launches every plugin slot.
func (m model) submitForm() (tea.Model, tea.Cmd) {
	request, launches, err := m.form.request(m.directory, m.plugins)
	if err != nil {
		return m.inform("Cannot create: " + err.Error()), nil
	}
	m.form.submitting = true
	m.status = "Creating the conversation."
	client, directory := m.client, m.directory
	return m, func() tea.Msg {
		created, err := client.Create(request)
		if err != nil {
			return createdMsg{err: err}
		}
		result := createdMsg{created: created}
		for _, slot := range launches {
			result.pluginSlots = append(result.pluginSlots, slot.name)
			_, err := client.Launch(LaunchRequest{
				Directory: directory, Conversation: created.Conversation,
				Name: slot.name, Plugin: slot.plugin,
			})
			if err != nil {
				result.launchFailure = append(result.launchFailure, slot.name+": "+describe(err))
			}
		}
		return result
	}
}

// onCreated keeps the manual slots' join codes and opens the new conversation.
func (m model) onCreated(msg createdMsg) (tea.Model, tea.Cmd) {
	m.form.submitting = false
	if msg.err != nil {
		return m.fail(msg.err), nil
	}
	launched := map[string]bool{}
	for _, name := range msg.pluginSlots {
		launched[foldName(name)] = true
	}
	codes := map[string]JoinInvitation{}
	for _, invitation := range msg.created.Invitations {
		// A launch reissues its slot's code, so only manual slots keep the create result.
		if !launched[foldName(invitation.Participant)] {
			codes[foldName(invitation.Participant)] = invitation
		}
	}
	m.invitations[msg.created.Conversation] = codes
	next, command := m.openChat(msg.created.Conversation)
	m = next.(model)
	command = tea.Batch(command, m.loadList())
	if len(codes) > 0 {
		m = m.openInvite("")
	}
	switch {
	case len(msg.launchFailure) > 0:
		m.status = "Created. Launch failed for " + cleanLine(strings.Join(msg.launchFailure, "; "))
	case len(codes) > 0:
		m.status = fmt.Sprintf("Created. %d join prompt(s) to paste.", len(codes))
	default:
		m.status = "Created. Plugins are starting."
	}
	return m, command
}

// describe turns an error into one status line without sensitive detail.
func describe(err error) string {
	if errors.Is(err, errCoreUnreachable) {
		return "core not reachable"
	}
	return err.Error()
}

// formWidth returns the width of the centered form for a terminal width.
func formWidth(width int) int {
	return max(min(maxFormWidth, width-4), 40)
}

// formHelp returns the key hints for the bottom bar of the new chat form.
func (m model) formHelp() []hint {
	return []hint{{"↑↓", "move"}, {"tab", "next field"}, {"⏎", "new line in brief"}, {"← →", "how a worker joins"},
		{"ctrl+n", "add worker"}, {"ctrl+s", "create"}, {"esc", "cancel"}}
}

// Form line targets that are not a focusable field.
const (
	formLineNone      = -1
	formLineAddWorker = -2
)

// formView draws the centered form and scrolls to keep the focused field visible.
func (m model) formView(height int) []string {
	lines, _, focusLine := m.formLayout()
	first := scrollFirst(len(lines), focusLine, height)
	return lines[first:min(first+height, len(lines))]
}

// formLayout draws every form line and returns, for each line, the field index a click on
// it focuses (or a formLine value), and the line that holds the focused field.
func (m model) formLayout() ([]string, []int, int) {
	f := m.form
	width := formWidth(m.width)
	margin := strings.Repeat(" ", max((m.width-width)/2, 0))
	title := brightStyle.Render("New chat")
	hint := dimStyle.Render("ctrl+s creates from any field")
	lines := []string{"", title + strings.Repeat(" ", max(width-ansi.StringWidth(title)-ansi.StringWidth(hint), 1)) + hint, ""}

	lines = append(lines, headStyle.Render("BRIEF")+"  "+dimStyle.Render("what should the team find out or do?"))
	focusLine := len(lines)
	border := borderStyle
	if kind, _ := f.field(f.focus); kind == fieldBrief {
		border = accentStyle
	}
	lines = append(lines, border.Render("┌"+strings.Repeat("─", width-2)+"┐"))
	for _, line := range strings.Split(f.brief.View(), "\n") {
		lines = append(lines, border.Render("│")+" "+pad(line, width-4)+" "+border.Render("│"))
	}
	lines = append(lines, border.Render("└"+strings.Repeat("─", width-2)+"┘"), "")
	fieldOf := make([]int, len(lines))
	for index := range fieldOf {
		fieldOf[index] = formLineNone
		if index >= focusLine && index < len(lines)-1 {
			fieldOf[index] = int(fieldBrief)
		}
	}

	lines = append(lines, headStyle.Render("TEAM"),
		dimStyle.Render(fmt.Sprintf("  %-*s  %-8s  %s", nameFieldWidth, "NAME", "ROLE", "JOINS VIA")))
	if f.focus > 0 && f.focus < leadFields {
		focusLine = len(lines)
	}
	fieldOf = append(fieldOf, formLineNone, formLineNone)
	lines = append(lines, m.leadLine(width))
	fieldOf = append(fieldOf, int(fieldLeadName))
	for row := range f.workers {
		if f.focus == leadFields+2*row || f.focus == leadFields+2*row+1 {
			focusLine = len(lines)
		}
		lines = append(lines, m.workerLine(row, width))
		fieldOf = append(fieldOf, leadFields+2*row)
	}
	addButton, _ := m.buttons(button{id: "add", label: "+ Add worker"})
	lines = append(lines, "  "+addButton+"   "+keyHints("ctrl+n", "add", "ctrl+d", "remove", "← →", "how it joins"), "")
	fieldOf = append(fieldOf, formLineAddWorker, formLineNone)

	lines = append(lines, m.createSummary()...)
	lines = append(lines, "")
	for len(fieldOf) < len(lines) {
		fieldOf = append(fieldOf, formLineNone)
	}
	actions, _ := m.formButtons()
	marker := "  "
	if kind, _ := f.field(f.focus); kind == fieldCreate {
		marker = accentStyle.Render("› ")
		focusLine = len(lines)
	}
	if f.submitting {
		actions = dimStyle.Render("Creating...")
	}
	lines = append(lines, marker+actions)
	fieldOf = append(fieldOf, f.fieldCount()-1)
	for index, line := range lines {
		lines[index] = margin + line
	}
	return lines, fieldOf, focusLine
}

// formButtons draws the Create and Cancel buttons.
func (m model) formButtons() (string, []span) {
	return m.buttons(button{id: "create", label: "Create  ctrl+s", primary: true}, button{id: "cancel", label: "Cancel"})
}

// removeButton draws the button that removes a worker row. A form keeps at least one worker.
func (m model) removeButton(row int) (string, []span) {
	if len(m.form.workers) < 2 {
		return "", nil
	}
	return m.buttons(button{id: fmt.Sprintf("remove:%d", row), label: "×"})
}

// formLeft returns the column where the centered form starts.
func formLeft(width int) int {
	return max((width-formWidth(width))/2, 0)
}

// sourceColumn is where a worker row's join source starts, counted from the form's left edge:
// marker, name field, gap, role, gap.
const sourceColumn = 2 + nameFieldWidth + 2 + 8 + 2

// createSummary says what pressing create will do: which plugins start and which join
// prompts the user gets to paste.
func (m model) createSummary() []string {
	f := m.form
	bar := accentStyle.Render("▎ ")
	lines := []string{bar + headStyle.Render("WHAT HAPPENS ON CREATE")}
	prompts := []string{roleStyle("lead").Render(cleanLine(strings.TrimSpace(f.leadName.Value())))}
	for _, row := range f.workers {
		name := cleanLine(strings.TrimSpace(row.name.Value()))
		if plugin := sourceName(row.source, m.plugins); plugin != "" {
			lines = append(lines, bar+roleStyle("worker").Render(cleanLine(plugin))+textStyle.Render(" starts ")+
				brightStyle.Render(name)+textStyle.Render(" right away."))
		} else {
			prompts = append(prompts, textStyle.Render(name))
		}
	}
	lines = append(lines, bar+textStyle.Render(fmt.Sprintf("You get %d join prompt(s) to paste: ", len(prompts)))+
		strings.Join(prompts, dimStyle.Render(", "))+textStyle.Render("."))
	return append(lines, bar+dimStyle.Render("Each code works once and expires in one hour."))
}

// leadLine draws the lead's row. The lead always joins with a pasted join prompt.
func (m model) leadLine(width int) string {
	marker := "  "
	if m.form.focus == int(fieldLeadName) {
		marker = accentStyle.Render("› ")
	}
	return marker + pad(m.form.leadName.View(), nameFieldWidth) + "  " + dimStyle.Render(fmt.Sprintf("%-8s", "lead")) +
		"  " + dimStyle.Render("a chat you paste the prompt into")
}

// workerLine draws one worker row with its join source.
func (m model) workerLine(row int, width int) string {
	worker := m.form.workers[row]
	nameFocused := m.form.focus == leadFields+2*row
	sourceFocused := m.form.focus == leadFields+2*row+1
	marker := "  "
	if nameFocused || sourceFocused {
		marker = accentStyle.Render("› ")
	}
	note := ""
	if sourceName(worker.source, m.plugins) != "" {
		note = dimStyle.Render("starts on create")
	}
	line := marker + pad(worker.name.View(), nameFieldWidth) + "  " + dimStyle.Render(fmt.Sprintf("%-8s", "worker")) +
		"  " + choice(sourceLabel(worker.source, m.plugins), sourceFocused) + "  " + note
	if remove, _ := m.removeButton(row); remove != "" {
		line = pad(line, width-removeButtonWidth) + remove
	}
	if nameFocused || sourceFocused {
		return fill(line, width, colorSelect)
	}
	return line
}

// choice draws a value that left and right change, highlighted when focused.
func choice(value string, focused bool) string {
	if focused {
		return accentStyle.Render("‹ ") + brightStyle.Render(cleanLine(value)) + accentStyle.Render(" ›")
	}
	return dimStyle.Render("‹ ") + textStyle.Render(cleanLine(value)) + dimStyle.Render(" ›")
}

// scrollFirst returns the first of count lines to show in height rows around line focus.
func scrollFirst(count int, focus int, height int) int {
	if count <= height {
		return 0
	}
	return min(max(focus-height/2, 0), count-height)
}

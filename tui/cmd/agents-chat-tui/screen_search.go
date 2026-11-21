package main

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

const (
	// searchHeaderRows are the title, input, summary and rule rows above the results.
	searchHeaderRows = 4
	// searchHitRows are the title, detail and snippet rows of a result and the gap after it.
	searchHitRows   = 4
	maxSearchLength = 200
	hitTimeLayout   = "2 Jan 2006 15:04"
)

// searchState holds the search box and the results of the last search.
type searchState struct {
	input    textinput.Model
	hits     []SearchHit
	query    string
	pending  string
	selected int
	searched bool
	// returnTo is the screen that esc goes back to.
	returnTo screen
}

// newSearch returns an empty search box.
func newSearch() searchState {
	input := textinput.New()
	input.Prompt = ""
	input.Placeholder = "Words from a brief or message"
	input.CharLimit = maxSearchLength
	return searchState{input: input}
}

// openSearch shows the search box in the center and keeps the last results.
func (m model) openSearch() (tea.Model, tea.Cmd) {
	if m.screen != screenSearch {
		m.search.returnTo = m.screen
	}
	m.screen = screenSearch
	m.chat.input.Blur()
	m.resizeInputs()
	return m, m.search.input.Focus()
}

// closeSearch goes back to the chat list or the open chat.
func (m model) closeSearch() (tea.Model, tea.Cmd) {
	m.search.input.Blur()
	if m.search.returnTo == screenChat && m.chat.id != "" {
		m.screen = screenChat
		return m.setChatFocus(m.chat.focus)
	}
	m.screen = screenHome
	return m, nil
}

// searchKey handles a key on the search screen. Typing edits the query, the arrows choose a
// result and enter searches a changed query or opens the chosen result.
func (m model) searchKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		return m.closeSearch()
	case "up":
		m.search.selected = max(m.search.selected-1, 0)
		return m, nil
	case "down":
		m.search.selected = min(m.search.selected+1, max(len(m.search.hits)-1, 0))
		return m, nil
	case "enter":
		query := strings.TrimSpace(m.search.input.Value())
		switch {
		case query == "":
			return m, nil
		case query != m.search.query || !m.search.searched:
			return m.runSearch(query)
		case len(m.search.hits) > 0:
			return m.openHit(m.search.hits[m.search.selected])
		}
		return m, nil
	}
	var command tea.Cmd
	m.search.input, command = m.search.input.Update(msg)
	return m, command
}

// runSearch asks the core for matches of query.
func (m model) runSearch(query string) (tea.Model, tea.Cmd) {
	m.search.pending = query
	request := SearchRequest{Directory: m.directory, Query: query}
	client := m.client
	return m.inform("Searching."), func() tea.Msg {
		results, err := client.Search(request)
		return searchedMsg{query: query, results: results, err: err}
	}
}

// onSearched shows the results of the latest search and drops answers to older ones.
func (m model) onSearched(msg searchedMsg) model {
	if msg.query != m.search.pending {
		return m
	}
	m.search.pending = ""
	if msg.err != nil {
		return m.fail(msg.err)
	}
	m.search.hits = msg.results.Hits
	m.search.query = msg.query
	m.search.selected = 0
	m.search.searched = true
	m.status = ""
	return m.succeed()
}

// openHit opens the chat of a result and scrolls to the matching message once the
// transcript has loaded. The chat list switches to the archive for an archived chat.
func (m model) openHit(hit SearchHit) (tea.Model, tea.Cmd) {
	var listCommand tea.Cmd
	if m.list.archived != hit.Archived {
		m.list = listState{archived: hit.Archived}
		listCommand = m.loadList()
	}
	m.search.input.Blur()
	next, command := m.openChat(hit.Conversation)
	opened := next.(model)
	opened.chat.jumping = true
	if hit.MessageID != nil {
		opened.chat.jumpTarget = *hit.MessageID
	}
	return opened, tea.Batch(command, listCommand)
}

// searchHelp returns the key hints for the search screen.
func (m model) searchHelp() []hint {
	return []hint{{"⏎", "search or open"}, {"↑↓", "choose"}, {"esc", "back"}, {"ctrl+o", m.mouseHint()}}
}

// searchView draws the search box, a summary line and the results, four rows per result.
func (m model) searchView(width int, height int) []string {
	lines := []string{
		" " + headStyle.Render("SEARCH ALL CHATS"),
		" " + accentStyle.Render("›") + " " + m.search.input.View(),
		"   " + dimStyle.Render(m.searchSummary()),
		rule(width),
	}
	rows := max((height-searchHeaderRows)/searchHitRows, 1)
	first := max(m.search.selected-rows+1, 0)
	terms := queryTerms(m.search.query)
	for index := first; index < len(m.search.hits) && index < first+rows; index++ {
		block := m.hitLines(m.search.hits[index], terms, width)
		switch {
		case index == m.search.selected:
			for row, line := range block {
				block[row] = fill(accentStyle.Render("▎")+line, width, colorSelect)
			}
		case m.hover == fmt.Sprintf("hit:%d", index):
			for row, line := range block {
				block[row] = fill(dimStyle.Render("▎")+line, width, colorBand)
			}
		default:
			for row, line := range block {
				block[row] = " " + line
			}
		}
		lines = append(lines, block...)
		lines = append(lines, "")
	}
	return lines
}

// hitLines draws a result as its chat title, where the match is, and the snippet.
func (m model) hitLines(hit SearchHit, terms []string, width int) []string {
	title := brightStyle.Render(ansi.Truncate(cleanLine(hit.Title), max(width-14, 10), "…"))
	if hit.Archived {
		title += "  " + tag("ARCHIVED", colorDim)
	}
	place := "in the brief"
	if hit.Author != nil {
		place = cleanLine(*hit.Author)
		if place == humanName {
			place = "You"
		}
	}
	when := "-"
	if hit.Created != nil {
		when = time.Unix(*hit.Created, 0).Format(hitTimeLayout)
	}
	snippet := ansi.Truncate(highlightTerms(cleanLine(hit.Snippet), terms), max(width-3, 10), "…")
	return []string{title, dimStyle.Render(place + " · " + when), snippet}
}

// searchSummary says what the search screen is doing or how many results it shows.
func (m model) searchSummary() string {
	switch {
	case m.search.pending != "":
		return "Searching."
	case !m.search.searched:
		return "Type words and press ⏎. Every word must match, and a word also finds longer words."
	case len(m.search.hits) == 0:
		return "No matches for " + cleanLine(m.search.query) + "."
	case len(m.search.hits) == 1:
		return "1 match · ⏎ opens it at the message"
	}
	return fmt.Sprintf("%d matches, best first · ⏎ opens the chosen one at the message", len(m.search.hits))
}

// searchHitAt returns the result drawn at a body row of the search screen.
func (m model) searchHitAt(row int, height int) (int, bool) {
	if row < searchHeaderRows || (row-searchHeaderRows)%searchHitRows == searchHitRows-1 {
		return 0, false
	}
	rows := max((height-searchHeaderRows)/searchHitRows, 1)
	first := max(m.search.selected-rows+1, 0)
	index := first + (row-searchHeaderRows)/searchHitRows
	if index >= len(m.search.hits) || index >= first+rows {
		return 0, false
	}
	return index, true
}

// clickSearch opens the clicked result.
func (m model) clickSearch(row int, height int) (tea.Model, tea.Cmd) {
	index, found := m.searchHitAt(row, height)
	if !found {
		return m, nil
	}
	m.search.selected = index
	return m.openHit(m.search.hits[index])
}

// queryTerms splits a query into lower-case words the way the core matches them.
func queryTerms(query string) []string {
	return strings.FieldsFunc(strings.ToLower(query), func(character rune) bool {
		return !isWordRune(character)
	})
}

// isWordRune reports the characters that the core's index treats as part of a word.
func isWordRune(character rune) bool {
	return unicode.IsLetter(character) || unicode.IsDigit(character) || character == '_'
}

// wordRuns splits text into alternating runs of word and other characters.
func wordRuns(text string) []string {
	var runs []string
	var current []rune
	for _, character := range text {
		if len(current) > 0 && isWordRune(current[0]) != isWordRune(character) {
			runs = append(runs, string(current))
			current = nil
		}
		current = append(current, character)
	}
	if len(current) > 0 {
		runs = append(runs, string(current))
	}
	return runs
}

// highlightTerms draws the words of text that start with a query term in the accent color.
func highlightTerms(text string, terms []string) string {
	var builder, plain strings.Builder
	flushPlain := func() {
		if plain.Len() > 0 {
			builder.WriteString(textStyle.Render(plain.String()))
			plain.Reset()
		}
	}
	for _, run := range wordRuns(text) {
		if matchesTerm(strings.ToLower(run), terms) {
			flushPlain()
			builder.WriteString(accentStyle.Render(run))
			continue
		}
		plain.WriteString(run)
	}
	flushPlain()
	return builder.String()
}

// matchesTerm reports whether word starts with one of the terms.
func matchesTerm(word string, terms []string) bool {
	for _, term := range terms {
		if strings.HasPrefix(word, term) {
			return true
		}
	}
	return false
}

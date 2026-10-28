package chat

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// barMaxRows bounds the conversation bar; beyond it, "+N more" stands for the
// tabs that don't fit, and the selected tab always shows.
const barMaxRows = 3

// conversationViews lists the bar for the state view: All, then the
// directory in order, hidden conversations included and marked.
func (m *Model) conversationViews() []ConversationView {
	m.refreshDirectory()
	out := []ConversationView{{All: true, Position: 1, Unread: m.unreadIn(allKey), Selected: m.conv.selected.common(), Draft: m.savedDraft(allKey)}}
	position := 1
	for _, key := range m.conv.order {
		v := ConversationView{ID: viewID(key), Group: key.group(), Hidden: m.conv.hidden[key], Unread: m.unreadIn(key),
			Selected: m.conv.selected == key, Draft: m.savedDraft(key), Contact: key.contact}
		if !v.Hidden {
			position++
			v.Position = position
		}
		out = append(out, v)
	}
	return out
}

// viewID is a conversation's ID in the state view: the agent ID of a 1:1, or
// the raw member IDs of a group joined by ", ".
func viewID(key viewKey) string {
	if key.isContact() {
		return key.label() // "⇄ workspace …"; never a valid agent ID
	}
	if key.isRoom() {
		return key.label() // "#room"; never a valid agent ID
	}
	if key.isGroup() {
		return strings.Join(key.group(), ", ")
	}
	return key.agent()
}

// selectedID is the selected conversation's ID in the state view.
func (m *Model) selectedID() string { return viewID(m.conv.selected) }

// hiddenCounts are the hidden conversations and their unread messages.
func (m *Model) hiddenCounts() (views, unread int) {
	for key := range m.conv.hidden {
		views++
		unread += m.unreadIn(key)
	}
	return views, unread
}

// barTab is one rendered tab and its display width.
type barTab struct {
	text     string
	width    int
	selected bool
}

// barTabs renders the visible tabs in order.
func (m *Model) barTabs() []barTab {
	var tabs []barTab
	for _, c := range m.conversationViews() {
		if c.Hidden {
			continue
		}
		tabs = append(tabs, renderTab(c))
	}
	return tabs
}

func renderTab(c ConversationView) barTab {
	name, style := "All", titleStyle
	if !c.All {
		name, style = safe(c.ID), agentStyle(c.ID)
	}
	label := strconv.Itoa(c.Position) + " " + name
	badge := ""
	if c.Unread > 0 {
		badge = " •" + strconv.Itoa(c.Unread)
	}
	text := " " + style.Render(label) + everyoneStyle.Render(badge) + " "
	if c.Selected {
		// The selected tab is the selection band with explicit colours, the
		// badge bold inside it; never reverse video.
		text = pickStyle.Render(" "+label) + pickStyle.Bold(true).Render(badge+" ")
	}
	return barTab{text: text, width: ansi.StringWidth(text), selected: c.Selected}
}

// barLines lays the tabs out on at most rows lines of the width. Tabs that
// don't fit are counted as "+N more", the selected tab is always shown, and
// the hidden-conversation summary always ends the bar.
func (m *Model) barLines(rows int) []string {
	return packTabs(m.barTabs(), m.barTail(), max(1, rows), m.width)
}

// barNeed is how many rows the bar needs at most, up to barMaxRows.
func (m *Model) barNeed() int {
	return len(m.barLines(barMaxRows))
}

// barTail is what always ends the bar: the hidden summary, if any.
func (m *Model) barTail() []barTab {
	hidden, unread := m.hiddenCounts()
	if hidden == 0 {
		return nil
	}
	summary := " " + ruleStyle.Render(strconv.Itoa(hidden)+" hidden · "+strconv.Itoa(unread)+" unread") + " "
	return []barTab{{text: summary, width: ansi.StringWidth(summary)}}
}

// packTabs fills lines greedily, then the tail. When everything needs more
// than rows lines, the fewest tabs are dropped so that the kept tabs, a
// "+N more" marker and the tail fit; a selected tab that would be dropped
// takes the place of the last kept one.
func packTabs(tabs, tail []barTab, rows, width int) []string {
	all := append(append([]barTab{}, tabs...), tail...)
	if lines := pack(all, width); len(lines) <= rows {
		return render(lines)
	}
	for fit := len(tabs) - 1; fit >= 0; fit-- {
		kept := keepSelected(tabs, fit)
		row := append(append(kept, moreTab(len(tabs)-len(kept))), tail...)
		if lines := pack(row, width); len(lines) <= rows || fit == 0 {
			return render(lines[:min(len(lines), rows)])
		}
	}
	return nil
}

// keepSelected is the first fit tabs, with the selected tab swapped in for
// the last one when it lies beyond them.
func keepSelected(tabs []barTab, fit int) []barTab {
	kept := append([]barTab{}, tabs[:fit]...)
	for _, t := range tabs[fit:] {
		if t.selected && fit > 0 {
			kept[fit-1] = t
		}
	}
	return kept
}

func moreTab(n int) barTab {
	text := " " + ruleStyle.Render("+"+strconv.Itoa(n)+" more") + " "
	return barTab{text: text, width: ansi.StringWidth(text)}
}

// pack groups tabs into lines no wider than width; a tab wider than the
// line gets a line of its own and is cut on render.
func pack(tabs []barTab, width int) [][]barTab {
	var lines [][]barTab
	var line []barTab
	used := 0
	for _, t := range tabs {
		if len(line) > 0 && used+t.width > width {
			lines, line, used = append(lines, line), nil, 0
		}
		line, used = append(line, t), used+t.width
	}
	if len(line) > 0 {
		lines = append(lines, line)
	}
	return lines
}

func render(lines [][]barTab) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		var b strings.Builder
		for _, t := range line {
			b.WriteString(t.text)
		}
		out[i] = b.String()
	}
	return out
}

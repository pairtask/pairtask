package chat

import (
	"strconv"
	"strings"
)

// conversationViews lists the bar for the state view.
func (m *Model) conversationViews() []ConversationView {
	m.refreshDirectory()
	out := []ConversationView{{All: true, Position: 1, Unread: m.unreadIn(allKey), Selected: m.conv.selected.common(), Draft: m.savedDraft(allKey)}}
	for i, id := range m.conv.order {
		key := viewKey(id)
		out = append(out, ConversationView{ID: id, Position: i + 2, Unread: m.unreadIn(key), Selected: m.conv.selected == key, Draft: m.savedDraft(key)})
	}
	return out
}

// barRow renders the conversation bar: positions, labels, unread badges, the
// selected one reversed in its colour, scrolled so the selected one shows.
func (m *Model) barRow() string {
	var items []string
	widths := []int{}
	selected := 0
	for _, c := range m.conversationViews() {
		name := "All"
		style := titleStyle
		if !c.All {
			name, style = safe(c.ID), agentStyle(c.ID)
		}
		label := strconv.Itoa(c.Position) + " " + name
		badge := ""
		if c.Unread > 0 {
			badge = " •" + strconv.Itoa(c.Unread)
		}
		if c.Selected {
			// The selected tab is the selection band with explicit colours,
			// the badge bold inside it; never reverse video.
			selected = len(items)
			label = pickStyle.Render(" "+label) + pickStyle.Bold(true).Render(badge+" ")
		} else {
			label = " " + style.Render(label) + everyoneStyle.Render(badge) + " "
		}
		items = append(items, label)
		widths = append(widths, len(name)+len(strconv.Itoa(c.Position))+3+badgeWidth(c.Unread))
	}
	return fitBar(items, widths, selected, m.width)
}

func badgeWidth(n int) int {
	if n == 0 {
		return 0
	}
	return 2 + len(strconv.Itoa(n))
}

// fitBar joins the items from the first one that still shows the selected
// item within the width, marking an overflow on the left with "…".
func fitBar(items []string, widths []int, selected, width int) string {
	start := 0
	for {
		used := 0
		for i := start; i <= selected; i++ {
			used += widths[i]
		}
		if used <= width-1 || start == selected {
			break
		}
		start++
	}
	row := ""
	if start > 0 {
		row = ruleStyle.Render("…")
	}
	return row + strings.Join(items[start:], "")
}

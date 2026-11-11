package chat

import (
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/lipgloss/v2"
)

// The input fields' colours assume neither a dark nor a light terminal. The
// libraries' defaults are for a dark one: the line being typed gets a black
// background with no text colour, so on a light terminal the text, in its
// default black, can't be seen. Here text, prompt and cursor take the
// terminal's own colours, a selection is reversed, and only the placeholder
// has a colour of its own, a mid grey readable on either.

var (
	inputPlain       = lipgloss.NewStyle()
	inputMuted       = lipgloss.NewStyle().Faint(true)
	inputPlaceholder = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
)

// editorStyles are the composer's.
func editorStyles() textarea.Styles {
	s := textarea.DefaultStyles(true)
	s.Focused = textarea.StyleState{Base: inputPlain, CursorLine: inputPlain, CursorLineNumber: inputMuted, EndOfBuffer: inputPlain,
		LineNumber: inputMuted, Placeholder: inputPlaceholder, Prompt: inputPlain, Selection: inputPlain.Reverse(true), Text: inputPlain}
	s.Blurred = s.Focused
	s.Blurred.CursorLine, s.Blurred.Prompt, s.Blurred.Text = inputMuted, inputMuted, inputMuted
	s.Cursor.Color = lipgloss.NoColor{}
	return s
}

// commandStyles are the command line's.
func commandStyles() textinput.Styles {
	s := textinput.DefaultStyles(true)
	s.Focused = textinput.StyleState{Text: inputPlain, Placeholder: inputPlaceholder, Suggestion: inputPlaceholder, Prompt: inputPlain}
	s.Blurred = textinput.StyleState{Text: inputMuted, Placeholder: inputPlaceholder, Suggestion: inputPlaceholder, Prompt: inputMuted}
	s.Cursor.Color = lipgloss.NoColor{}
	return s
}

package chat

import "fmt"

// /reply (and /r) offers the received messages of the open conversation,
// newest first: in All every one, in a room its own, in a 1:1 or group its
// senders'. Picking only fills the line; the next Enter binds the reply to
// that message's ID, which no newer arrival or view switch changes. Esc
// cancels a bound reply, keeping what was typed.

// replyCompletions are the messages a reply can answer here.
func (m *Model) replyCompletions(l argLine) []string {
	if len(l.words) > 0 {
		return nil
	}
	var rows []string
	for i := len(m.timeline.order) - 1; i >= 0; i-- {
		if e := m.timeline.order[i]; m.targetable(e) && m.inSelected(e) {
			rows = append(rows, fmt.Sprintf("#%d", e.msg.Seq))
		}
	}
	if m.compose.reply != nil {
		rows = append(rows, "cancel") // a bound reply can be left from here too
	}
	return withPrefix(rows, l.current)
}

// replyPickLabel is a /reply picker row: the message, as /ack shows it.
func (m *Model) replyPickLabel(item string) (string, bool) {
	if l, ok := m.argumentLine(); !ok || !replyLine(l) {
		return "", false
	}
	if e := m.entryAt(item); e != nil {
		return m.ackLabel(e.msg.MessageID), true
	}
	return "", false
}

// replyLine: the command line is /reply, or its alias /r.
func replyLine(l argLine) bool {
	return l.name == "reply" || l.name == "r"
}

// cancelReply forgets a bound reply; the draft stays, to the same view.
func (m *Model) cancelReply() bool {
	if m.compose.reply == nil {
		return false
	}
	m.compose.reply = nil
	m.status = "reply cancelled"
	return true
}

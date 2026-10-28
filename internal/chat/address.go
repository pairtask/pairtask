package chat

import (
	"regexp"
	"slices"
	"strings"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Audience sources, shown in the hint line and the state view.
const (
	SourceEveryone = "everyone"
	SourceMentions = "mentions"
	SourceTo       = "to"
	SourceReply    = "reply"
)

// mentionID is the inline mention grammar: narrower than a protocol agent ID,
// which may contain commas, spaces or "@". Other IDs are addressed with /to.
var mentionID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// audience is who a draft would go to, or why it can't be sent.
type audience struct {
	everyone bool
	room     string // with everyone: a room's members instead of the Square
	ids      []string
	mentions []string // members of the room or group it addresses (#164)
	notSeen  []string
	source   string
	err      string
}

// prefix is the leading mention prefix of a draft.
type prefix struct {
	tokens  []string // mention IDs without "@", in order
	partial string   // an unfinished or invalid token, blocking
	body    string   // the text after the prefix
	escaped bool     // the draft started with `\@`
}

// parsePrefix reads the mentions at the start of a draft's first line.
// Leading spaces are ignored; spaces or tabs separate tokens; the first
// non-mention or a line break ends the prefix. `\@` escapes a literal "@".
func parsePrefix(draft string) prefix {
	rest := strings.TrimLeft(draft, " \t")
	if strings.HasPrefix(rest, `\@`) {
		return prefix{body: rest[1:], escaped: true}
	}
	var p prefix
	for strings.HasPrefix(rest, "@") {
		end := strings.IndexAny(rest, " \t\n")
		if end < 0 {
			end = len(rest)
		}
		token := rest[1:end]
		if !mentionID.MatchString(token) {
			p.partial = rest[:end]
			break
		}
		p.tokens = append(p.tokens, token)
		rest = strings.TrimLeft(rest[end:], " \t")
	}
	p.body = rest
	return p
}

// resolveAudience applies the addressing rules to the composer. In All a
// draft goes to everyone unless it mentions agents or carries a reply or /to
// audience; in a 1:1 it goes to that agent, and mentions of anyone else block.
func (m *Model) resolveAudience() audience {
	if key := m.conv.selected; key.isContact() {
		return audience{err: "messages to a workspace are sent from its ⇄ tab"}
	}
	c := &m.compose
	if c.pasted {
		return m.explicitAudience()
	}
	p := parsePrefix(c.editor.Value())
	if p.partial != "" {
		return audience{err: "not an agent mention: " + safe(p.partial) + " (use /to for other IDs, \\@ for a literal @)"}
	}
	if len(p.tokens) == 0 {
		return m.explicitAudience()
	}
	if c.reply != nil {
		return audience{err: "remove the mentions or /reply cancel"}
	}
	if c.to != nil {
		return audience{err: "use either /to or @mentions, not both"}
	}
	return m.viewMentions(p.tokens)
}

// viewMentions resolves leading mentions in the selected conversation: any
// agents in All, only its own agent in a 1:1, and in a room or group the
// members it addresses, its audience unchanged.
func (m *Model) viewMentions(tokens []string) audience {
	key := m.conv.selected
	switch {
	case key.isGroup() || key.isRoom():
		return m.attentionMentions(tokens)
	case !key.common():
		return m.ownMentions(tokens, key.agent())
	}
	return m.mentionAudience(tokens)
}

// ownMentions allows mentions in a 1:1 only when they name that agent. The
// broadcast keyword @all is never a name here, even for an agent literally
// called all.
func (m *Model) ownMentions(tokens []string, key string) audience {
	for _, t := range tokens {
		if t == "all" || t != key {
			return audience{err: "mentions address other agents only in All (Tab)"}
		}
	}
	return audience{ids: []string{key}, source: SourceMentions, notSeen: m.unseen([]string{key})}
}

// explicitAudience is the reply or /to audience, the 1:1's agent, or everyone.
func (m *Model) explicitAudience() audience {
	c := &m.compose
	switch {
	case c.reply != nil && c.reply.public:
		return audience{everyone: true, room: c.reply.room, source: SourceReply}
	case c.reply != nil:
		return audience{ids: []string{c.reply.sender}, source: SourceReply, notSeen: m.unseen([]string{c.reply.sender})}
	case c.to != nil:
		return audience{ids: c.to, source: SourceTo, notSeen: m.unseen(c.to)}
	case m.conv.selected.isRoom():
		return audience{everyone: true, room: m.conv.selected.room, source: SourceConversation}
	case m.conv.selected.isGroup():
		ids := m.conv.selected.group()
		return audience{ids: ids, source: SourceConversation, notSeen: m.unseen(ids)}
	case !m.conv.selected.common():
		id := m.conv.selected.agent()
		return audience{ids: []string{id}, source: SourceConversation, notSeen: m.unseen([]string{id})}
	}
	return audience{everyone: true, source: SourceEveryone}
}

func (m *Model) mentionAudience(tokens []string) audience {
	if slices.Contains(tokens, "all") {
		if len(tokens) > 1 {
			return audience{err: "@all can't be combined with other mentions"}
		}
		return audience{everyone: true, source: SourceMentions}
	}
	ids := slices.Clone(tokens)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if len(ids) > protocol.MaxRecipients {
		return audience{err: "at most 256 recipients"}
	}
	return audience{ids: ids, source: SourceMentions, notSeen: m.unseen(ids)}
}

// unseen lists IDs that are neither online nor in the loaded conversation.
// Presence is advisory, so these are marked, not refused.
func (m *Model) unseen(ids []string) []string {
	var out []string
	for _, id := range ids {
		if !m.known(id) {
			out = append(out, id)
		}
	}
	return out
}

func (m *Model) known(id string) bool {
	return slices.Contains(m.agentCandidates(id), id)
}

// scope is the operator_send addressing for a sendable audience.
func (a audience) scope() (protocol.Message, error) {
	if a.everyone {
		return protocol.Message{To: protocol.Broadcast, Room: a.room, Mentions: a.mentions}, nil
	}
	scope, err := scopeFor(a.ids)
	scope.Mentions = a.mentions
	return scope, err
}

// outgoingText is the text that is sent: the draft as typed, except that a
// leading `\@` escape loses its backslash.
func (m *Model) outgoingText() string {
	text := m.compose.editor.Value()
	if m.compose.pasted {
		return text
	}
	if p := parsePrefix(text); p.escaped {
		lead := len(text) - len(strings.TrimLeft(text, " \t"))
		return text[:lead] + text[lead+1:]
	}
	return text
}

// draftBody reports whether the draft has text beyond its mention prefix.
func (m *Model) draftBody() bool {
	text := m.compose.editor.Value()
	if m.compose.pasted {
		return strings.TrimSpace(text) != ""
	}
	return strings.TrimSpace(parsePrefix(text).body) != ""
}

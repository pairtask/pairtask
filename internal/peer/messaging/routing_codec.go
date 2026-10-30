package messaging

import (
	"encoding/binary"
	"fmt"
	"math"
)

// Version 3's frames: messages to and from agents, their acknowledgments
// by agent, and the agents each side shares.

var (
	routedShape      = shape{FrameRouted, 8}
	agentReadShape   = shape{FrameAgentRead, 2}
	agentStoredShape = shape{FrameAgentReadStored, 2}
	directoryShape   = shape{FrameDirectory, 1}
)

// MaxAgents bounds a recipient list and a directory.
const MaxAgents = 64

// EncodeRouted is a version 3 message: the fields of Message, then From,
// To, and the reply's ID and author.
func EncodeRouted(m Message) ([]byte, error) {
	to, err := encodeList(m.To)
	if err != nil {
		return nil, err
	}
	n := binary.BigEndian.AppendUint64(nil, m.N)
	return encode(FrameRouted, []byte(m.ID), n, []byte(stamp(m.CreatedAt)), []byte(m.Text),
		[]byte(m.From), to, []byte(m.ReplyTo.ID), []byte{m.ReplyTo.Author})
}

func DecodeRouted(frame []byte) (Message, error) {
	f, err := decode(frame, routedShape)
	if err != nil {
		return Message{}, err
	}
	m, err := messageFields(f[:4])
	if err != nil {
		return Message{}, err
	}
	if m.To, err = decodeList(f[5]); err != nil {
		return Message{}, err
	}
	m.From = string(f[4])
	m.ReplyTo, err = replyField(f[6], f[7])
	return m, err
}

// replyField is a reply reference as it travels: one author byte, which
// is none exactly when there is no ID.
func replyField(id, author []byte) (ReplyRef, error) {
	if len(author) != 1 {
		return ReplyRef{}, fmt.Errorf("%w: reply author", ErrProtocol)
	}
	r := ReplyRef{ID: string(id), Author: author[0]}
	if r.Author > ReplyByReceiver || (r.Author == ReplyAuthorNone) != (r.ID == "") {
		return ReplyRef{}, fmt.Errorf("%w: reply reference", ErrProtocol)
	}
	return r, nil
}

// AgentRead says a recipient agent acknowledged a routed message.
type AgentRead struct{ ID, Agent string }

func EncodeAgentRead(r AgentRead) ([]byte, error) {
	return encode(FrameAgentRead, []byte(r.ID), []byte(r.Agent))
}

func DecodeAgentRead(frame []byte) (AgentRead, error) { return decodeAgentRead(frame, agentReadShape) }

func EncodeAgentReadStored(r AgentRead) ([]byte, error) {
	return encode(FrameAgentReadStored, []byte(r.ID), []byte(r.Agent))
}

func DecodeAgentReadStored(frame []byte) (AgentRead, error) {
	return decodeAgentRead(frame, agentStoredShape)
}

func decodeAgentRead(frame []byte, want shape) (AgentRead, error) {
	f, err := decode(frame, want)
	if err != nil {
		return AgentRead{}, err
	}
	return AgentRead{ID: string(f[0]), Agent: string(f[1])}, nil
}

// EncodeDirectory is the agents a side shares with the peer.
func EncodeDirectory(agents []string) ([]byte, error) {
	list, err := encodeList(agents)
	if err != nil {
		return nil, err
	}
	return encode(FrameDirectory, list)
}

func DecodeDirectory(frame []byte) ([]string, error) {
	f, err := decode(frame, directoryShape)
	if err != nil {
		return nil, err
	}
	return decodeList(f[0])
}

// encodeList is a count byte and each item with a uint16 length, so an ID
// may hold any byte, as agent IDs may.
func encodeList(items []string) ([]byte, error) {
	if len(items) > MaxAgents {
		return nil, fmt.Errorf("%w: more than %d agents", ErrProtocol, MaxAgents)
	}
	out := []byte{byte(len(items))}
	for _, it := range items {
		if it == "" || len(it) > math.MaxUint16 {
			return nil, fmt.Errorf("%w: list item", ErrProtocol)
		}
		out = binary.BigEndian.AppendUint16(out, uint16(len(it)))
		out = append(out, it...)
	}
	return out, nil
}

func decodeList(b []byte) ([]string, error) {
	if len(b) == 0 || int(b[0]) > MaxAgents {
		return nil, fmt.Errorf("%w: list", ErrProtocol)
	}
	count, rest := int(b[0]), b[1:]
	items := make([]string, 0, count)
	for range count {
		item, more, err := nextField(rest)
		if err != nil || len(item) == 0 {
			return nil, fmt.Errorf("%w: list item", ErrProtocol)
		}
		items, rest = append(items, string(item)), more
	}
	if len(rest) != 0 {
		return nil, fmt.Errorf("%w: list trailing bytes", ErrProtocol)
	}
	return items, nil
}

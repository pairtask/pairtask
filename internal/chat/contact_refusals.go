package chat

import (
	"strings"

	"github.com/pairtask/pairtask/internal/protocol"
)

// A paired workspace's review request for an agent here that it may not ask
// is refused, and the refusal is kept for this side's operator to see. It
// grants nothing and can't be approved: allowing the agent only lets the
// workspace ask again, with a new request.

// refusedHere: a request from the contact that this side refused.
func refusedHere(v protocol.ContactMessageView) bool {
	return v.Direction == "in" && v.State == "refused"
}

// unreadHere: a received message, or a refusal, not yet shown here.
func unreadHere(v protocol.ContactMessageView) bool {
	return v.State == "received" || refusedHere(v) && v.ReadAt == nil
}

// refusalReasons say why a request was refused here.
var refusalReasons = map[string]string{"not_shared": "not shared here"}

func refusalWords(reason string) string {
	if words, ok := refusalReasons[reason]; ok {
		return words
	}
	return safe(reason)
}

// refusalGuidance says why, in full whatever the width (the head may be cut),
// and what allowing the agent would do: future requests only, and the
// requester must send a new one.
func refusalGuidance(v protocol.ContactMessageView) string {
	agent := commandOperand(v.To)
	return "This request was refused: " + refusalWords(v.RefusalReason) + ". " +
		"/pair reviews " + agent + " ask lets this workspace ask in future; it has to send a new request."
}

// commandOperand is the one recipient as a word safe to paste into a
// command, or AGENT when it isn't one: never a flag, never several words.
func commandOperand(to []string) string {
	if len(to) != 1 || !mentionID.MatchString(to[0]) || strings.HasPrefix(to[0], "-") {
		return "AGENT"
	}
	return to[0]
}

package messaging

import (
	"regexp"
	"strconv"
	"time"
)

// Version 11 carries claims on a paired message back to its sender (#419):
// an agent that received a message says it took a task of it, let it go,
// or finished it, and the workspace that sent the message shows that beside
// its own copy. A claim coordinates; it authorizes nothing on either side.
// Notices travel in a phase of their own, after consent outcomes: each side
// sends those it owes, each answered before the next.

// ClaimVersion is the first version that carries claim notices.
const ClaimVersion byte = 11

// Version 11 only.
const (
	FrameClaimNotice byte = 31 // where a claim on one of the receiver's messages stands
	FrameClaimReply  byte = 32 // the answer to a ClaimNotice: kept, or why it never will be
)

// Claim states a notice carries.
const (
	ClaimActive   = "active"
	ClaimReleased = "released"
	ClaimDone     = "done"
)

// Why a notice is dropped for good. The claiming side settles it and never
// sends it again.
const (
	ClaimUnknownMessage = "unknown_message" // not a message the receiver sent this contact in this pairing
	ClaimNotRecipient   = "not_recipient"   // the agent wasn't one of that message's recipients
	ClaimStale          = "stale"           // a newer state of the same claim is already held
	ClaimConflict       = "conflict"        // the same sequence number with another content
)

// ClaimNotice is the state of one claim: on the message MessageID (its ID
// as its sender, the receiver of the notice, knows it), the Task taken by
// Agent in Role. Seq counts a claim's states from 1 on the claiming side;
// the holder keeps the highest it has seen.
type ClaimNotice struct {
	MessageID, Agent, Role, Task, State string
	Seq                                 uint64
	At                                  time.Time
}

// ClaimReply answers a notice, naming all of what it answers: kept when
// Reason is empty, else dropped for that reason.
type ClaimReply struct {
	MessageID, Agent, Role, Task string
	Seq                          uint64
	Reason                       string
}

var (
	claimTask    = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)
	claimRoles   = map[string]bool{"owner": true, "reviewer": true}
	claimStates  = map[string]bool{ClaimActive: true, ClaimReleased: true, ClaimDone: true}
	claimReasons = map[string]bool{"": true, ClaimUnknownMessage: true, ClaimNotRecipient: true, ClaimStale: true, ClaimConflict: true}
)

// claimSubject: the message, agent, role and task a notice or reply names.
func claimSubject(id, agent, role, task string) bool {
	if !ValidID(id) || agent == "" || len(agent) > AgentIDBytes {
		return false
	}
	return claimRoles[role] && claimTask.MatchString(task)
}

func validClaimNotice(n ClaimNotice) bool {
	if !claimSubject(n.MessageID, n.Agent, n.Role, n.Task) {
		return false
	}
	return claimStates[n.State] && n.Seq > 0
}

func validClaimReply(r ClaimReply) bool {
	if !claimSubject(r.MessageID, r.Agent, r.Role, r.Task) {
		return false
	}
	return r.Seq > 0 && claimReasons[r.Reason]
}

// claimSeq reads a sequence number as sent; 0 for anything else, which
// no valid notice carries.
func claimSeq(text string) uint64 {
	seq, err := strconv.ParseUint(text, 10, 64)
	if err != nil || strconv.FormatUint(seq, 10) != text {
		return 0
	}
	return seq
}

// claimTime reads a notice's time as sent; the zero time for anything
// that doesn't read back exactly.
func claimTime(text string) time.Time {
	at, err := time.Parse(time.RFC3339Nano, text)
	if err != nil || stamp(at) != text {
		return time.Time{}
	}
	return at.UTC()
}

var (
	claimNoticeCodec = fieldCodec[ClaimNotice]{shape{FrameClaimNotice, 7}, "claim notice",
		func(n ClaimNotice) []string {
			return []string{n.MessageID, n.Agent, n.Role, n.Task, n.State, strconv.FormatUint(n.Seq, 10), stamp(n.At)}
		},
		func(f []string) ClaimNotice {
			return ClaimNotice{MessageID: f[0], Agent: f[1], Role: f[2], Task: f[3], State: f[4], Seq: claimSeq(f[5]), At: claimTime(f[6])}
		},
		func(n ClaimNotice) bool { return validClaimNotice(n) && !n.At.IsZero() }}
	claimReplyCodec = fieldCodec[ClaimReply]{shape{FrameClaimReply, 6}, "claim reply",
		func(r ClaimReply) []string {
			return []string{r.MessageID, r.Agent, r.Role, r.Task, strconv.FormatUint(r.Seq, 10), r.Reason}
		},
		func(f []string) ClaimReply {
			return ClaimReply{MessageID: f[0], Agent: f[1], Role: f[2], Task: f[3], Seq: claimSeq(f[4]), Reason: f[5]}
		},
		validClaimReply}
)

func EncodeClaimNotice(n ClaimNotice) ([]byte, error) { return claimNoticeCodec.encode(n) }

func DecodeClaimNotice(frame []byte) (ClaimNotice, error) { return claimNoticeCodec.decode(frame) }

func EncodeClaimReply(r ClaimReply) ([]byte, error) { return claimReplyCodec.encode(r) }

func DecodeClaimReply(frame []byte) (ClaimReply, error) { return claimReplyCodec.decode(frame) }

// reply answers n: kept, or dropped for reason.
func (n ClaimNotice) reply(reason string) ClaimReply {
	return ClaimReply{MessageID: n.MessageID, Agent: n.Agent, Role: n.Role, Task: n.Task, Seq: n.Seq, Reason: reason}
}

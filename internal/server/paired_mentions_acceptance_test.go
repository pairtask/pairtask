//go:build linux

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

type pairedMentionsFixture struct {
	*redeemOperatorFixture
	owner *sessionProbe
	key   string
}

func pairedMentionsServer(t *testing.T) *pairedMentionsFixture {
	t.Helper()
	return pairedMentionsServerSeeded(t, nil)
}

func pairedMentionsServerSeeded(t *testing.T, seed func(*redeemOperatorFixture)) *pairedMentionsFixture {
	t.Helper()
	f := newRedeemOperator(t, redeemConfig(), "")
	id := messageIdentity(t)
	messageConfirm(f, id, 1)
	if seed != nil {
		seed(f)
	}
	owner := sessionOpen(f.operatorFixture)
	owner.snapshot()
	return &pairedMentionsFixture{f, owner, id.Fingerprint()}
}

func pairedMentionsRequest(key, id string, here ...string) map[string]any {
	return map[string]any{"contact_op": protocol.ContactOpSend, "contact_key": key, "message_id": id,
		"contact_text": "shared question", "contact_here": here}
}

func pairedMentionsSend(t *testing.T, f *pairedMentionsFixture, request map[string]any) sessionWire {
	t.Helper()
	r, _ := f.owner.request(protocol.TypeOperatorContact, request)
	operatorEqual(t, "paired operator send admitted", r.text("type"), protocol.TypeOperatorContactResult)
	return r
}

type pairedMentionsAudience struct {
	Operator bool     `json:"operator"`
	Agents   []string `json:"agents"`
}

type pairedMentionsContext struct {
	Kind         string                 `json:"kind"`
	Contact      string                 `json:"contact"`
	Root         string                 `json:"root"`
	PeerAudience pairedMentionsAudience `json:"peer_audience"`
}

func pairedMentionsNotice(t *testing.T, p *operatorPeer, contact string, audience pairedMentionsAudience) (protocol.Message, pairedMentionsContext) {
	t.Helper()
	notice := p.read()
	operatorEqual(t, "home delivery is a message", notice.Type, protocol.TypeMsg)
	operatorEqual(t, "only our operator creates local authority", notice.Origin, protocol.OriginOperator)
	operatorEqual(t, "home delivery is durable", notice.Durable, true)
	operatorEqual(t, "home delivery requires acknowledgment", notice.AckRequested, true)
	if notice.Remote != nil {
		t.Fatal("operator-origin paired context misuses remote provenance")
	}
	if notice.Assignment == nil {
		t.Fatal("tagged home delivery has no reply assignment")
	}
	operatorEqual(t, "assigned task", notice.Assignment.Task, "reply")
	operatorEqual(t, "new assignment state", notice.Assignment.State, "assigned")
	data, err := json.Marshal(notice)
	operatorNoError(t, err)
	var fields sessionWire
	operatorNoError(t, json.Unmarshal(data, &fields))
	conversation := sessionDecode[pairedMentionsContext](t, fields, "conversation")
	operatorEqual(t, "paired conversation kind", conversation.Kind, "contact")
	operatorEqual(t, "canonical contact in notice", conversation.Contact, contact)
	operatorEqual(t, "root binding is present", conversation.Root != "", true)
	if !reflect.DeepEqual(conversation.PeerAudience, audience) {
		t.Fatalf("notice audience=%+v, want %+v", conversation.PeerAudience, audience)
	}
	return notice, conversation
}

func pairedMentionsRows(t *testing.T, f *pairedMentionsFixture) []messaging.Row {
	t.Helper()
	rows, _, err := f.st.PeerMessages().Page(context.Background(), f.key, 0, 64)
	operatorNoError(t, err)
	return rows
}

func pairedMentionsCounts(t *testing.T, f *pairedMentionsFixture) map[string]int {
	t.Helper()
	counts := map[string]int{}
	for _, table := range []string{"messages", "durable_messages", "durable_inbox", "operator_requests", "operator_contact_requests", "assignments", "claims", "peer_messages", "paired_threads", "paired_reply_outcomes"} {
		var present int
		operatorNoError(t, f.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&present))
		if present > 0 {
			counts[table] = f.count(table)
		}
	}
	return counts
}

func TestPairedMentionsAcceptanceHomeAssignmentsFreezeOriginalPeerAudience(t *testing.T) {
	for _, audience := range []pairedMentionsAudience{{Operator: true}, {Agents: []string{"remote-a", "remote-z"}}} {
		t.Run(map[bool]string{true: "operator", false: "agents"}[audience.Operator], func(t *testing.T) {
			f := pairedMentionsServer(t)
			a, b := f.peer("home-a", protocol.Version), f.peer("home-b", protocol.Version)
			request := pairedMentionsRequest(f.key, "paired-root", "home-b", "home-a")
			if len(audience.Agents) > 0 {
				request["contact_to"] = []string{"remote-z", "remote-a"}
			}
			pairedMentionsSend(t, f, request)
			operatorEqual(t, "paired send creates both home assignments", f.count("assignments"), 2)
			_, first := pairedMentionsNotice(t, a, f.key, audience)
			_, second := pairedMentionsNotice(t, b, f.key, audience)
			operatorEqual(t, "assignees share canonical root", first.Root, second.Root)
			var count int
			operatorNoError(t, f.db.QueryRow(`SELECT COUNT(*) FROM assignments WHERE operator_message_id=? AND task='reply' AND state='assigned'`, first.Root).Scan(&count))
			operatorEqual(t, "one assignment per home agent", count, 2)
			rows := pairedMentionsRows(t, f)
			operatorEqual(t, "one peer copy despite two home tags", len(rows), 1)
			operatorEqual(t, "peer ID is the original operation ID", rows[0].ID, "paired-root")
			if !slices.Equal(rows[0].To, audience.Agents) {
				t.Fatalf("peer recipients changed: %q", rows[0].To)
			}
		})
	}
}

func TestPairedMentionsAcceptanceSecondAssignmentFailureRollsBackEveryDestination(t *testing.T) {
	f := pairedMentionsServer(t)
	f.peer("home-a", protocol.Version)
	f.peer("home-b", protocol.Version)
	before := pairedMentionsCounts(t, f)
	_, err := f.db.Exec(`CREATE TRIGGER fail_paired_assignment BEFORE INSERT ON assignments WHEN NEW.agent_id='home-b' BEGIN SELECT RAISE(ABORT,'second home assignment failure'); END`)
	operatorNoError(t, err)
	request := pairedMentionsRequest(f.key, "atomic-paired", "home-a", "home-b")
	r, _ := f.owner.request(protocol.TypeOperatorContact, request)
	operatorEqual(t, "transaction failure is explicit", r.text("code"), protocol.ErrInternal)
	if after := pairedMentionsCounts(t, f); !reflect.DeepEqual(after, before) {
		t.Fatalf("partial paired delivery escaped rollback: before=%v after=%v", before, after)
	}
	_, err = f.db.Exec(`DROP TRIGGER fail_paired_assignment`)
	operatorNoError(t, err)
	pairedMentionsSend(t, f, request)
	operatorEqual(t, "successful retry has one peer copy", len(pairedMentionsRows(t, f)), 1)
}

func TestPairedMentionsAcceptanceHumanAttentionHasNoClaimAndKeepsPeerCopy(t *testing.T) {
	f := pairedMentionsServer(t)
	request := pairedMentionsRequest(f.key, "human-root")
	request["contact_operator_here"] = true
	pairedMentionsSend(t, f, request)
	page := inboxReadPage(f.operatorFixture, 0, 0, 16)
	operatorEqual(t, "human gets one waiting item", len(page.Messages), 1)
	operatorEqual(t, "human waiting item needs ack", page.Messages[0].AckRequested, true)
	operatorEqual(t, "human tag creates no assignment", f.count("assignments"), 0)
	operatorEqual(t, "human tag creates no claim", f.count("claims"), 0)
	operatorEqual(t, "local human tag does not suppress peer copy", len(pairedMentionsRows(t, f)), 1)
}

func pairedMentionsReplyResult(t *testing.T, p *operatorPeer, id, parent string) {
	t.Helper()
	p.write(protocol.Message{Type: protocol.TypeMsg, MessageID: id, To: protocol.OperatorID, InReplyTo: parent,
		Payload: json.RawMessage(`{"text":"home answer"}`), Durable: true, AckRequested: true})
	ack := p.read()
	operatorEqual(t, "home reply accepted locally", ack.Stage, protocol.StageAccepted)
	data, err := json.Marshal(ack)
	operatorNoError(t, err)
	var fields sessionWire
	operatorNoError(t, json.Unmarshal(data, &fields))
	var outcome struct {
		State  string `json:"state"`
		Reason string `json:"reason"`
	}
	outcome = sessionDecode[struct {
		State  string `json:"state"`
		Reason string `json:"reason"`
	}](t, fields, "peer_delivery")
	operatorEqual(t, "assignment authorizes correlated peer reply", outcome.State, "queued")
}

func TestPairedMentionsAcceptanceAssignedReplyUsesCanonicalRootAndOriginalAudience(t *testing.T) {
	for _, useRoot := range []bool{false, true} {
		t.Run(map[bool]string{false: "delivery-id", true: "root-id"}[useRoot], func(t *testing.T) {
			f := pairedMentionsServer(t)
			home := f.peer("home-a", protocol.Version) // deliberately not shared
			request := pairedMentionsRequest(f.key, "root-peer-id", "home-a")
			request["contact_to"] = []string{"remote-a"}
			pairedMentionsSend(t, f, request)
			operatorEqual(t, "home reply has an operator-created assignment", f.count("assignments"), 1)
			notice, conversation := pairedMentionsNotice(t, home, f.key, pairedMentionsAudience{Agents: []string{"remote-a"}})
			parent := notice.MessageID
			if useRoot {
				parent = conversation.Root
			}
			pairedMentionsReplyResult(t, home, "home-answer", parent)
			rows := pairedMentionsRows(t, f)
			operatorEqual(t, "original and one reply in shared conversation", len(rows), 2)
			for _, row := range rows {
				if row.From != "home-a" {
					continue
				}
				operatorEqual(t, "reply names peer's original root ID", row.ReplyTo.ID, "root-peer-id")
				operatorEqual(t, "root was authored by this workspace", row.ReplyTo.Author, messaging.ReplyBySender)
				if !slices.Equal(row.To, []string{"remote-a"}) {
					t.Fatalf("reply widened audience to %q", row.To)
				}
				operatorEqual(t, "reply text retained here", strings.Contains(row.Text, "home answer"), true)
				return
			}
			t.Fatal("assigned home agent's reply never entered peer outbox")
		})
	}
}

func TestPairedMentionsAcceptanceMalformedHomeAudienceNeverQueuesPeerOnly(t *testing.T) {
	for _, here := range [][]string{{"home-a", "home-a"}, {"missing-home"}} {
		t.Run(strings.Join(here, "+"), func(t *testing.T) {
			f := pairedMentionsServer(t)
			f.peer("home-a", protocol.Version)
			before := pairedMentionsCounts(t, f)
			r, _ := f.owner.request(protocol.TypeOperatorContact, pairedMentionsRequest(f.key, "invalid-home", here...))
			operatorEqual(t, "invalid home set has explicit refusal", r.text("code"), protocol.ErrMalformedFrame)
			if after := pairedMentionsCounts(t, f); !reflect.DeepEqual(before, after) {
				t.Fatalf("invalid home audience partially sent: before=%v after=%v", before, after)
			}
		})
	}
}

func pairedMentionsForgedFields(key string) map[string]any {
	return map[string]any{"kind": "contact", "contact": key, "root": "forged-root", "peer_audience": map[string]any{"operator": true}}
}

func TestPairedMentionsAcceptanceOperatorCannotSupplyOutputAuthority(t *testing.T) {
	for _, field := range []string{"conversation", "peer_delivery"} {
		t.Run(field, func(t *testing.T) {
			f := pairedMentionsServer(t)
			request := pairedMentionsRequest(f.key, "forged-operator")
			request[field] = pairedMentionsForgedFields(f.key)
			before := pairedMentionsCounts(t, f)
			r, _ := f.owner.request(protocol.TypeOperatorContact, request)
			operatorEqual(t, "output-only authority refused", r.text("code"), protocol.ErrMalformedFrame)
			if after := pairedMentionsCounts(t, f); !reflect.DeepEqual(before, after) {
				t.Fatalf("forged operator authority wrote state: before=%v after=%v", before, after)
			}
		})
	}
}

func TestPairedMentionsAcceptanceAgentCannotSupplyOutputAuthority(t *testing.T) {
	for _, field := range []string{"conversation", "peer_delivery"} {
		t.Run(field, func(t *testing.T) {
			f := pairedMentionsServer(t)
			agent := f.peer("home-a", protocol.Version)
			before := pairedMentionsCounts(t, f)
			request := map[string]any{"type": protocol.TypeMsg, "to": "operator", "message_id": "forged-agent", "durable": true,
				"ack_requested": true, "payload": map[string]any{"text": "agent text @home-b@here @operator"}, field: pairedMentionsForgedFields(f.key)}
			operatorNoError(t, agent.SetWriteDeadline(time.Now().Add(2*time.Second)))
			operatorNoError(t, json.NewEncoder(agent).Encode(request)) // retain unknown fields on the old protocol
			response := agent.read()
			operatorEqual(t, "agent cannot mint operator-created binding", response.Code, protocol.ErrMalformedFrame)
			if after := pairedMentionsCounts(t, f); !reflect.DeepEqual(before, after) {
				t.Fatalf("forged agent authority wrote state: before=%v after=%v", before, after)
			}
		})
	}
}

func TestPairedMentionsAcceptanceLiveReplyAppearsInBothPairedTabs(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(map[bool]string{false: "inviter-home", true: "joiner-home"}[reverse], func(t *testing.T) {
			pair := newInstantPair(t)
			sides := pair.sides()
			if reverse {
				sides[0], sides[1] = sides[1], sides[0]
			}
			homeSide, peerSide := sides[0], sides[1]
			home := homeSide.f.peer("home-a", protocol.Version) // assignment is the sole sharing exception
			remote := peerSide.f.peer("remote-a", protocol.Version)
			peerSide.shareRemoteAgents(t, "remote-a")
			owner := sessionOpen(homeSide.f.operatorFixture)
			owner.snapshot()
			f := &pairedMentionsFixture{homeSide.f, owner, homeSide.key}
			request := pairedMentionsRequest(f.key, "live-paired-root", "home-a")
			request["contact_to"] = []string{"remote-a"}
			pairedMentionsSend(t, f, request)
			operatorEqual(t, "live send created local authority atomically", f.count("assignments"), 1)
			notice, _ := pairedMentionsNotice(t, home, f.key, pairedMentionsAudience{Agents: []string{"remote-a"}})
			original := remote.read()
			operatorEqual(t, "original line reaches peer agent once", original.Origin, protocol.OriginRemote)
			operatorEqual(t, "original copy retains operator attribution", original.From, protocol.OperatorID)
			pairedMentionsReplyResult(t, home, "live-home-answer", notice.MessageID)
			for _, side := range sides {
				view := side.awaitRemoteView(t, func(m protocol.ContactMessageView) bool { return m.ID == "live-home-answer" })
				operatorEqual(t, "both tabs retain home agent attribution", view.From, "home-a")
				operatorEqual(t, "both tabs retain answer text", view.Text, "home answer")
				operatorEqual(t, "both tabs correlate original peer root", view.ReplyID, "live-paired-root")
			}
			reply := remote.read()
			operatorEqual(t, "frozen peer agent also receives home answer", reply.Origin, protocol.OriginRemote)
			operatorEqual(t, "peer delivery names replying home agent", reply.From, "home-a")
			pair.assertOneConnection(t)
		})
	}
}

func TestPairedMentionsAcceptanceFullHumanInboxRollsBackHomeAndPeerCopies(t *testing.T) {
	f := pairedMentionsServerSeeded(t, func(base *redeemOperatorFixture) {
		base.peer("home-a", protocol.Version)
		seedInboxMessages(t, base.operatorFixture, store.OperatorInboxMessages)
	})
	before := pairedMentionsCounts(t, f)
	request := pairedMentionsRequest(f.key, "human-capacity", "home-a")
	request["contact_operator_here"] = true
	r, _ := f.owner.request(protocol.TypeOperatorContact, request)
	operatorEqual(t, "full local human inbox refuses whole send", r.text("type"), protocol.TypeError)
	operatorEqual(t, "capacity refusal has a code", r.text("code") != "", true)
	if after := pairedMentionsCounts(t, f); !reflect.DeepEqual(before, after) {
		t.Fatalf("human capacity failure leaked another destination: before=%v after=%v", before, after)
	}
}

func pairedMentionsNoLiveNotice(t *testing.T, agent *operatorPeer) {
	t.Helper()
	operatorNoError(t, agent.SetReadDeadline(time.Now().Add(250*time.Millisecond)))
	var duplicate protocol.Message
	err := protocol.ReadFrame(agent.r, &duplicate)
	if err == nil {
		t.Fatalf("operation replay/conflict sent another live frame: %+v", duplicate)
	}
	var timeout net.Error
	if !errors.As(err, &timeout) || !timeout.Timeout() {
		t.Fatalf("agent connection failed instead of staying quiet: %v", err)
	}
}

func TestPairedMentionsAcceptanceOperatorReplayDoesNotRedeliverLiveNotice(t *testing.T) {
	f := pairedMentionsServer(t)
	home := f.peer("home-a", protocol.Version)
	request := pairedMentionsRequest(f.key, "live-replayed-operation", "home-a")
	pairedMentionsSend(t, f, request)
	pairedMentionsNotice(t, home, f.key, pairedMentionsAudience{Operator: true})
	before := f.owner.snapshot().Mark
	counts := pairedMentionsCounts(t, f)
	pairedMentionsSend(t, f, request)
	pairedMentionsNoLiveNotice(t, home)
	after := f.owner.snapshot().Mark
	operatorEqual(t, "idempotent send emits no new conversation events", after, before)
	if got := pairedMentionsCounts(t, f); !reflect.DeepEqual(got, counts) {
		t.Fatalf("live replay changed delivery state: before=%v after=%v", counts, got)
	}
}

func TestPairedMentionsAcceptanceAudienceBoundaryFingerprintCannotCollide(t *testing.T) {
	for _, firstHome := range []bool{false, true} {
		t.Run(map[bool]string{false: "peer-to-home", true: "home-to-peer"}[firstHome], func(t *testing.T) {
			f := pairedMentionsServer(t)
			home := f.peer("home-a", protocol.Version)
			local := pairedMentionsRequest(f.key, "audience-collision", "home-a")
			peer := pairedMentionsRequest(f.key, "audience-collision")
			peer["contact_to"] = []string{"here", "home-a"}
			first, changed := peer, local
			if firstHome {
				first, changed = local, peer
			}
			pairedMentionsSend(t, f, first)
			if firstHome {
				pairedMentionsNotice(t, home, f.key, pairedMentionsAudience{Operator: true})
			}
			before := pairedMentionsCounts(t, f)
			mark := f.owner.snapshot().Mark
			response, _ := f.owner.request(protocol.TypeOperatorContact, changed)
			operatorEqual(t, "same ID cannot move across local/peer audience boundary", response.text("code"), protocol.ErrContactRefused)
			if got := pairedMentionsCounts(t, f); !reflect.DeepEqual(got, before) {
				t.Fatalf("fingerprint collision changed state: before=%v after=%v", before, got)
			}
			pairedMentionsNoLiveNotice(t, home)
			operatorEqual(t, "audience conflict emits no conversation change", f.owner.snapshot().Mark, mark)
		})
	}
}

func pairedMentionsLocalReplyRead(t *testing.T, f *pairedMentionsFixture, root string) {
	t.Helper()
	page := inboxReadPage(f.operatorFixture, 0, 0, 32)
	for _, msg := range page.Messages {
		if msg.MessageID != "kept-answer" {
			continue
		}
		operatorEqual(t, "kept reply read retains sender", msg.From, "home-a")
		operatorEqual(t, "kept reply read answers canonical local root", msg.InReplyTo, root)
		var payload struct {
			Text string `json:"text"`
		}
		operatorNoError(t, json.Unmarshal(msg.Payload, &payload))
		operatorEqual(t, "operator read retains accepted body", payload.Text, "home answer")
		return
	}
	t.Fatal("accepted locally-kept reply absent from actual operator read")
}

func pairedMentionsNotSentPage(t *testing.T, f *pairedMentionsFixture, reason string) {
	t.Helper()
	r, _ := f.owner.request(protocol.TypeOperatorContact, map[string]any{"contact_op": protocol.ContactOpList, "contact_key": f.key})
	operatorEqual(t, "kept paired reply can be listed", r.text("type"), protocol.TypeOperatorContactResult)
	page := sessionDecode[protocol.ContactMessagePage](t, r, "contact_page")
	count := 0
	for _, row := range page.Messages {
		if row.ID != "kept-answer" {
			continue
		}
		count++
		operatorEqual(t, "local-only reply state is explicit", row.State, "not_sent")
		operatorEqual(t, "local-only reply reason retained", row.RefusalReason, reason)
		operatorEqual(t, "paired history retains local author", row.From, "home-a")
		operatorEqual(t, "paired history retains accepted answer", row.Text, "home answer")
		operatorEqual(t, "paired history correlates peer root", row.ReplyID, "kept-root")
		operatorEqual(t, "paired history retains reply side", row.ReplySide, protocol.ReplyBySender)
		if !slices.Equal(row.To, []string{"remote-a"}) {
			t.Fatalf("local-only projection changed frozen audience: %q", row.To)
		}
		operatorEqual(t, "local-only projection is not a wire message", row.Seq, uint64(0))
	}
	operatorEqual(t, "one locally-kept reply appears in actual paired read", count, 1)
}

func pairedMentionsHistoryCursors(t *testing.T, f *pairedMentionsFixture) map[string]int64 {
	t.Helper()
	seen := map[string]int64{}
	before := int64(0)
	for range 3 {
		r, _ := f.owner.request(protocol.TypeOperatorContact, map[string]any{"contact_op": protocol.ContactOpList, "contact_key": f.key, "contact_before": before})
		operatorEqual(t, "mixed paired history is readable", r.text("type"), protocol.TypeOperatorContactResult)
		page := sessionDecode[protocol.ContactMessagePage](t, r, "contact_page")
		oldest := int64(0)
		for _, row := range page.Messages {
			if _, duplicate := seen[row.ID]; duplicate {
				t.Fatalf("mixed pagination duplicated %q", row.ID)
			}
			if before > 0 && row.Cursor >= before {
				t.Fatalf("mixed pagination crossed cursor boundary: %d >= %d", row.Cursor, before)
			}
			seen[row.ID] = row.Cursor
			if oldest == 0 || row.Cursor < oldest {
				oldest = row.Cursor
			}
		}
		if !page.More {
			return seen
		}
		if oldest <= 0 {
			t.Fatal("mixed history advertised another page without a cursor")
		}
		before = oldest
	}
	t.Fatal("mixed history pagination never finished")
	return nil
}

func TestPairedMentionsAcceptanceNotSentActualReadsSurviveRestart(t *testing.T) {
	for _, reason := range []string{"not_shared", "paused", "contact_removed"} {
		t.Run(reason, func(t *testing.T) {
			database := filepath.Join(t.TempDir(), "kept-paired.db")
			var contact, root string
			var acceptedSeq uint64
			var cursors map[string]int64
			t.Run("before", func(t *testing.T) {
				base := newRedeemOperator(t, redeemConfig(), database)
				identity := messageIdentity(t)
				messageConfirm(base, identity, 1)
				contact = identity.Fingerprint()
				owner := sessionOpen(base.operatorFixture)
				owner.snapshot()
				f := &pairedMentionsFixture{base, owner, contact}
				home := f.peer("home-a", protocol.Version)
				request := pairedMentionsRequest(contact, "kept-root", "home-a")
				request["contact_to"] = []string{"remote-a"}
				pairedMentionsSend(t, f, request)
				notice, conversation := pairedMentionsNotice(t, home, contact, pairedMentionsAudience{Agents: []string{"remote-a"}})
				root = conversation.Root
				for i := 0; i < 63; i++ {
					_, err := f.st.PeerMessages().Queue(context.Background(), messaging.Outgoing{Contact: contact, ID: fmt.Sprintf("padding-%02d", i), Text: "original peer history", Now: time.Now()})
					operatorNoError(t, err)
				}
				switch reason {
				case "not_shared":
					_, err := f.st.ReleaseClaim(context.Background(), store.ClaimRequest{MessageID: root, Agent: "home-a", Task: "reply", Role: protocol.RoleOwner, At: time.Now()})
					operatorNoError(t, err)
				case "paused":
					_, _ = f.owner.request(protocol.TypeOperatorContact, map[string]any{"contact_op": protocol.ContactOpPause, "contact_key": contact, "message_id": "pause-for-reply"})
				case "contact_removed":
					_, err := f.db.Exec(`DELETE FROM peer_contacts WHERE identity_key=?`, contact)
					operatorNoError(t, err)
				}
				home.write(protocol.Message{Type: protocol.TypeMsg, MessageID: "kept-answer", To: protocol.OperatorID, InReplyTo: notice.MessageID, Payload: json.RawMessage(`{"text":"home answer"}`), Durable: true, AckRequested: true})
				ack := home.read()
				operatorEqual(t, "local-only reply accepted", ack.Stage, protocol.StageAccepted)
				if ack.PeerDelivery == nil {
					t.Fatal("accepted reply lost peer outcome")
				}
				operatorEqual(t, "reply did not leave", ack.PeerDelivery.State, "not_sent")
				operatorEqual(t, "reply's actual reason", ack.PeerDelivery.Reason, reason)
				acceptedSeq = ack.Seq
				pairedMentionsNotSentPage(t, f, reason)
				pairedMentionsLocalReplyRead(t, f, root)
				cursors = pairedMentionsHistoryCursors(t, f)
				operatorEqual(t, "mixed history retains transport and local-only rows", len(cursors), 65)
				if reason == "not_shared" {
					offer, err := f.st.PeerMessages().Offer(context.Background(), contact, time.Now(), messaging.SessionLimit)
					operatorNoError(t, err)
					operatorEqual(t, "wire-offer exclusion checked against real transport work", len(offer) > 0, true)
					for _, row := range offer {
						if row.ID == "kept-answer" {
							t.Fatal("local-only projection entered peer wire outbox")
						}
					}
				}
			})
			t.Run("after", func(t *testing.T) {
				base := newRedeemOperator(t, redeemConfig(), database)
				owner := sessionOpen(base.operatorFixture)
				owner.snapshot()
				f := &pairedMentionsFixture{base, owner, contact}
				pairedMentionsNotSentPage(t, f, reason)
				if got := pairedMentionsHistoryCursors(t, f); !reflect.DeepEqual(got, cursors) {
					t.Fatalf("restart/replay changed mixed history IDs/cursors: before=%v after=%v", cursors, got)
				}
				pairedMentionsLocalReplyRead(t, f, root)
				home := f.peer("home-a", protocol.Version)
				operatorEqual(t, "unacked original notice replays after restart", home.read().Type, protocol.TypeMsg)
				home.write(protocol.Message{Type: protocol.TypeMsg, MessageID: "kept-answer", To: protocol.OperatorID, InReplyTo: root, Payload: json.RawMessage(`{"text":"home answer"}`), Durable: true, AckRequested: true})
				ack := home.read()
				operatorEqual(t, "kept reply retry remains accepted", ack.Stage, protocol.StageAccepted)
				operatorEqual(t, "kept reply retry has stable local sequence", ack.Seq, acceptedSeq)
				operatorEqual(t, "kept reply retry is explicit replay", ack.Repeated, true)
				pairedMentionsNotSentPage(t, f, reason)
				if got := pairedMentionsHistoryCursors(t, f); !reflect.DeepEqual(got, cursors) {
					t.Fatalf("replay changed mixed history IDs/cursors: before=%v after=%v", cursors, got)
				}
				if reason == "contact_removed" {
					blocked, _ := f.owner.request(protocol.TypeOperatorContact, pairedMentionsRequest(contact, "must-not-send"))
					operatorEqual(t, "read-only removed history grants no send authority", blocked.text("code"), protocol.ErrContactRefused)
					_, found, err := f.st.Contact(context.Background(), contact)
					operatorNoError(t, err)
					operatorEqual(t, "read did not recreate removed contact", found, false)
				}
			})
		})
	}
}

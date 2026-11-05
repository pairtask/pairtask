package chat_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
)

func pairedMentionsChat(t *testing.T, capable bool) (*chatHarness, string) {
	t.Helper()
	return pairedMentionsChatNamed(t, capable, "Office")
}

func pairedMentionsChatNamed(t *testing.T, capable bool, name string) (*chatHarness, string) {
	t.Helper()
	caps := slices.Clone(protocol.SessionCapabilities)
	if capable && !slices.Contains(caps, "paired_mentions") {
		caps = append(caps, "paired_mentions")
	}
	if !capable {
		caps = slices.DeleteFunc(caps, func(c string) bool { return c == "paired_mentions" })
	}
	h := pairingChatCapabilities(t, caps)
	key := "sha256:" + strings.Repeat("a", 64)
	v2Command(h, "/pair list")
	h.reply(pairingChatRequest(h, "list"), contactChatReply(contactChatRow(key, name, true), contactChatRow("sha256:"+strings.Repeat("b", 64), "Other", true)))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	h.input(chatKey(tea.KeyEscape, 0))
	for _, id := range []string{"home-a", "home-b"} {
		revision := h.m.State().Revision + 1
		h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{Kind: protocol.EventPresenceChanged, AgentID: id, SessionID: "session-" + id, State: "connected", ProtocolVersion: protocol.Version, Watermark: protocol.Watermark{Epoch: "epoch", Revision: revision}}}
		h.await(func() bool { return h.m.State().Revision == revision })
	}
	h.input(chatKey(tea.KeyTab, 0))
	read := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "paired fixture selected Office", read.ContactKey, key)
	page := contactMessagesPage(key)
	page.ContactPage.Directory = []string{"remote-a", "home-a"} // same ID can exist on both sides
	h.reply(read, page)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	return h, key
}

func pairedMentionsChatFields(t *testing.T, request protocol.Message) ([]string, bool) {
	t.Helper()
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var fields struct {
		Here         []string `json:"contact_here"`
		OperatorHere bool     `json:"contact_operator_here"`
	}
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	return fields.Here, fields.OperatorHere
}

func pairedMentionsChatAccept(t *testing.T, h *chatHarness, request protocol.Message) {
	t.Helper()
	h.reply(request, protocol.Message{Type: protocol.TypeOperatorContactResult, ContactMessage: &protocol.ContactMessageView{Contact: request.ContactKey, ID: request.MessageID, Direction: "out", State: "queued", Text: request.ContactText}})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	for _, other := range h.requests {
		if other.Type == protocol.TypeOperatorSend {
			t.Fatal("home tags were sent as a separate non-atomic local operation")
		}
	}
}

func TestPairedMentionsChatAcceptanceAudienceShowsBothSidesBeforeEnter(t *testing.T) {
	for _, tc := range []struct {
		name, draft, peer string
	}{{"home-only", "@home-a@here question", "Office's operator"}, {"mixed", "@home-a@here @remote-a question", "remote-a@Office"}} {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := pairedMentionsChat(t, true)
			v2Type(h, tc.draft)
			v3Contains(t, h.m.State().Hint, "home-a@here")
			v3Contains(t, h.m.State().Hint, tc.peer)
			chatEqual(t, "audience preview never sends", h.m.State().InFlight, 0)
			chatEqual(t, "audience preview retains draft", h.m.State().Draft, tc.draft)
		})
	}
}

func TestPairedMentionsChatAcceptanceRepeatedTagsBecomeOneAtomicRequest(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	h, key := pairedMentionsChat(t, true)
	v2Type(h, "@home-a@here @home-a@here @remote-a shared question")
	h.input(chatKey(tea.KeyEnter, 0))
	request := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "paired send operation", request.ContactOp, protocol.ContactOpSend)
	chatEqual(t, "canonical peer contact", request.ContactKey, key)
	here, human := pairedMentionsChatFields(t, request)
	if !slices.Equal(here, []string{"home-a"}) {
		t.Fatalf("home IDs were not deduplicated/canonical: %q", here)
	}
	chatEqual(t, "agent tags do not create a human tag", human, false)
	if !slices.Equal(request.ContactTo, []string{"remote-a"}) {
		t.Fatalf("remote audience widened or contains home IDs: %q", request.ContactTo)
	}
	pairedMentionsChatAccept(t, h, request)
	h.input(tea.WindowSizeMsg{Width: 60, Height: 24})
	v2FrameFits(t, h, 60, 24)
}

func TestPairedMentionsChatAcceptanceBareNameRemainsTheirAgent(t *testing.T) {
	h, _ := pairedMentionsChat(t, true)
	v2Type(h, "@home-a question")
	h.input(chatKey(tea.KeyEnter, 0))
	request := h.take(protocol.TypeOperatorContact)
	here, human := pairedMentionsChatFields(t, request)
	chatEqual(t, "bare name never selects local agent", len(here), 0)
	chatEqual(t, "bare agent name is not human", human, false)
	if !slices.Equal(request.ContactTo, []string{"home-a"}) {
		t.Fatalf("same-named local agent stole peer addressing: %q", request.ContactTo)
	}
	pairedMentionsChatAccept(t, h, request)
}

func TestPairedMentionsChatAcceptanceHumanAttentionKeepsPeerOperatorAudience(t *testing.T) {
	h, _ := pairedMentionsChat(t, true)
	v2Type(h, "@operator @home-a@here shared question")
	h.input(chatKey(tea.KeyEnter, 0))
	request := h.take(protocol.TypeOperatorContact)
	here, human := pairedMentionsChatFields(t, request)
	chatEqual(t, "explicit local human attention", human, true)
	if !slices.Equal(here, []string{"home-a"}) {
		t.Fatalf("human tag replaced tagged home agent: %q", here)
	}
	chatEqual(t, "no remote agent tags means their operator still receives", len(request.ContactTo), 0)
	pairedMentionsChatAccept(t, h, request)
}

func TestPairedMentionsChatAcceptanceHumanOnlyTagQueuesOneAtomicPairedSend(t *testing.T) {
	h, key := pairedMentionsChat(t, true)
	v2Type(h, "@operator human attention without an agent assignment")
	h.input(chatKey(tea.KeyEnter, 0))
	if h.m.State().InFlight == 0 {
		t.Fatal("capable broker held a valid human-only paired tag")
	}
	request := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "human-only tag keeps paired contact", request.ContactKey, key)
	chatEqual(t, "human-only attention uses atomic paired send", request.ContactOp, protocol.ContactOpSend)
	here, human := pairedMentionsChatFields(t, request)
	chatEqual(t, "human-only tag reaches local waiting inbox", human, true)
	chatEqual(t, "human-only tag creates no home agent assignment", len(here), 0)
	chatEqual(t, "human-only tag keeps peer operator copy", len(request.ContactTo), 0)
	chatEqual(t, "tag removed from shared body", request.ContactText, "human attention without an agent assignment")
	pairedMentionsChatAccept(t, h, request)
}

func TestPairedMentionsChatAcceptanceCanonicalTagDedupKeepsBothSidesDistinct(t *testing.T) {
	for _, tc := range []struct {
		name, draft string
		here, peer  []string
	}{
		{"remote-only", "@remote-a @remote-a@Office @remote-a question", nil, []string{"remote-a"}},
		{"same-name-both-sides", "@home-a@here @home-a @home-a@Office @home-a@here question", []string{"home-a"}, []string{"home-a"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, key := pairedMentionsChat(t, true)
			v2Type(h, tc.draft)
			h.input(chatKey(tea.KeyEnter, 0))
			if h.m.State().InFlight == 0 {
				t.Fatal("capable parser rejected repeated canonical tags")
			}
			request := h.take(protocol.TypeOperatorContact)
			chatEqual(t, "canonical tags retain paired destination", request.ContactKey, key)
			here, human := pairedMentionsChatFields(t, request)
			if !slices.Equal(here, tc.here) {
				t.Fatalf("home canonical dedup crossed sides: got%q want%q", here, tc.here)
			}
			if !slices.Equal(request.ContactTo, tc.peer) {
				t.Fatalf("peer canonical dedup crossed sides: got%q want%q", request.ContactTo, tc.peer)
			}
			chatEqual(t, "agent-only tags create no human attention", human, false)
			chatEqual(t, "canonical mention forms stripped from body", request.ContactText, "question")
			pairedMentionsChatAccept(t, h, request)
		})
	}
}

func TestPairedMentionsChatAcceptanceUnsupportedBrokerKeepsWholeDraftAndWritesNothing(t *testing.T) {
	for _, draft := range []string{"@home-a@here question", "@operator question", "@remote-a @home-a@here question"} {
		t.Run(draft, func(t *testing.T) {
			h, _ := pairedMentionsChat(t, false)
			v2Type(h, draft)
			h.input(chatKey(tea.KeyEnter, 0))
			chatEqual(t, "unsupported local tags never send a partial peer copy", h.m.State().InFlight, 0)
			chatEqual(t, "unsupported local tags keep exact draft", h.m.State().Draft, draft)
			view := ansi.Strip(h.m.View().Content)
			if !strings.Contains(view, "unsupported") && !strings.Contains(view, "upgrade") {
				t.Fatal("unsupported local tags lack an actionable explanation")
			}
		})
	}
}

func TestPairedMentionsChatAcceptanceUnknownHomeOrDifferentHumanContactNeverPartiallySends(t *testing.T) {
	for _, draft := range []string{"@missing@here question", "@operator@Other question"} {
		t.Run(draft, func(t *testing.T) {
			h, _ := pairedMentionsChat(t, true)
			v2Type(h, draft)
			h.input(chatKey(tea.KeyEnter, 0))
			chatEqual(t, "invalid local/cross-contact target writes nothing", h.m.State().InFlight, 0)
			chatEqual(t, "invalid local/cross-contact target retains draft", h.m.State().Draft, draft)
		})
	}
}

func TestPairedMentionsChatAcceptanceHomeCompletionRequiresAnotherEnterToSend(t *testing.T) {
	h, key := pairedMentionsChat(t, true)
	v2Type(h, "@home-b")
	picker := h.m.State().Picker
	if picker == nil {
		t.Fatal("connected home agent is absent from paired mention completion")
	}
	want := "home-b@here"
	index := slices.Index(picker.Items, want)
	if index < 0 {
		t.Fatalf("home completion lacks explicit local scope: %v", picker.Items)
	}
	for range index {
		h.input(chatKey(tea.KeyDown, 0))
	}
	h.input(chatKey(tea.KeyEnter, 0))
	chatEqual(t, "completion does not send", h.m.State().InFlight, 0)
	chatEqual(t, "completion inserts unambiguous local mention", h.m.State().Draft, "@home-b@here ")
	v2Type(h, "question")
	h.input(chatKey(tea.KeyEnter, 0))
	request := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "completion retains paired destination", request.ContactKey, key)
	here, human := pairedMentionsChatFields(t, request)
	if !slices.Equal(here, []string{"home-b"}) {
		t.Fatalf("completion resolved home tag incorrectly: %q", here)
	}
	chatEqual(t, "home completion does not tag human", human, false)
	chatEqual(t, "home-only completion keeps peer operator", len(request.ContactTo), 0)
	pairedMentionsChatAccept(t, h, request)
}

func TestPairedMentionsChatAcceptanceExplicitCurrentWorkspaceHumanTargetsPeerOperator(t *testing.T) {
	h, _ := pairedMentionsChat(t, true)
	v2Type(h, "@operator@Office question")
	h.input(chatKey(tea.KeyEnter, 0))
	request := h.take(protocol.TypeOperatorContact)
	here, human := pairedMentionsChatFields(t, request)
	chatEqual(t, "qualified peer operator creates no local human attention", human, false)
	chatEqual(t, "human qualifier creates no agent delivery", len(here), 0)
	chatEqual(t, "peer operator still sees shared line", len(request.ContactTo), 0)
	pairedMentionsChatAccept(t, h, request)
}

func pairedMentionsPeerCompletion(t *testing.T, h *chatHarness, key string) int {
	t.Helper()
	picker := h.m.State().Picker
	if picker == nil {
		t.Fatal("same-named agents on both sides have no scoped completion")
	}
	local, peer := slices.Index(picker.Items, "home-a@here"), -1
	for i, item := range picker.Items {
		if strings.Contains(item, key) {
			peer = i
		}
	}
	if local < 0 || peer < 0 {
		t.Fatalf("both scoped identities must be offered: %v", picker.Items)
	}
	if local == peer {
		t.Fatalf("peer label here collides with local namespace: %v", picker.Items)
	}
	return peer
}

func TestPairedMentionsChatAcceptancePeerNamedHereUsesCanonicalKeyCompletion(t *testing.T) {
	h, key := pairedMentionsChatNamed(t, true, "here")
	v2Type(h, "@home-a@")
	peer := pairedMentionsPeerCompletion(t, h, key)
	for range peer {
		h.input(chatKey(tea.KeyDown, 0))
	}
	h.input(chatKey(tea.KeyEnter, 0))
	v3Contains(t, h.m.State().Draft, key)
	chatEqual(t, "scope completion never sends", h.m.State().InFlight, 0)
	v2Type(h, "peer question")
	h.input(chatKey(tea.KeyEnter, 0))
	request := h.take(protocol.TypeOperatorContact)
	here, human := pairedMentionsChatFields(t, request)
	chatEqual(t, "canonical key targets peer contact", request.ContactKey, key)
	chatEqual(t, "peer named here cannot select home delivery", len(here), 0)
	chatEqual(t, "peer completion does not create human attention", human, false)
	if !slices.Equal(request.ContactTo, []string{"home-a"}) {
		t.Fatalf("canonical peer completion lost recipient: %q", request.ContactTo)
	}
	pairedMentionsChatAccept(t, h, request)
}

func TestPairedMentionsChatAcceptanceMixedPeerOperatorAndAgentNeverNarrowsAudience(t *testing.T) {
	for _, draft := range []string{"@operator@Office @remote-a question", "@remote-a @operator@Office question"} {
		t.Run(draft, func(t *testing.T) {
			h, _ := pairedMentionsChat(t, true)
			v2Type(h, draft)
			h.input(chatKey(tea.KeyEnter, 0))
			chatEqual(t, "mixed remote operator/agent audience writes nothing", h.m.State().InFlight, 0)
			chatEqual(t, "mixed audience preserves exact draft", h.m.State().Draft, draft)
			view := strings.ToLower(ansi.Strip(h.m.View().Content))
			if !strings.Contains(view, "operator") || !strings.Contains(view, "agent") {
				t.Fatal("mixed audience has no explicit explanation")
			}
		})
	}
}

func TestPairedMentionsChatAcceptanceLocallyKeptReplyShowsNotSentReason(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	for _, reason := range []string{"not_shared", "paused"} {
		t.Run(reason, func(t *testing.T) {
			h, key := pairedMentionsChat(t, true)
			revision := h.m.State().Revision + 1
			h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{Kind: protocol.EventContactMessagesChanged, ContactKey: key, Watermark: protocol.Watermark{Epoch: "epoch", Revision: revision}}}
			request := h.take(protocol.TypeOperatorContact)
			chatEqual(t, "local-kept event refreshes paired history", request.ContactOp, protocol.ContactOpList)
			row := protocol.ContactMessageView{ID: "kept-answer", Cursor: 2, Direction: "out", State: "not_sent", RefusalReason: reason, From: "home-a", To: []string{"remote-a"}, ReplyID: "paired-root", ReplySide: protocol.ReplyBySender, Text: "KEPT ANSWER SENTINEL", CreatedAt: time.Now()}
			h.reply(request, contactMessagesPage(key, row))
			h.await(func() bool { return h.m.State().InFlight == 0 })
			view := strings.ReplaceAll(strings.ToLower(ansi.Strip(h.m.View().Content)), "_", " ")
			v3Contains(t, view, "kept answer sentinel")
			v3Contains(t, view, "home-a")
			v3Contains(t, view, "not sent")
			if reason == "not_shared" {
				if !strings.Contains(view, "not shared") && !strings.Contains(view, "isn't shared") {
					t.Fatal("not-sent status does not explain the sharing refusal")
				}
			} else {
				v3Contains(t, view, reason)
			}
			chatEqual(t, "read stays in original paired thread", request.ContactKey, key)
			chatEqual(t, "render never resends local-only answer", h.m.State().InFlight, 0)
		})
	}
}

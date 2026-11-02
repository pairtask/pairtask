package chat_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/pairtask/pairtask/internal/chat"
	"github.com/pairtask/pairtask/internal/protocol"
)

// Typed keys and bracketed paste are intentionally separate: they carry
// different addressing and command semantics in the operator UI.
func v2Type(h *chatHarness, text string) {
	h.t.Helper()
	for _, r := range text {
		if r == '\n' {
			h.input(chatKey('j', tea.ModCtrl))
		} else {
			h.input(tea.KeyPressMsg{Code: r, Text: string(r)})
		}
	}
}

func v2Command(h *chatHarness, command string) {
	h.t.Helper()
	h.input(chatKey('k', tea.ModCtrl))
	v2Type(h, strings.TrimPrefix(command, "/"))
	h.input(chatKey(tea.KeyEnter, 0))
}

func v2Send(h *chatHarness) protocol.Message {
	h.t.Helper()
	h.input(chatKey(tea.KeyEnter, 0))
	if h.m.State().Intent == nil {
		h.t.Fatalf("Enter did not create send intent: %+v", h.m.State())
	}
	return h.take(protocol.TypeOperatorSend)
}

func v2Body(t *testing.T, msg protocol.Message) string {
	t.Helper()
	var body struct{ Text string }
	if err := json.Unmarshal(msg.Payload, &body); err != nil {
		t.Fatal(err)
	}
	return body.Text
}

func v2History(h *chatHarness, messages ...protocol.Message) {
	h.t.Helper()
	inbox, history := h.baseline()
	h.reply(inbox, chatInbox())
	h.reply(history, chatHistory(messages...))
	h.await(func() bool { return h.m.State().InFlight == 0 })
}

func TestChatV2TypedAddressingUsesOnlyLeadingTokens(t *testing.T) {
	for _, tc := range []struct {
		name, draft, to, body string
		mentions              []string
	}{
		{name: "default broadcast", draft: "hello", to: "*", body: "hello"},
		{name: "tag unknown", draft: "@unseen hello", to: "*", body: "@unseen hello", mentions: []string{"unseen"}},
		{name: "explicit broadcast", draft: "@all hello", to: "*", body: "@all hello"},
		{name: "deduplicated public tags", draft: "@z @A @z hello", to: "*", mentions: []string{"A", "z"}, body: "@z @A @z hello"},
		{name: "body mention", draft: "hello @bob", to: "*", body: "hello @bob"},
		{name: "body email", draft: "mail user@example.com", to: "*", body: "mail user@example.com"},
		{name: "newline ends prefix", draft: "@alice\n@bob hello", to: "*", mentions: []string{"alice"}, body: "@alice\n@bob hello"},
		{name: "escaped leading mention", draft: "\\@bob hello", to: "*", body: "@bob hello"},
		{name: "literal slash", draft: "//ack #1", to: "*", body: "/ack #1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newChatHarness(t, chat.Bounds{})
			h.ready()
			v2Type(h, tc.draft)
			req := v2Send(h)
			chatEqual(t, "destination", req.To, tc.to)
			chatEqual(t, "public tags never create private group", len(req.Group), 0)
			if !reflect.DeepEqual(req.Mentions, tc.mentions) {
				t.Fatalf("mentions = %q, want %q", req.Mentions, tc.mentions)
			}
			chatEqual(t, "full payload", v2Body(t, req), tc.body)
		})
	}
}

func TestChatV2InvalidAddressNeverFallsBackToBroadcast(t *testing.T) {
	for _, draft := range []string{"@", "@@ hello", "@a,b hello", "@alice: hello", "@all @bob hello", "@operator hello", "@alice", "@alice   ", "@" + strings.Repeat("x", 129) + " body"} {
		t.Run(draft, func(t *testing.T) {
			h := newChatHarness(t, chat.Bounds{})
			h.ready()
			v2Type(h, draft)
			h.input(chatKey(tea.KeyEscape, 0)) // Dismiss any completion, retaining text.
			h.input(chatKey(tea.KeyEnter, 0))
			chatEqual(t, "invalid scope cannot send", h.m.State().Intent, nil)
			chatEqual(t, "draft retained", h.m.State().Draft, draft)
		})
	}
}

func TestChatV2PasteCannotAddressOrExecuteCommands(t *testing.T) {
	for _, body := range []string{"@alice secret", "/ack #1", "/quit", "//ack #1", "\\@alice literal", "@all @alice\none\ntwo\nthree\nfour\nfive\nsix"} {
		t.Run(body, func(t *testing.T) {
			h := newChatHarness(t, chat.Bounds{})
			h.ready()
			h.input(tea.PasteMsg{Content: body})
			chatEqual(t, "paste does not send", h.m.State().Intent, nil)
			chatEqual(t, "full paste retained", h.m.State().Draft, body)
			if body == "/ack #1" || body == "/quit" {
				h.input(chatKey(tea.KeyEnter, 0))
				chatEqual(t, "first Enter guards command-looking text", h.m.State().Intent, nil)
				chatEqual(t, "guard identifies exact draft", h.m.State().SendGuard, body)
				v3Contains(t, h.m.State().Status, "Enter again")
			}
			req := v2Send(h)
			chatEqual(t, "paste default audience", req.To, protocol.Broadcast)
			chatEqual(t, "paste preserved literally", v2Body(t, req), body)
		})
	}
}

func TestChatV2PasteIntoTypedAddressPreservesExplicitScope(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	h.ready()
	v2Type(h, "@alice ")
	h.input(tea.PasteMsg{Content: "body\n@bob /quit"})
	req := v2Send(h)
	chatEqual(t, "typed tag stays public after paste", req.To, "*")
	if !reflect.DeepEqual(req.Mentions, []string{"alice"}) {
		t.Fatalf("paste changed typed tags: %q", req.Mentions)
	}
	chatEqual(t, "body untouched", v2Body(t, req), "@alice body\n@bob /quit")
}

func TestChatV2LiteralAudienceCommandPreservesDraft(t *testing.T) {
	for _, id := range []string{"all", "@all", "a,b", " a ", "agent\n", "agent\x1b]52;c;x\a"} {
		t.Run(id, func(t *testing.T) {
			h := newChatHarness(t, chat.Bounds{})
			h.ready()
			v2Type(h, "draft body")
			encoded, _ := json.Marshal([]string{id})
			v2Command(h, "/to "+string(encoded))
			chatEqual(t, "command preserves draft", h.m.State().Draft, "draft body")
			req := v2Send(h)
			chatEqual(t, "literal destination", req.To, id)
			chatEqual(t, "no accidental group", len(req.Group), 0)
			chatEqual(t, "body", v2Body(t, req), "draft body")
		})
	}
}

func TestChatV2PastedCommandLineCannotExecute(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	v2History(h, chatIncoming("target", 7))
	v2Type(h, "keep this draft")
	h.input(chatKey('k', tea.ModCtrl))
	h.input(tea.PasteMsg{Content: "ack #7"})
	h.input(chatKey(tea.KeyEnter, 0))
	chatEqual(t, "no pasted acknowledgment", len(h.m.State().QueuedAcks), 0)
	chatEqual(t, "no pasted request", h.m.State().InFlight, 0)
	h.input(chatKey(tea.KeyEscape, 0))
	chatEqual(t, "draft restored", h.m.State().Draft, "keep this draft")
	chatEqual(t, "no pasted send", h.m.State().Intent, nil)
}

func TestChatV2ReplyFreezesLiteralTargetAndRejectsAudienceChanges(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	msg := chatIncoming("literal-target", 7)
	msg.From = "@all"
	v2History(h, msg)
	v2Type(h, "reply body")
	v2Command(h, "/reply #7")
	chatEqual(t, "reply bound", h.m.State().InReplyTo, msg.MessageID)
	for _, command := range []string{`/to ["bob"]`, "/to", "/reply #999"} {
		v2Command(h, command)
		if h.m.State().CommandLine != nil {
			h.input(chatKey(tea.KeyEscape, 0))
		}
		chatEqual(t, "failed command preserves reply", h.m.State().InReplyTo, msg.MessageID)
		chatEqual(t, "failed command preserves body", h.m.State().Draft, "reply body")
	}
	req := v2Send(h)
	chatEqual(t, "literal reply audience", req.To, "@all")
	chatEqual(t, "reply correlation", req.InReplyTo, msg.MessageID)
	chatEqual(t, "queued draft cleared", h.m.State().Draft, "")
	chatEqual(t, "queued reply detached", h.m.State().InReplyTo, "")
	// A late outcome belongs to this intent, never the next draft.
	v2Type(h, "new draft")
	h.reply(req, protocol.Message{Type: protocol.TypeAck, MessageID: req.MessageID, Stage: protocol.StageAccepted, Seq: 8})
	h.await(func() bool { return h.m.State().Intent.State == chat.IntentAccepted })
	chatEqual(t, "late acceptance preserves newer draft", h.m.State().Draft, "new draft")
}

func TestChatConversationReplyCancelPreservesTextAndSelectedScope(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	v2History(h, chatIncoming("target", 7))
	v2Type(h, "body")
	v2Command(h, "/r #7")
	v2Command(h, "/reply cancel")
	chatEqual(t, "reply cleared", h.m.State().InReplyTo, "")
	chatEqual(t, "body retained", h.m.State().Draft, "body")
	req := v2Send(h)
	chatEqual(t, "cancel keeps the selected direct conversation", req.To, "author")
	chatEqual(t, "no stale correlation", req.InReplyTo, "")
}

func TestChatV2ReplyRefusesAlreadyAddressedDraft(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	v2History(h, chatIncoming("target", 7))
	v2Type(h, "@bob body")
	v2Command(h, "/reply #7")
	h.input(chatKey(tea.KeyEscape, 0))
	chatEqual(t, "no implicit reply", h.m.State().InReplyTo, "")
	chatEqual(t, "addressed draft retained", h.m.State().Draft, "@bob body")
	req := v2Send(h)
	chatEqual(t, "original public audience retained", req.To, "*")
	if !reflect.DeepEqual(req.Mentions, []string{"bob"}) {
		t.Fatalf("refused reply changed tags: %q", req.Mentions)
	}
}

func TestChatV2BackslashNewlineDoesNotSend(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	h.ready()
	v2Type(h, "first\\")
	h.input(chatKey(tea.KeyEnter, 0))
	chatEqual(t, "fallback newline does not send", h.m.State().Intent, nil)
	chatEqual(t, "fallback replaces trailing backslash", h.m.State().Draft, "first\n")
	v2Type(h, "second")
	req := v2Send(h)
	chatEqual(t, "multiline body", v2Body(t, req), "first\nsecond")
}

func TestChatV2ClearPreservesAckBindingAndHiddenTargets(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	v2History(h, chatIncoming("hidden-target", 7))
	v2Command(h, "/ack #7")
	chatEqual(t, "ack waits for receipt", len(h.m.State().QueuedAcks), 1)
	v2Command(h, "/clear")
	chatEqual(t, "clear preserves entry", len(h.m.State().Entries), 1)
	chatEqual(t, "clear preserves queued ack", len(h.m.State().QueuedAcks), 1)
	chatEqual(t, "clear preserves exact ack ID", h.m.State().QueuedAcks[0].MessageID, "hidden-target")
	v2Command(h, "/reply #7")
	chatEqual(t, "hidden target still addressable", h.m.State().InReplyTo, "hidden-target")
}

func TestChatV2BroadcastUsesBrokerAcceptedMembership(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	h.ready()
	v2Type(h, "everyone")
	req := v2Send(h)
	chatEqual(t, "broadcast remains dynamic", req.To, protocol.Broadcast)
	chatEqual(t, "no locally expanded group", len(req.Group), 0)
	h.reply(req, protocol.Message{Type: protocol.TypeAck, MessageID: req.MessageID, Stage: protocol.StageAccepted, Seq: 1,
		Recipients: []protocol.Recipient{{AgentID: "new-member", SessionID: "new-session"}}, Excluded: []string{"old-client"}})
	h.await(func() bool { return h.m.State().Intent.State == chat.IntentAccepted })
	chatEqual(t, "accepted member count", len(h.m.State().Intent.Recipients), 1)
	chatEqual(t, "authoritative member", h.m.State().Intent.Recipients[0].AgentID, "new-member")
	chatEqual(t, "authoritative member session", h.m.State().Intent.Recipients[0].SessionID, "new-session")
	chatEqual(t, "authoritative exclusions", reflect.DeepEqual(h.m.State().Intent.Excluded, []string{"old-client"}), true)
}

func v2PickAgent(h *chatHarness, id string) {
	h.t.Helper()
	picker := h.m.State().Picker
	if picker == nil || picker.Kind != "agent" {
		h.t.Fatalf("agent picker not open: %+v", picker)
	}
	index := -1
	for i, item := range picker.Items {
		if item == id {
			index = i
			break
		}
	}
	if index < 0 {
		h.t.Fatalf("picker missing raw ID %q: %+v", id, picker)
	}
	for i := picker.Selected; i < index; i++ {
		h.input(chatKey(tea.KeyDown, 0))
	}
	for i := picker.Selected; i > index; i-- {
		h.input(chatKey(tea.KeyUp, 0))
	}
	h.input(chatKey(tea.KeyEnter, 0))
}

func TestChatV2AgentPickerCompletesWithoutSending(t *testing.T) {
	for _, id := range []string{"alice", "all", "a,b", "agent\n"} {
		t.Run(id, func(t *testing.T) {
			h := newChatHarness(t, chat.Bounds{})
			msg := chatIncoming("known", 1)
			msg.From = id
			v2History(h, msg)
			v2Type(h, "@")
			v2PickAgent(h, id)
			chatEqual(t, "completion does not send", h.m.State().Intent, nil)
			if h.m.State().Picker != nil {
				t.Fatal("picker did not close on completion")
			}
			v2Type(h, " body")
			req := v2Send(h)
			if id == "alice" {
				chatEqual(t, "inline pick addresses publicly", req.To, "*")
				if !reflect.DeepEqual(req.Mentions, []string{id}) {
					t.Fatalf("inline pick lost tag: %q", req.Mentions)
				}
			} else {
				chatEqual(t, "non-inline pick keeps exact private destination", req.To, id)
			}
			chatEqual(t, "pick is not reply", req.InReplyTo, "")
		})
	}
}

func TestChatV2NonInlinePickerRefusesMixedAddress(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	msg := chatIncoming("known", 1)
	msg.From = "a,b"
	v2History(h, msg)
	v2Type(h, "@alice @")
	v2PickAgent(h, "a,b")
	chatEqual(t, "failed pick keeps text", h.m.State().Draft, "@alice @")
	h.input(chatKey(tea.KeyEscape, 0))
	h.input(chatKey(tea.KeyEnter, 0))
	chatEqual(t, "mixed syntax cannot send", h.m.State().Intent, nil)
}

func TestChatV2CommandCompletionAndAmbiguityNeverSend(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	v2History(h, chatIncoming("target", 7))
	v2Type(h, "body")
	h.input(chatKey('k', tea.ModCtrl))
	v2Type(h, "repl")
	h.input(chatKey(tea.KeyTab, 0))
	chatEqual(t, "Tab never runs reply", h.m.State().InReplyTo, "")
	h.input(chatKey(tea.KeyEscape, 0))
	chatEqual(t, "cancel preserves draft", h.m.State().Draft, "body")
	v2Command(h, "/c") // /clear and /copy are both applicable.
	if h.m.State().CommandLine == nil {
		t.Fatal("ambiguous command unexpectedly executed")
	}
	chatEqual(t, "ambiguous command cannot send", h.m.State().Intent, nil)
	h.input(chatKey(tea.KeyEscape, 0))
	chatEqual(t, "Escape closes argument choices", h.m.State().Picker, nil)
	h.input(chatKey(tea.KeyEscape, 0))
	chatEqual(t, "second Escape closes command", h.m.State().CommandLine, nil)
	v2Command(h, "/repl #7")
	chatEqual(t, "unique prefix executes once", h.m.State().InReplyTo, "target")
	chatEqual(t, "unique prefix preserves body", h.m.State().Draft, "body")
}

func TestChatV2ClearingPasteRestoresTypedAddressing(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	h.ready()
	h.input(tea.PasteMsg{Content: "literal"})
	for range len("literal") {
		h.input(chatKey(tea.KeyBackspace, 0))
	}
	chatEqual(t, "draft empty", h.m.State().Draft, "")
	v2Type(h, "@alice body")
	req := v2Send(h)
	chatEqual(t, "paste provenance resets to public tagging", req.To, "*")
	if !reflect.DeepEqual(req.Mentions, []string{"alice"}) {
		t.Fatalf("cleared paste still ignored typed tag: %q", req.Mentions)
	}
}

func TestChatV2OfflineReplySubmitPreservesMetadata(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	v2History(h, chatIncoming("target", 7))
	v2Type(h, "body")
	v2Command(h, "/reply #7")
	h.wire.Close()
	h.await(func() bool { return h.m.State().Connection == chat.Reconnecting })
	h.input(chatKey(tea.KeyEnter, 0))
	chatEqual(t, "offline no send", h.m.State().Intent, nil)
	chatEqual(t, "offline keeps body", h.m.State().Draft, "body")
	chatEqual(t, "offline keeps correlation", h.m.State().InReplyTo, "target")
	if got := h.m.State().Audience; got.Source != "reply" || !reflect.DeepEqual(got.IDs, []string{"author"}) {
		t.Fatalf("offline lost reply audience: %+v", got)
	}
}

func TestChatV2MalformedCommandsNeverPerformPartialAction(t *testing.T) {
	for _, command := range []string{"/quit extra", "/clear extra", "/help extra", "/ack #7 extra", "/reply #7 extra", "/to alice", "/to [\"alice\"] trailing"} {
		t.Run(command, func(t *testing.T) {
			h := newChatHarness(t, chat.Bounds{})
			v2History(h, chatIncoming("target", 7))
			v2Type(h, "draft")
			v2Command(h, command)
			state := h.m.State()
			chatEqual(t, "malformed command stays connected", state.Connection, chat.Connected)
			chatEqual(t, "malformed command preserves draft", state.Draft, "draft")
			chatEqual(t, "malformed command does not clear", state.Cutoff, uint64(0))
			chatEqual(t, "malformed command does not open help", state.Help, false)
			chatEqual(t, "malformed command does not acknowledge", len(state.QueuedAcks), 0)
			chatEqual(t, "malformed command does not reply", state.InReplyTo, "")
			chatEqual(t, "malformed command does not change audience", state.Audience.Source, "everyone")
			errorText := state.Status
			if state.CommandLine != nil {
				errorText += state.CommandLine.Error
			}
			if errorText == "" {
				t.Fatal("malformed command did not report an error")
			}
		})
	}
}

func TestChatV2OldEpochSequenceCannotBecomeNewReplyOrAck(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	v2History(h, chatIncoming("old-target", 7))
	h.event(protocol.EventResyncRequired, "next-epoch", 1)
	snapshot := h.take(protocol.TypeOperatorSnapshot)
	h.reply(snapshot, protocol.Message{Type: protocol.TypeOperatorSnapshot, Snapshot: &protocol.OperatorSnapshot{
		Watermark: protocol.Watermark{Epoch: "next-epoch", Revision: 1},
	}})
	h.await(func() bool { return h.m.State().Baseline && h.m.State().Epoch == "next-epoch" })
	v2Type(h, "draft")
	v2Command(h, "/reply #7")
	chatEqual(t, "old epoch cannot bind reply", h.m.State().InReplyTo, "")
	h.input(chatKey(tea.KeyEscape, 0))
	v2Command(h, "/ack #7")
	chatEqual(t, "old epoch cannot queue acknowledgment", len(h.m.State().QueuedAcks), 0)
	chatEqual(t, "old epoch refusal preserves draft", h.m.State().Draft, "draft")
}

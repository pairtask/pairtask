package hub

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/notify"
	"github.com/pairtask/pairtask/internal/protocol"
)

func decisionHub(t *testing.T, notifier *Notifier) (*Hub, context.Context, *Client) {
	t.Helper()
	h := newTestHub(t)
	h.SetNotifier(notifier)
	ctx, cancel := context.WithCancel(context.Background())
	go h.Run(ctx)
	t.Cleanup(func() {
		cancel()
		select {
		case <-h.Done():
		case <-time.After(5 * time.Second):
			t.Error("decision notifier prevented hub shutdown")
		}
	})
	agent := newClient("codex-1")
	agent.ProtocolVersion = protocol.DecisionVersion
	if !h.Register(ctx, agent) {
		t.Fatal("agent registration failed")
	}
	recv(t, agent) // welcome
	return h, ctx, agent
}

func requestDecision(t *testing.T, h *Hub, ctx context.Context, agent *Client, id, key, summary string) protocol.Decision {
	t.Helper()
	request := protocol.Message{Type: protocol.TypeDecisionRequest, RequestID: id,
		DecisionKey: key, DecisionSummary: summary}
	if !h.Submit(ctx, Inbound{From: agent, Msg: request}) {
		t.Fatal("decision request not submitted")
	}
	for i := 0; i < 4; i++ {
		frame := recv(t, agent)
		if frame.Type == protocol.TypeDecisionResult && frame.RequestID == id && frame.Decision != nil {
			return *frame.Decision
		}
		if frame.Type == protocol.TypeError {
			t.Fatalf("decision request failed: %+v", frame)
		}
	}
	t.Fatal("no correlated decision result")
	return protocol.Decision{}
}

func operatorDecisionMessage(t *testing.T, h *Hub, ctx context.Context, id, to, parent string) {
	t.Helper()
	out := h.SubmitOperator(ctx, 0, protocol.Message{Type: protocol.TypeOperatorSend, MessageID: id,
		To: to, InReplyTo: parent, Payload: json.RawMessage(`{"text":"yes"}`)})
	if out.Stage != protocol.StageAccepted {
		t.Fatalf("operator message rejected: %+v", out)
	}
}

func TestDecisionHubAcceptanceOneRealChatAskAndOneNoticeOnReplay(t *testing.T) {
	var posts atomic.Int32
	observed := make(chan string, 4)
	h, ctx, agent := decisionHub(t, &Notifier{Workspace: "desk", Post: func(_ context.Context, notice notify.Notice) (string, error) {
		posts.Add(1)
		if notice.Agent != "codex-1" || notice.Summary != "Approve release" || notice.Anchor == "" {
			t.Errorf("wrong Slack notice: %+v", notice)
		}
		return "sent", nil
	}, Observed: func(id string) { observed <- id }})
	first := requestDecision(t, h, ctx, agent, "request-1", "release", "Approve release")
	if first.State != protocol.DecisionOpen || first.Anchor == "" || first.Replayed {
		t.Fatalf("first decision = %+v", first)
	}
	select {
	case <-observed:
	case <-time.After(3 * time.Second):
		t.Fatal("Slack post did not complete")
	}
	replayed := requestDecision(t, h, ctx, agent, "request-2", "release", "Changed on replay")
	if !replayed.Replayed || replayed.ID != first.ID || replayed.Anchor != first.Anchor || replayed.Summary != first.Summary {
		t.Fatalf("replay changed decision: %+v", replayed)
	}
	page, err := h.store.ReadOperatorPage(ctx, protocol.PageRequest{})
	if err != nil || len(page.Messages) != 1 {
		t.Fatalf("operator inbox has %d asks, error %v", len(page.Messages), err)
	}
	ask := page.Messages[0]
	if ask.MessageID != first.Anchor || ask.From != "codex-1" || !ask.Durable {
		t.Fatalf("anchor is not the real durable agent message: %+v", ask)
	}
	if got := posts.Load(); got != 1 {
		t.Fatalf("replayed request posted %d Slack notices, want one", got)
	}
}

func TestDecisionHubAcceptanceOnlyCorrelatedOperatorReplyAnswers(t *testing.T) {
	h, ctx, agent := decisionHub(t, nil)
	request := requestDecision(t, h, ctx, agent, "request", "choose", "Choose deployment")
	if request.Delivery != protocol.DeliveryOff {
		t.Fatalf("notifier-off request = %+v", request)
	}
	operatorDecisionMessage(t, h, ctx, "unrelated", "codex-1", "")
	stored, err := h.store.Decision(ctx, request.ID)
	if err != nil || stored.State != protocol.DecisionOpen {
		t.Fatalf("unrelated message answered the request: %+v, error %v", stored, err)
	}
	operatorDecisionMessage(t, h, ctx, "answer", "codex-1", request.Anchor)
	stored, err = h.store.Decision(ctx, request.ID)
	if err != nil || stored.State != protocol.DecisionAnswered || stored.AnsweredAt == nil {
		t.Fatalf("correlated reply did not answer: %+v, error %v", stored, err)
	}
}

func TestDecisionHubAcceptanceUnknownAnchorIsRejectedBeforeRecordOrPost(t *testing.T) {
	var posts atomic.Int32
	h, ctx, agent := decisionHub(t, &Notifier{Post: func(context.Context, notify.Notice) (string, error) {
		posts.Add(1)
		return "sent", nil
	}})
	if !h.Submit(ctx, Inbound{From: agent, Msg: protocol.Message{
		Type: protocol.TypeDecisionRequest, RequestID: "request", DecisionSummary: "Choose now",
		AnchorMessageID: "unknown",
	}}) {
		t.Fatal("decision request not submitted")
	}
	frame := recv(t, agent)
	if frame.Type != protocol.TypeError || frame.Code != protocol.ErrDecisionAnchor || frame.RequestID != "request" {
		t.Fatalf("unknown anchor was not rejected: %+v", frame)
	}
	decisions, err := h.store.DecisionsSince(ctx, time.Time{}, 10)
	if err != nil || len(decisions) != 0 || posts.Load() != 0 {
		t.Fatalf("invalid anchor left %d records, %d posts, error %v", len(decisions), posts.Load(), err)
	}
}

func TestDecisionHubAcceptanceAnswerDuringBackoffStopsRetry(t *testing.T) {
	var posts atomic.Int32
	firstAttempt := make(chan string, 1)
	backoff := make(chan time.Duration, 1)
	tick := make(chan time.Time)
	secondPost := make(chan struct{}, 1)
	h, ctx, agent := decisionHub(t, &Notifier{
		Post: func(context.Context, notify.Notice) (string, error) {
			if posts.Add(1) > 1 {
				secondPost <- struct{}{}
			}
			return "", &notify.Failure{Class: notify.FailNetwork}
		},
		After:    func(d time.Duration) <-chan time.Time { backoff <- d; return tick },
		Observed: func(id string) { firstAttempt <- id },
	})
	request := requestDecision(t, h, ctx, agent, "request", "backoff", "Choose now")
	select {
	case <-firstAttempt:
	case <-time.After(3 * time.Second):
		t.Fatal("first delivery attempt was not recorded")
	}
	select {
	case delay := <-backoff:
		if delay != 2*time.Second {
			t.Fatalf("first backoff = %s", delay)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("retry was not scheduled")
	}
	operatorDecisionMessage(t, h, ctx, "answer", "codex-1", request.Anchor)
	tick <- time.Now()
	select {
	case <-secondPost:
		t.Fatal("answered request retried its Slack notice")
	case <-time.After(200 * time.Millisecond):
	}
	stored, err := h.store.Decision(ctx, request.ID)
	if err != nil || stored.State != protocol.DecisionAnswered {
		t.Fatalf("answer not stored: %+v, error %v", stored, err)
	}
	if got := posts.Load(); got != 1 {
		t.Fatalf("answered request retried %d times, want one", got)
	}
}

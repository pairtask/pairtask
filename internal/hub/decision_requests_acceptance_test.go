package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/notify"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

type decisionFixture struct {
	t     *testing.T
	h     *Hub
	ctx   context.Context
	agent *Client
}

func decisionHub(t *testing.T, notifier *Notifier) *decisionFixture {
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
	return &decisionFixture{t: t, h: h, ctx: ctx, agent: agent}
}

func (f *decisionFixture) request(id, key, summary string) protocol.Decision {
	f.t.Helper()
	request := protocol.Message{Type: protocol.TypeDecisionRequest, RequestID: id,
		DecisionKey: key, DecisionSummary: summary}
	if !f.h.Submit(f.ctx, Inbound{From: f.agent, Msg: request}) {
		f.t.Fatal("decision request not submitted")
	}
	for i := 0; i < 4; i++ {
		frame := recv(f.t, f.agent)
		if frame.Type == protocol.TypeDecisionResult && frame.RequestID == id {
			if frame.Decision == nil {
				f.t.Fatal("decision_result had no decision")
			}
			return *frame.Decision
		}
		if frame.Type == protocol.TypeError {
			f.t.Fatalf("decision request failed: %+v", frame)
		}
	}
	f.t.Fatal("no correlated decision result")
	return protocol.Decision{}
}

func (f *decisionFixture) operatorMessage(id, parent string) {
	f.t.Helper()
	out := f.h.SubmitOperator(f.ctx, 0, protocol.Message{Type: protocol.TypeOperatorSend, MessageID: id,
		To: f.agent.ID, InReplyTo: parent, Payload: json.RawMessage(`{"text":"yes"}`)})
	if out.Stage != protocol.StageAccepted {
		f.t.Fatalf("operator message rejected: %+v", out)
	}
}

func TestDecisionHubAcceptanceOneRealChatAskAndOneNoticeOnReplay(t *testing.T) {
	var posts atomic.Int32
	observed := make(chan string, 4)
	f := decisionHub(t, &Notifier{Workspace: "desk", Post: func(_ context.Context, notice notify.Notice) (string, error) {
		posts.Add(1)
		assertDecisionNotice(t, notice)
		return "sent", nil
	}, Observed: func(id string) { observed <- id }})
	first := f.request("request-1", "release", "Approve release")
	assertFirstDecision(t, first)
	select {
	case <-observed:
	case <-time.After(3 * time.Second):
		t.Fatal("Slack post did not complete")
	}
	replayed := f.request("request-2", "release", "Changed on replay")
	assertDecisionReplay(t, first, replayed)
	assertOneOperatorAnchor(t, f, first.Anchor)
	if got := posts.Load(); got != 1 {
		t.Fatalf("replayed request posted %d Slack notices, want one", got)
	}
}

func assertDecisionNotice(t *testing.T, notice notify.Notice) {
	t.Helper()
	if notice.Agent != "codex-1" {
		t.Errorf("wrong Slack notice: %+v", notice)
	}
	if notice.Summary != "Approve release" {
		t.Errorf("wrong Slack notice: %+v", notice)
	}
	if notice.Anchor == "" {
		t.Errorf("wrong Slack notice: %+v", notice)
	}
}

func assertFirstDecision(t *testing.T, first protocol.Decision) {
	t.Helper()
	if first.State != protocol.DecisionOpen {
		t.Fatalf("first decision = %+v", first)
	}
	if first.Anchor == "" {
		t.Fatalf("first decision = %+v", first)
	}
	if first.Replayed {
		t.Fatalf("first decision = %+v", first)
	}
}

func assertDecisionReplay(t *testing.T, first, replayed protocol.Decision) {
	t.Helper()
	if !replayed.Replayed {
		t.Fatalf("replay changed decision: %+v", replayed)
	}
	if replayed.ID != first.ID {
		t.Fatalf("replay changed decision: %+v", replayed)
	}
	if replayed.Anchor != first.Anchor {
		t.Fatalf("replay changed decision: %+v", replayed)
	}
	if replayed.Summary != first.Summary {
		t.Fatalf("replay changed decision: %+v", replayed)
	}
}

func assertOneOperatorAnchor(t *testing.T, f *decisionFixture, anchor string) {
	t.Helper()
	page, err := f.h.store.ReadOperatorPage(f.ctx, protocol.PageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 {
		t.Fatalf("operator inbox has %d asks, error %v", len(page.Messages), err)
	}
	ask := page.Messages[0]
	if ask.MessageID != anchor {
		t.Fatalf("anchor is not the real durable agent message: %+v", ask)
	}
	if ask.From != "codex-1" {
		t.Fatalf("anchor is not the real durable agent message: %+v", ask)
	}
	if !ask.Durable {
		t.Fatalf("anchor is not the real durable agent message: %+v", ask)
	}
}

func TestDecisionHubAcceptanceOnlyCorrelatedOperatorReplyAnswers(t *testing.T) {
	f := decisionHub(t, nil)
	request := f.request("request", "choose", "Choose deployment")
	if request.Delivery != protocol.DeliveryOff {
		t.Fatalf("notifier-off request = %+v", request)
	}
	f.operatorMessage("unrelated", "")
	stored, err := f.h.store.Decision(f.ctx, request.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != protocol.DecisionOpen {
		t.Fatalf("unrelated message answered the request: %+v, error %v", stored, err)
	}
	f.operatorMessage("answer", request.Anchor)
	assertDecisionAnswered(t, f, request.ID)
}

func assertDecisionAnswered(t *testing.T, f *decisionFixture, id string) {
	t.Helper()
	stored, err := f.h.store.Decision(f.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != protocol.DecisionAnswered {
		t.Fatalf("correlated reply did not answer: %+v", stored)
	}
	if stored.AnsweredAt == nil {
		t.Fatalf("correlated reply has no answer time: %+v", stored)
	}
}

func TestDecisionHubAcceptanceUnknownAnchorIsRejectedBeforeRecordOrPost(t *testing.T) {
	var posts atomic.Int32
	f := decisionHub(t, &Notifier{Post: func(context.Context, notify.Notice) (string, error) {
		posts.Add(1)
		return "sent", nil
	}})
	if !f.h.Submit(f.ctx, Inbound{From: f.agent, Msg: protocol.Message{
		Type: protocol.TypeDecisionRequest, RequestID: "request", DecisionSummary: "Choose now",
		AnchorMessageID: "unknown",
	}}) {
		t.Fatal("decision request not submitted")
	}
	frame := recv(t, f.agent)
	if frame.Type != protocol.TypeError {
		t.Fatalf("unknown anchor was not rejected: %+v", frame)
	}
	if frame.Code != protocol.ErrDecisionAnchor {
		t.Fatalf("unknown anchor was not rejected: %+v", frame)
	}
	if frame.RequestID != "request" {
		t.Fatalf("unknown anchor response lost correlation: %+v", frame)
	}
	decisions, err := f.h.store.DecisionsSince(f.ctx, time.Time{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 0 {
		t.Fatalf("invalid anchor left %d records", len(decisions))
	}
	if posts.Load() != 0 {
		t.Fatalf("invalid anchor left %d records, %d posts, error %v", len(decisions), posts.Load(), err)
	}
}

func TestDecisionHubAcceptanceAnswerDuringBackoffStopsRetry(t *testing.T) {
	var posts atomic.Int32
	firstAttempt := make(chan string, 1)
	backoff := make(chan time.Duration, 1)
	tick := make(chan time.Time)
	secondPost := make(chan struct{}, 1)
	f := decisionHub(t, &Notifier{
		Post: func(context.Context, notify.Notice) (string, error) {
			if posts.Add(1) > 1 {
				secondPost <- struct{}{}
			}
			return "", &notify.Failure{Class: notify.FailNetwork}
		},
		After:    func(d time.Duration) <-chan time.Time { backoff <- d; return tick },
		Observed: func(id string) { firstAttempt <- id },
	})
	request := f.request("request", "backoff", "Choose now")
	awaitDecisionObservation(t, firstAttempt)
	assertDecisionBackoff(t, backoff, 2*time.Second)
	f.operatorMessage("answer", request.Anchor)
	tick <- time.Now()
	assertNoSecondPost(t, secondPost)
	assertDecisionAnswered(t, f, request.ID)
	if got := posts.Load(); got != 1 {
		t.Fatalf("answered request retried %d times, want one", got)
	}
}

func awaitDecisionObservation(t *testing.T, observed <-chan string) {
	t.Helper()
	select {
	case <-observed:
	case <-time.After(3 * time.Second):
		t.Fatal("delivery attempt was not recorded")
	}
}

func assertDecisionBackoff(t *testing.T, backoff <-chan time.Duration, want time.Duration) {
	t.Helper()
	select {
	case got := <-backoff:
		if got != want {
			t.Fatalf("backoff = %s, want %s", got, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("retry was not scheduled")
	}
}

func assertNoSecondPost(t *testing.T, second <-chan struct{}) {
	t.Helper()
	select {
	case <-second:
		t.Fatal("answered request retried its Slack notice")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestDecisionHubAcceptanceThreeFailuresKeepDecisionOpenAndCountAttempts(t *testing.T) {
	var posts atomic.Int32
	observed := make(chan string, 3)
	f := decisionHub(t, &Notifier{
		Post: func(context.Context, notify.Notice) (string, error) {
			posts.Add(1)
			return "", &notify.Failure{Class: notify.FailNetwork}
		},
		After: func(time.Duration) <-chan time.Time {
			ready := make(chan time.Time, 1)
			ready <- time.Now()
			return ready
		},
		Observed: func(id string) { observed <- id },
	})
	request := f.request("request", "retry", "Choose the build")
	for i := 0; i < 3; i++ {
		awaitDecisionObservation(t, observed)
	}
	assertThreeFailures(t, f, request.ID, posts.Load())
}

func assertThreeFailures(t *testing.T, f *decisionFixture, id string, posts int32) {
	t.Helper()
	stored, err := f.h.store.Decision(f.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != protocol.DecisionOpen {
		t.Fatalf("failed notice closed the decision: %+v", stored)
	}
	if stored.Delivery != protocol.DeliveryFailed {
		t.Fatalf("failed notice was not recorded: %+v", stored)
	}
	if stored.Reason != notify.FailNetwork {
		t.Fatalf("failed notice exposed a wrong reason: %+v", stored)
	}
	if stored.Attempts != 3 {
		t.Fatalf("retry count = stored %d, posts %d", stored.Attempts, posts)
	}
	if posts != 3 {
		t.Fatalf("retry count = stored %d, posts %d", stored.Attempts, posts)
	}
}

func TestDecisionHubAcceptanceStartupDrainsPendingBeyondOneQueuePage(t *testing.T) {
	h := newTestHub(t)
	const total = DecisionQueue + 1
	seedPendingDecisionPage(t, h, total)
	observed := make(chan string, total)
	var posts atomic.Int32
	h.SetNotifier(&Notifier{Post: func(context.Context, notify.Notice) (string, error) {
		posts.Add(1)
		return "sent", nil
	}, Observed: func(id string) { observed <- id }})
	ctx, cancel := context.WithCancel(context.Background())
	go h.Run(ctx)
	t.Cleanup(func() {
		cancel()
		select {
		case <-h.Done():
		case <-time.After(5 * time.Second):
			t.Error("pending decision drain prevented shutdown")
		}
	})
	for i := 0; i < total; i++ {
		awaitDecisionObservation(t, observed)
	}
	pending, err := h.store.PendingDecisions(ctx, total)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("startup left %d requests pending", len(pending))
	}
	if got := posts.Load(); got != total {
		t.Fatalf("startup posted %d notices, want %d", got, total)
	}
}

func seedPendingDecisionPage(t *testing.T, h *Hub, total int) {
	t.Helper()
	for i := 0; i < total; i++ {
		id := fmt.Sprintf("d-%03d", i)
		_, _, err := h.store.CreateDecision(context.Background(), store.Decision{
			ID: id, Agent: "codex-1", Anchor: "ask-" + id,
			Summary: "Choose release", Delivery: protocol.DeliveryPending,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

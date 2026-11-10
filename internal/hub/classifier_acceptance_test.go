package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/classifier"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

type classifierFixture struct {
	h        *Hub
	ctx      context.Context
	clients  map[string]*Client
	observed chan string
}

func newClassifierFixture(t *testing.T, ask func(context.Context, classifier.Request) (classifier.Answer, error)) *classifierFixture {
	t.Helper()
	h := newTestHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	f := &classifierFixture{h: h, ctx: ctx, clients: map[string]*Client{}, observed: make(chan string, 256)}
	if ask != nil {
		h.SetClassifier(&Classifier{Provider: "typesafe", Model: "jev-1.14.0", Threshold: .7, Ask: ask, Observed: func(id string) { f.observed <- id }})
	}
	go h.Run(ctx)
	t.Cleanup(func() {
		cancel()
		select {
		case <-h.Done():
		case <-time.After(5 * time.Second):
			t.Error("classifier prevented hub shutdown")
		}
	})
	for _, id := range []string{"builder", "tester"} {
		c := newClient(id)
		c.ProtocolVersion = protocol.Version
		if !h.Register(ctx, c) {
			t.Fatal("registration failed")
		}
		recv(t, c)
		f.clients[id] = c
	}
	return f
}

func (f *classifierFixture) send(t *testing.T, msg protocol.Message) protocol.Message {
	t.Helper()
	msg.Type = protocol.TypeOperatorSend
	if msg.To == "" {
		msg.To = "*"
	}
	if msg.Payload == nil {
		msg.Payload = json.RawMessage(`{"text":"Please write regression tests."}`)
	}
	ctx, cancel := context.WithTimeout(f.ctx, 3*time.Second)
	defer cancel()
	out := f.h.SubmitOperator(ctx, 0, msg)
	if out.Stage != protocol.StageAccepted {
		t.Fatalf("send rejected: %+v", out)
	}
	return out
}

func (f *classifierFixture) prediction(t *testing.T, id string) *store.Prediction {
	t.Helper()
	select {
	case got := <-f.observed:
		if got != id {
			t.Fatalf("observed %s, want %s", got, id)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("prediction %s did not finish", id)
	}
	p, err := f.h.store.Prediction(f.ctx, id)
	if err != nil || p == nil {
		t.Fatalf("prediction missing: %v", err)
	}
	return p
}

func chosenTester(req classifier.Request) classifier.Answer {
	for alias, description := range req.Criteria {
		if strings.Contains(description, "tester") {
			return classifier.Answer{Choice: alias, Confidence: .95, Probabilities: map[string]float64{alias: .95, classifier.AbstainChoice: .05}, Model: "jev-1.14.0"}
		}
	}
	return classifier.Answer{Choice: classifier.AbstainChoice, Confidence: 1}
}

func TestClassifierHubAcceptanceBlockedProviderCannotDelayDeliveryOrClaim(t *testing.T) {
	f, gate := newBlockedClassifier(t)
	f.send(t, protocol.Message{MessageID: "blocked"})
	for _, c := range f.clients {
		m := recv(t, c)
		classifierEqual(t, m.MessageID, "blocked")
		classifierEqual(t, m.Assignment, (*protocol.AssignmentView)(nil))
		classifierEqual(t, m.Origin, protocol.OriginOperator)
	}
	classifierAwait(t, gate.started)
	f.claim(t, "blocked", "builder")
	close(gate.release)
	p := f.prediction(t, "blocked")
	classifierEqual(t, p.Outcome, store.PredictionPredicted)
	classifierEqual(t, p.AgentID, "tester")
	classifierEqual(t, p.Provider, "typesafe")
	classifierEqual(t, p.ModelVersion, "jev-1.14.0")
	classifierEqual(t, p.Threshold, .7)
	f.owner(t, "blocked", "builder")
	classifierEqual(t, f.send(t, protocol.Message{MessageID: "blocked"}).Repeated, true)
	again, err := f.h.store.Prediction(f.ctx, "blocked")
	classifierOK(t, err)
	classifierEqual(t, again, p)
	classifierEqual(t, gate.calls.Load(), int32(1))
	for _, c := range f.clients {
		classifierEmpty(t, c)
	}
}

func TestClassifierHubAcceptanceOffPreservesBroadcast(t *testing.T) {
	f := newClassifierFixture(t, nil)
	f.send(t, protocol.Message{MessageID: "off"})
	for _, c := range f.clients {
		if m := recv(t, c); m.MessageID != "off" || m.Assignment != nil {
			t.Fatalf("off changed frame: %+v", m)
		}
	}
	p, err := f.h.store.Prediction(f.ctx, "off")
	classifierOK(t, err)
	classifierEqual(t, p, (*store.Prediction)(nil))
	classifierEqual(t, f.h.ClassifierEnabled(), false)
}

func TestClassifierHubAcceptanceExplicitRoutingSkipsProvider(t *testing.T) {
	var calls atomic.Int32
	f := newClassifierFixture(t, func(_ context.Context, r classifier.Request) (classifier.Answer, error) {
		calls.Add(1)
		return chosenTester(r), nil
	})
	f.send(t, protocol.Message{MessageID: "tagged", Mentions: []string{"tester"}})
	for id, c := range f.clients {
		m := recv(t, c)
		if (m.Assignment != nil) != (id == "tester") {
			t.Fatalf("tag assignment changed: %+v", m)
		}
	}
	if p := f.prediction(t, "tagged"); p.Outcome != store.PredictionSkipped {
		t.Fatalf("tag classified: %+v", p)
	}
	f.send(t, protocol.Message{MessageID: "direct", To: "tester"})
	recv(t, f.clients["tester"])
	if p := f.prediction(t, "direct"); p.Outcome != store.PredictionSkipped {
		t.Fatalf("direct classified: %+v", p)
	}
	f.send(t, protocol.Message{MessageID: "reply", InReplyTo: "tagged"})
	for _, c := range f.clients {
		recv(t, c)
	}
	if p := f.prediction(t, "reply"); p.Outcome != store.PredictionSkipped {
		t.Fatalf("owned reply classified: %+v", p)
	}
	if calls.Load() != 0 {
		t.Fatalf("explicit routing called provider %d times", calls.Load())
	}
}

func TestClassifierHubAcceptanceProviderFailuresLeaveMessageClaimable(t *testing.T) {
	for _, class := range []string{classifier.FailTimeout, "http_429", "http_5xx", classifier.FailDecode} {
		t.Run(class, func(t *testing.T) {
			f := newClassifierFixture(t, func(context.Context, classifier.Request) (classifier.Answer, error) {
				return classifier.Answer{}, &classifier.Failure{Class: class}
			})
			f.send(t, protocol.Message{MessageID: "failure"})
			for _, c := range f.clients {
				recv(t, c)
			}
			p := f.prediction(t, "failure")
			want := store.PredictionError
			if class == classifier.FailTimeout {
				want = store.PredictionTimeout
			}
			classifierEqual(t, p.Outcome, want)
			classifierEqual(t, p.Reason, class)
			classifierEqual(t, p.AgentID, "")
			if _, err := f.h.store.SetClaim(f.ctx, store.ClaimRequest{MessageID: "failure", Agent: "builder", Task: "reply", Role: "owner", At: time.Now()}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestClassifierHubAcceptanceRoomAudienceFrozenBeforeProvider(t *testing.T) {
	requests := make(chan classifier.Request, 1)
	f := newClassifierFixture(t, func(_ context.Context, req classifier.Request) (classifier.Answer, error) {
		requests <- req
		return chosenTester(req), nil
	})
	if err := f.h.store.JoinRooms(f.ctx, store.RoomMember{Room: "tests", Agent: "tester"}); err != nil {
		t.Fatal(err)
	}
	f.send(t, protocol.Message{MessageID: "single", Room: "tests"})
	recv(t, f.clients["tester"])
	p := f.prediction(t, "single")
	if p.Outcome != store.PredictionSkipped || !reflect.DeepEqual(p.Eligible, []string{"tester"}) {
		t.Fatalf("room audience: %+v", p)
	}
	select {
	case <-requests:
		t.Fatal("single recipient called provider")
	default:
	}
}

func TestClassifierHubAcceptanceOversizedRosterIsExplicitlySkipped(t *testing.T) {
	h := newTestHub(t)
	observed := make(chan string, 1)
	h.SetClassifier(&Classifier{Provider: "typesafe", Model: "jev-1.14.0", Threshold: .7, Ask: func(context.Context, classifier.Request) (classifier.Answer, error) {
		t.Error("oversized roster called provider")
		return classifier.Answer{}, nil
	}, Observed: func(id string) { observed <- id }})
	var recipients []protocol.Recipient
	for i := 0; i < classifier.AgentsMax+1; i++ {
		recipients = append(recipients, protocol.Recipient{AgentID: fmt.Sprintf("agent-%03d", i)})
	}
	msg := protocol.Message{MessageID: "large", Payload: json.RawMessage(`{"text":"help"}`)}
	h.classifyLater(context.Background(), msg, recipients, operatorSend{scope: store.OperatorScope{Kind: store.ScopeAll}})
	p, err := h.store.Prediction(context.Background(), "large")
	classifierOK(t, err)
	classifierEqual(t, p != nil, true)
	classifierEqual(t, p.Outcome, store.PredictionSkipped)
	classifierEqual(t, p.Reason, "roster_too_large")
	classifierEqual(t, len(p.Eligible), len(recipients))
}

func TestClassifierHubAcceptanceQueueFullStillDelivers(t *testing.T) {
	f, gate := newBlockedClassifier(t)
	f.send(t, protocol.Message{MessageID: "in-flight"})
	f.drain(t, "in-flight")
	classifierAwait(t, gate.started)
	for i := 0; i <= ClassifierQueue; i++ {
		id := fmt.Sprintf("queued-%d", i)
		f.send(t, protocol.Message{MessageID: id})
		f.drain(t, id)
		f.acknowledge(t, id)
	}
	p := f.prediction(t, fmt.Sprintf("queued-%d", ClassifierQueue))
	classifierEqual(t, p.Outcome, store.PredictionSkipped)
	classifierEqual(t, p.Reason, "queue_full")
}

func TestClassifierHubAcceptanceRoomExcludesOutsidersAndKeepsFrozenRoster(t *testing.T) {
	request := make(chan classifier.Request, 1)
	release := make(chan struct{})
	f := newClassifierFixture(t, func(ctx context.Context, req classifier.Request) (classifier.Answer, error) {
		request <- req
		select {
		case <-release:
			return chosenTester(req), nil
		case <-ctx.Done():
			return classifier.Answer{}, ctx.Err()
		}
	})
	outsider := newClient("outsider")
	outsider.ProtocolVersion = protocol.Version
	classifierEqual(t, f.h.Register(f.ctx, outsider), true)
	recv(t, outsider)
	classifierOK(t, f.h.store.JoinRooms(f.ctx, store.RoomMember{Room: "team", Agent: "builder"}, store.RoomMember{Room: "team", Agent: "tester"}))
	f.send(t, protocol.Message{MessageID: "room", Room: "team"})
	f.drain(t, "room")
	req := classifierAwait(t, request)
	for _, description := range req.Criteria {
		classifierEqual(t, strings.Contains(description, "outsider"), false)
	}
	classifierEqual(t, len(req.Criteria), 3)
	classifierOK(t, f.h.store.LeaveRoom(f.ctx, store.RoomMember{Room: "team", Agent: "tester"}))
	close(release)
	p := f.prediction(t, "room")
	classifierEqual(t, p.Eligible, []string{"builder", "tester"})
	classifierEqual(t, p.AgentID, "tester")
	classifierEmpty(t, outsider)
}

func classifierEqual(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}
func classifierOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func classifierAwait[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("classifier event did not arrive")
	}
	var zero T
	return zero
}
func classifierEmpty(t *testing.T, c *Client) {
	t.Helper()
	select {
	case m := <-c.Send:
		t.Fatalf("unexpected classifier/delivery frame: %+v", m)
	default:
	}
}
func (f *classifierFixture) drain(t *testing.T, id string) {
	t.Helper()
	for _, c := range f.clients {
		classifierEqual(t, recv(t, c).MessageID, id)
	}
}
func (f *classifierFixture) acknowledge(t *testing.T, id string) {
	t.Helper()
	for _, c := range f.clients {
		for _, stage := range []string{protocol.StageAdapterReceived, protocol.StageAgentAcknowledged} {
			_, err := f.h.store.Acknowledge(f.ctx, store.Acknowledgment{MessageID: id, Recipient: c.identity(), Stage: stage, ProtocolVersion: protocol.Version})
			classifierOK(t, err)
		}
	}
}
func (f *classifierFixture) claim(t *testing.T, id, agent string) {
	t.Helper()
	_, err := f.h.store.SetClaim(f.ctx, store.ClaimRequest{MessageID: id, Agent: agent, Task: "reply", Role: "owner", At: time.Now()})
	classifierOK(t, err)
}
func (f *classifierFixture) owner(t *testing.T, id, agent string) {
	t.Helper()
	claims, err := f.h.store.Claims(f.ctx, id, agent)
	classifierOK(t, err)
	classifierEqual(t, len(claims.Claims), 1)
	classifierEqual(t, claims.Claims[0].AgentID, agent)
	classifierEqual(t, len(claims.Assignments), 0)
}

type classifierGate struct {
	started, release chan struct{}
	calls            atomic.Int32
}

func newBlockedClassifier(t *testing.T) (*classifierFixture, *classifierGate) {
	t.Helper()
	gate := &classifierGate{started: make(chan struct{}), release: make(chan struct{})}
	return newClassifierFixture(t, gate.ask), gate
}
func (g *classifierGate) ask(ctx context.Context, req classifier.Request) (classifier.Answer, error) {
	if g.calls.Add(1) == 1 {
		close(g.started)
	}
	select {
	case <-g.release:
		return chosenTester(req), nil
	case <-ctx.Done():
		return classifier.Answer{}, ctx.Err()
	}
}

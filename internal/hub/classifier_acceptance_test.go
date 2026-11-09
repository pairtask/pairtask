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
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	f := newClassifierFixture(t, func(ctx context.Context, req classifier.Request) (classifier.Answer, error) {
		calls.Add(1)
		close(started)
		select {
		case <-release:
			return chosenTester(req), nil
		case <-ctx.Done():
			return classifier.Answer{}, ctx.Err()
		}
	})
	f.send(t, protocol.Message{MessageID: "blocked"})
	for _, c := range f.clients {
		m := recv(t, c)
		if m.MessageID != "blocked" || m.Assignment != nil || m.Origin != protocol.OriginOperator {
			t.Fatalf("delivery changed: %+v", m)
		}
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("provider not called")
	}
	_, err := f.h.store.SetClaim(f.ctx, store.ClaimRequest{MessageID: "blocked", Agent: "builder", Task: "reply", Role: "owner", At: time.Now()})
	if err != nil {
		t.Fatalf("blocked provider prevented claim: %v", err)
	}
	close(release)
	p := f.prediction(t, "blocked")
	if p.Outcome != store.PredictionPredicted || p.AgentID != "tester" || p.Provider != "typesafe" || p.ModelVersion != "jev-1.14.0" || p.Threshold != .7 {
		t.Fatalf("prediction/provenance: %+v", p)
	}
	claims, err := f.h.store.Claims(f.ctx, "blocked", "builder")
	if err != nil || len(claims.Claims) != 1 || claims.Claims[0].AgentID != "builder" || len(claims.Assignments) != 0 {
		t.Fatalf("shadow changed ownership: %+v %v", claims, err)
	}
	if !f.send(t, protocol.Message{MessageID: "blocked"}).Repeated {
		t.Fatal("retry not identified")
	}
	again, err := f.h.store.Prediction(f.ctx, "blocked")
	if err != nil || !reflect.DeepEqual(p, again) || calls.Load() != 1 {
		t.Fatal("retry changed prediction or called provider twice")
	}
	for _, c := range f.clients {
		select {
		case m := <-c.Send:
			t.Fatalf("classifier/retry sent another frame: %+v", m)
		default:
		}
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
	if err != nil || p != nil || f.h.ClassifierEnabled() {
		t.Fatalf("off produced prediction: %+v %v", p, err)
	}
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
			if p.Outcome != want || p.Reason != class || p.AgentID != "" {
				t.Fatalf("bad failure row: %+v", p)
			}
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
	if err != nil || p == nil || p.Outcome != store.PredictionSkipped || p.Reason != "roster_too_large" || len(p.Eligible) != len(recipients) {
		t.Fatalf("oversized roster silently truncated/queued: %+v %v", p, err)
	}
}

func TestClassifierHubAcceptanceQueueFullStillDelivers(t *testing.T) {
	started := make(chan struct{})
	var calls atomic.Int32
	f := newClassifierFixture(t, func(ctx context.Context, req classifier.Request) (classifier.Answer, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-ctx.Done()
		return classifier.Answer{}, ctx.Err()
	})
	f.send(t, protocol.Message{MessageID: "in-flight"})
	for _, c := range f.clients {
		recv(t, c)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("provider did not start")
	}
	for i := 0; i <= ClassifierQueue; i++ {
		id := fmt.Sprintf("queued-%d", i)
		f.send(t, protocol.Message{MessageID: id})
		for _, c := range f.clients {
			if m := recv(t, c); m.MessageID != id {
				t.Fatalf("overflow delivery lost: %+v", m)
			}
			for _, stage := range []string{protocol.StageAdapterReceived, protocol.StageAgentAcknowledged} {
				if _, err := f.h.store.Acknowledge(f.ctx, store.Acknowledgment{MessageID: id, Recipient: c.identity(), Stage: stage, ProtocolVersion: protocol.Version}); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	p := f.prediction(t, fmt.Sprintf("queued-%d", ClassifierQueue))
	if p.Outcome != store.PredictionSkipped || p.Reason != "queue_full" {
		t.Fatalf("overflow: %+v", p)
	}
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
	f.h.Register(f.ctx, outsider)
	recv(t, outsider)
	if err := f.h.store.JoinRooms(f.ctx, store.RoomMember{Room: "team", Agent: "builder"}, store.RoomMember{Room: "team", Agent: "tester"}); err != nil {
		t.Fatal(err)
	}
	f.send(t, protocol.Message{MessageID: "room", Room: "team"})
	for _, c := range f.clients {
		recv(t, c)
	}
	var req classifier.Request
	select {
	case req = <-request:
	case <-time.After(3 * time.Second):
		t.Fatal("provider did not start")
	}
	for _, description := range req.Criteria {
		if strings.Contains(description, "outsider") {
			t.Fatal("room sent outsider to provider")
		}
	}
	if len(req.Criteria) != 3 {
		t.Fatalf("room criteria=%v", req.Criteria)
	}
	if err := f.h.store.LeaveRoom(f.ctx, store.RoomMember{Room: "team", Agent: "tester"}); err != nil {
		t.Fatal(err)
	}
	close(release)
	p := f.prediction(t, "room")
	if !reflect.DeepEqual(p.Eligible, []string{"builder", "tester"}) || p.AgentID != "tester" {
		t.Fatalf("late roster changed shadow decision: %+v", p)
	}
	select {
	case m := <-outsider.Send:
		t.Fatalf("outsider got room message: %+v", m)
	default:
	}
}

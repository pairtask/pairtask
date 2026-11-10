package hub

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/pairtask/pairtask/internal/classifier"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

// The shadow classifier (#339, rollout step 1): after an untagged operator
// message to everyone has been persisted and handed to its recipients, a
// worker asks the decision model which recipient it was for and records
// the answer. Nothing reads the answer back into routing: no assignment,
// claim or frame changes. Delivery never waits for it.

// ClassifierQueue bounds the jobs waiting for the worker; a full queue
// skips the message (recorded) rather than hold anything up.
const ClassifierQueue = 64

// Classifier is what the hub is given when the mode is shadow. Ask is the
// provider call; tests inject one. Observed, when set, is called after a
// row is written for the message; tests wait on it instead of polling.
type Classifier struct {
	Provider  string
	Model     string
	Threshold float64
	Roles     map[string]string
	Ask       func(ctx context.Context, req classifier.Request) (classifier.Answer, error)
	Observed  func(messageID string)
	Clock     func() time.Time
}

// classifierJob is what the worker needs about one message; it never
// re-reads routing state.
type classifierJob struct {
	messageID string
	text      string
	room      string
	eligible  []string
}

type hubClassifier struct {
	cfg     *Classifier
	jobs    chan classifierJob
	workers sync.WaitGroup
}

// SetClassifier enables shadow predictions; call before Run. Nil disables.
func (h *Hub) SetClassifier(cfg *Classifier) {
	if cfg == nil || cfg.Ask == nil {
		h.classify = hubClassifier{}
		return
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	h.classify = hubClassifier{cfg: cfg, jobs: make(chan classifierJob, ClassifierQueue)}
}

// ClassifierEnabled: predictions are being recorded.
func (h *Hub) ClassifierEnabled() bool { return h.classify.cfg != nil }

// startClassifier runs the worker until ctx ends; Run waits for it.
func (h *Hub) startClassifier(ctx context.Context) {
	if h.classify.cfg == nil {
		return
	}
	h.classify.workers.Add(1)
	go func() {
		defer h.classify.workers.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case job := <-h.classify.jobs:
				h.predict(ctx, job)
			}
		}
	}()
}

// classifyLater queues an eligible send for the worker, or records why it
// was not asked. Called once the message is persisted and delivery queued.
func (h *Hub) classifyLater(ctx context.Context, out protocol.Message, recipients []protocol.Recipient, send operatorSend) {
	c := h.classify.cfg
	if c == nil {
		return
	}
	if reason := ineligible(out, send); reason != "" {
		h.recordSkip(ctx, out.MessageID, reason, nil)
		return
	}
	var ids []string
	for _, r := range recipients {
		ids = append(ids, r.AgentID)
	}
	eligible := classifier.Eligible(ids)
	if len(eligible) < 2 {
		h.recordSkip(ctx, out.MessageID, "single_recipient", eligible)
		return
	}
	if len(eligible) > classifier.AgentsMax {
		h.recordSkip(ctx, out.MessageID, "roster_too_large", eligible)
		return
	}
	text, _ := payloadText(out.Payload)
	job := classifierJob{messageID: out.MessageID, text: text, room: out.Room, eligible: eligible}
	select {
	case h.classify.jobs <- job:
	default:
		h.recordSkip(ctx, out.MessageID, "queue_full", eligible)
	}
}

// ineligible is why a send is not classified: not to everyone, tagged
// (the tags assign), or a reply (its conversation may already have an
// owner); "" when it is eligible.
func ineligible(out protocol.Message, send operatorSend) string {
	switch {
	case send.scope.Kind != store.ScopeAll:
		return "not_broadcast"
	case len(out.Mentions) > 0:
		return "tagged"
	case out.InReplyTo != "":
		return "reply"
	}
	return ""
}

// predict asks the model and records one row, whatever happened.
func (h *Hub) predict(ctx context.Context, job classifierJob) {
	c := h.classify.cfg
	roster := classifier.NewRoster(job.eligible, c.Roles)
	req := classifier.Request{Model: c.Model, State: classifier.State(job.text, job.room), Criteria: roster.Criteria,
		Instructions: "Which agent is this operator message for? Choose the one agent it addresses or asks to act; choose none when no single agent is clearly meant."}
	started := c.Clock()
	answer, err := c.Ask(ctx, req)
	p := store.Prediction{MessageID: job.messageID, Eligible: job.eligible, Provider: c.Provider, Model: c.Model,
		Threshold: c.Threshold, LatencyMS: c.Clock().Sub(started).Milliseconds(), CreatedAt: c.Clock()}
	if err != nil {
		p.Outcome, p.Reason = failureOutcome(err)
	} else {
		p = decided(p, roster, answer, c.Threshold)
	}
	h.recordPrediction(ctx, p)
}

// failureOutcome maps a provider failure to an outcome and its class.
func failureOutcome(err error) (outcome, reason string) {
	var failure *classifier.Failure
	if errors.As(err, &failure) {
		if failure.Class == classifier.FailTimeout {
			return store.PredictionTimeout, failure.Class
		}
		return store.PredictionError, failure.Class
	}
	return store.PredictionError, "request"
}

// decided applies the threshold and the roster to the model's answer.
func decided(p store.Prediction, roster classifier.Roster, answer classifier.Answer, threshold float64) store.Prediction {
	p.ModelVersion = answer.Model
	p.Confidence = answer.Confidence
	p.Probabilities, p.AbstainProbability = rekey(roster, answer.Probabilities)
	agent := roster.Agent(answer.Choice)
	switch {
	case agent == "" && answer.Choice != classifier.AbstainChoice:
		p.Outcome, p.Reason = store.PredictionAbstain, "outside_roster"
	case agent == "":
		p.Outcome, p.Reason = store.PredictionAbstain, "none"
	case answer.Confidence < threshold:
		p.Outcome, p.Reason = store.PredictionAbstain, "below_threshold"
	default:
		p.Outcome, p.AgentID = store.PredictionPredicted, agent
	}
	return p
}

// rekey maps the answer's alias probabilities back to agent IDs, with the
// abstain choice's apart; aliases outside the roster are dropped.
func rekey(roster classifier.Roster, probabilities map[string]float64) (map[string]float64, float64) {
	byAgent, abstain := map[string]float64{}, 0.0
	for alias, probability := range probabilities {
		if agent := roster.Agent(alias); agent != "" {
			byAgent[agent] = probability
		} else if alias == classifier.AbstainChoice {
			abstain = probability
		}
	}
	return byAgent, abstain
}

func (h *Hub) recordSkip(ctx context.Context, messageID, reason string, eligible []string) {
	c := h.classify.cfg
	h.recordPrediction(ctx, store.Prediction{MessageID: messageID, Outcome: store.PredictionSkipped, Reason: reason,
		Eligible: eligible, Provider: c.Provider, Model: c.Model, Threshold: c.Threshold, CreatedAt: c.Clock()})
}

// recordPrediction writes the row; the first one for a message stays.
func (h *Hub) recordPrediction(ctx context.Context, p store.Prediction) {
	if _, err := h.store.RecordPrediction(ctx, p); err != nil {
		h.log.Error("classifier prediction not recorded", "message_id", p.MessageID, "error", err)
	}
	if observed := h.classify.cfg.Observed; observed != nil {
		observed(p.MessageID)
	}
}

// settleClassifier waits for the worker at shutdown.
func (h *Hub) settleClassifier() { h.classify.workers.Wait() }

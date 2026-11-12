package hub

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/classifier"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

func TestAgentRolesHubAcceptanceRefusesOldOrInvalidRoleWithoutPersisting(t *testing.T) {
	f := newClassifierFixture(t, nil)
	for _, tc := range []struct {
		name    string
		version int
		role    string
		code    string
	}{
		{"old protocol", protocol.RoleVersion - 1, "CI tests", protocol.ErrForbidden},
		{"multiline role", protocol.RoleVersion, "CI tests\nand reviews", protocol.ErrMalformedFrame},
		{"oversized role", protocol.RoleVersion, strings.Repeat("x", protocol.AgentRoleMax+1), protocol.ErrMalformedFrame},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := f.h.roleAnswer(f.ctx, Inbound{From: &Client{ID: "tester", ProtocolVersion: tc.version},
				Msg: protocol.Message{Type: protocol.TypeRoleSet, RequestID: "reject", AgentRoleText: tc.role}})
			classifierEqual(t, response.Type, protocol.TypeError)
			classifierEqual(t, response.Code, tc.code)
			role, err := f.h.store.AgentRole(f.ctx, "tester")
			classifierOK(t, err)
			classifierEqual(t, role, "")
		})
	}
}

func TestAgentRolesHubAcceptanceUsesAuthenticatedRoleOnNextPrediction(t *testing.T) {
	requests := make(chan classifier.Request, 3)
	f := newClassifierFixture(t, func(_ context.Context, req classifier.Request) (classifier.Answer, error) {
		requests <- req
		return classifier.Answer{Choice: classifier.AbstainChoice, Confidence: 1}, nil
	})
	f.h.classify.cfg.Roles = map[string]string{"builder": "implementation", "tester": "old configured role"}

	result := f.h.roleAnswer(f.ctx, Inbound{
		From: f.clients["tester"],
		Msg:  protocol.Message{Type: protocol.TypeRoleSet, RequestID: "role-1", AgentID: "builder", AgentRoleText: "CI tests and reviews"},
	})
	classifierEqual(t, result.Type, protocol.TypeRoleResult)
	classifierEqual(t, result.RequestID, "role-1")
	if result.AgentRole == nil {
		t.Fatal("role result omitted stored role")
	}
	classifierEqual(t, result.AgentRole.AgentID, "tester")
	classifierEqual(t, result.AgentRole.Role, "CI tests and reviews")
	builderRole, err := f.h.store.AgentRole(f.ctx, "builder")
	classifierOK(t, err)
	classifierEqual(t, builderRole, "")

	f.send(t, protocol.Message{MessageID: "role-first"})
	f.drain(t, "role-first")
	first := classifierAwait(t, requests)
	classifierEqual(t, first.Criteria["a1"], "builder: implementation")
	classifierEqual(t, first.Criteria["a2"], "tester: CI tests and reviews")
	classifierEqual(t, f.prediction(t, "role-first").Outcome, store.PredictionAbstain)

	// A role change is read when the next job runs, without restarting the broker.
	_, err = f.h.store.SetAgentRole(f.ctx, "tester", "release gates", time.Now().UTC())
	classifierOK(t, err)
	f.send(t, protocol.Message{MessageID: "role-next"})
	f.drain(t, "role-next")
	next := classifierAwait(t, requests)
	classifierEqual(t, next.Criteria["a2"], "tester: release gates")
	classifierEqual(t, f.prediction(t, "role-next").Outcome, store.PredictionAbstain)

	_, err = f.h.store.SetAgentRole(f.ctx, "tester", "", time.Now().UTC())
	classifierOK(t, err)
	f.send(t, protocol.Message{MessageID: "role-cleared"})
	f.drain(t, "role-cleared")
	cleared := classifierAwait(t, requests)
	classifierEqual(t, cleared.Criteria["a2"], "tester: old configured role")
	classifierEqual(t, f.prediction(t, "role-cleared").Outcome, store.PredictionAbstain)
}

func TestAgentRolesHubAcceptanceFeedbackCountsClaimedAbstentionsButNotUnaskedMessages(t *testing.T) {
	f := newClassifierFixture(t, nil)
	at := time.Now().UTC().Add(-time.Minute)
	cases := []struct {
		id       string
		outcome  string
		picked   string
		claimant string
	}{
		{"abstained", store.PredictionAbstain, "", "tester"},
		{"wrong-pick", store.PredictionPredicted, "tester", "builder"},
		{"unclaimed", store.PredictionPredicted, "tester", ""},
		{"skipped", store.PredictionSkipped, "", "tester"},
		{"failed", store.PredictionError, "", "tester"},
	}
	for i, tc := range cases {
		f.send(t, protocol.Message{MessageID: tc.id})
		f.drain(t, tc.id)
		f.seedPrediction(t, store.Prediction{MessageID: tc.id, Eligible: []string{"builder", "tester"}, Outcome: tc.outcome, AgentID: tc.picked,
			Probabilities: map[string]float64{"tester": .65, "builder": .25}, Confidence: .65, CreatedAt: at.Add(time.Duration(i) * time.Second)})
		if tc.claimant != "" {
			f.claim(t, tc.id, tc.claimant)
		}
	}

	tester, err := f.h.feedback(f.ctx, "tester", 20)
	classifierOK(t, err)
	classifierEqual(t, tester.AgentID, "tester")
	classifierEqual(t, tester.Counts, map[string]int{
		protocol.VerdictMissed: 1, protocol.VerdictWrongPick: 1, protocol.VerdictUnclaimedPick: 1,
	})
	classifierEqual(t, len(tester.Items), 3)
	classifierEqual(t, tester.Items[0].MessageID, "unclaimed")
	classifierEqual(t, tester.Items[0].Verdict, protocol.VerdictUnclaimedPick)
	classifierEqual(t, tester.Items[1].MessageID, "wrong-pick")
	classifierEqual(t, tester.Items[1].Verdict, protocol.VerdictWrongPick)
	classifierEqual(t, tester.Items[2].MessageID, "abstained")
	classifierEqual(t, tester.Items[2].Verdict, protocol.VerdictMissed)
	classifierEqual(t, tester.Items[2].YourProbability, .65)

	builder, err := f.h.feedback(f.ctx, "builder", 20)
	classifierOK(t, err)
	classifierEqual(t, builder.Counts, map[string]int{protocol.VerdictMissed: 1})
	classifierEqual(t, len(builder.Items), 1)
	classifierEqual(t, builder.Items[0].MessageID, "wrong-pick")
}

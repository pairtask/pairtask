package bridge

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/pairtask/pairtask/internal/forge"
	"github.com/pairtask/pairtask/internal/protocol"
)

// The broker stub records only public requests; it does not simulate forge work.
type staleBridgeClient struct {
	version int
	view    protocol.ReviewView
	forges  forge.Registry
	calls   []protocol.Message
}

func (c *staleBridgeClient) ReviewProtocol() int { return c.version }

func (c *staleBridgeClient) ReviewForges(context.Context) (forge.Registry, error) {
	return c.forges, nil
}

func (c *staleBridgeClient) ReviewCall(_ context.Context, m protocol.Message) (protocol.Message, error) {
	c.calls = append(c.calls, m)
	switch m.Type {
	case "review_check_pins":
		if *m.Revisions != *c.view.Pinned {
			c.view.State = protocol.ReviewDeclined
		}
	case protocol.TypeReviewPrepare:
		c.view.State, c.view.Revisions = protocol.ReviewReady, m.Revisions
	case protocol.TypeReviewAccept:
		c.view.State, c.view.Attempt = protocol.ReviewInReview, 1
	}
	v := c.view
	return protocol.Message{Type: protocol.TypeReviewResult, Review: &v}, nil
}

func staleBridgeFixture(state string) (*staleBridgeClient, *forge.Fake) {
	pins := reviewBridgePins()
	fake := forge.NewFake()
	fake.SetRevisions(reviewBridgeTarget(), pins)
	v := protocol.ReviewView{ID: "stale-bridge", State: state, Spec: protocol.ReviewSpec{ReviewTarget: reviewBridgeTarget(), Reviewer: "reviewer"},
		Remote: &protocol.Remote{Contact: "paired", Sender: "requester"}, Pinned: &pins, RemoteRequest: "ask"}
	if state == protocol.ReviewReady {
		v.Revisions = &pins
	}
	return &staleBridgeClient{version: 26, view: v, forges: forge.Registry{"fake": fake}}, fake
}

func staleBridgeFields(t *testing.T, value any) map[string]any {
	t.Helper()
	data, err := json.Marshal(value)
	requireNoError(t, err)
	var fields map[string]any
	requireNoError(t, json.Unmarshal(data, &fields))
	return fields
}

func staleBridgeLast(t *testing.T, c *staleBridgeClient) protocol.Message {
	t.Helper()
	if len(c.calls) == 0 {
		t.Fatal("no broker call")
	}
	return c.calls[len(c.calls)-1]
}

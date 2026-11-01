package messaging_test

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

func TestRefusalVisibilitySessionAcceptanceSignalsReceiverWithoutStored(t *testing.T) {
	left, _ := sessionFixture(t)
	right, _ := sessionFixture(t)
	m := reviewWireFixture()
	_, err := left.PeerMessages().Queue(context.Background(), messaging.Outgoing{
		Contact: sessionContact, ID: "review-refused", Text: m.Text, Now: sessionAt, To: m.To, Review: m.Review,
	})
	sessionOK(t, err)
	outcomes := sessionExchange(t, left.PeerMessages(), right.PeerMessages(), 0)
	sessionSuccess(t, outcomes)
	refused, recorded := 0, 0
	for _, outcome := range outcomes {
		// Both the sender's terminal result and the receiver's local notice
		// must wake their operators; neither side claims Stored/delivery.
		sessionEqual(t, outcome.result.Moved(), true)
		sessionEqual(t, outcome.result.Sent, 0)
		sessionEqual(t, outcome.result.Received, 0)
		refused += outcome.result.Refused
		recorded += outcome.result.RefusalsRecorded
	}
	sessionEqual(t, refused, 1)
	sessionEqual(t, recorded, 1)
	sessionEqual(t, sessionRows(t, left.PeerMessages(), 1)[0].State, messaging.StateRefused)
	row := sessionRows(t, right.PeerMessages(), 1)[0]
	sessionEqual(t, row.State, messaging.StateRefused)
	sessionEqual(t, row.Refusal, messaging.Refuse(messaging.ReasonNotShared))
	if row.DeliveredAt != nil {
		t.Fatal("rejected request claims delivery")
	}
	sessionPending(t, right.PeerMessages(), 0)
}

func TestRefusalVisibilitySessionAcceptanceLostReplyStillSignalsOnce(t *testing.T) {
	db, path := sessionFixture(t)
	m := reviewWireFixture()
	hello, err := messaging.EncodeHello(messaging.Hello{Min: messaging.ReviewVersion, Max: messaging.ReviewVersion})
	sessionOK(t, err)
	ask, err := messaging.EncodeReviewAsk(m)
	sessionOK(t, err)
	for i := 0; i < 2; i++ {
		frames := &scriptFrames{input: [][]byte{hello, ask}, failKind: messaging.FrameRefused}
		result, err := sessionSide(frames, db.PeerMessages()).Serve(context.Background())
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("refusal reply failure not injected: %v", err)
		}
		sessionEqual(t, result.Moved(), i == 0)
		sessionEqual(t, result.RefusalsRecorded, 1-i)
		sessionEqual(t, result.Refused, 0)
		sessionEqual(t, result.Received, 0)
		sessionEqual(t, len(sessionRows(t, db.PeerMessages(), 1)), 1)
		if i == 0 {
			sessionOK(t, db.Close())
			db = sessionDB(t, path)
		}
	}
}

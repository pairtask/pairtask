package bridge

import (
	"bufio"
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestPairedResultsAcceptanceRealReadLoopReturnsToCaller(t *testing.T) {
	for _, populated := range []bool{false, true} {
		for _, kind := range []string{protocol.TypeWorkspacesResult, protocol.TypeReviewsAskedResult} {
			name := kind + "/empty"
			if populated {
				name = kind + "/populated"
			}
			t.Run(name, func(t *testing.T) {
				c, remote := pipeClient(t) // starts the production socket read loop
				c.protocolVersion = protocol.Version
				want := protocol.Message{Type: kind}
				if populated {
					want.Workspaces = []protocol.WorkspaceView{{Key: "sha256:remote", Name: "Office", Live: true, Agents: []string{"claude-1"}}}
					want.ReviewsAsked = []protocol.ReviewOutcomeView{{AskID: "review-1", Reviewer: "claude-1", Repository: "github.com/pairtask/pairtask", Change: "253", State: "refused", Reason: "not_shared"}}
				}
				wireDone := make(chan error, 1)
				go func() {
					_ = remote.SetDeadline(time.Now().Add(3 * time.Second))
					var request protocol.Message
					if err := protocol.ReadFrame(bufio.NewReader(remote), &request); err != nil {
						wireDone <- err
						return
					}
					want.RequestID = request.RequestID
					if err := protocol.WriteFrame(remote, want); err != nil {
						wireDone <- err
						return
					}
					wireDone <- protocol.WriteFrame(remote, protocol.Message{Type: protocol.TypeMsg, MessageID: "after-result", From: "peer"})
				}()
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				if kind == protocol.TypeWorkspacesResult {
					got, err := c.Workspaces(ctx)
					requireNoError(t, err)
					expected := want.Workspaces
					if expected == nil {
						expected = []protocol.WorkspaceView{}
					}
					if !reflect.DeepEqual(got.Workspaces, expected) {
						t.Fatalf("workspaces = %+v, want %+v", got.Workspaces, expected)
					}
				} else {
					got, err := c.ReviewsAsked(ctx)
					requireNoError(t, err)
					expected := want.ReviewsAsked
					if expected == nil {
						expected = []protocol.ReviewOutcomeView{}
					}
					if !reflect.DeepEqual(got.Asks, expected) {
						t.Fatalf("reviews = %+v, want %+v", got.Asks, expected)
					}
				}
				requireNoError(t, <-wireDone)
				inbox, err := c.Receive(ctx, 10, time.Second)
				requireNoError(t, err)
				if len(inbox.Messages) != 1 {
					t.Fatalf("result leaked to inbox: %+v", inbox.Messages)
				}
				assertEqual(t, "only subsequent peer message reaches inbox", inbox.Messages[0].MessageID, "after-result")
			})
		}
	}
}

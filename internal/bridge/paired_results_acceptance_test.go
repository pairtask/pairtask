package bridge

import (
	"bufio"
	"context"
	"net"
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
			t.Run(name, func(t *testing.T) { pairedResultRoundTrip(t, pairedAnswer(kind, populated)) })
		}
	}
}

func pairedAnswer(kind string, populated bool) protocol.Message {
	answer := protocol.Message{Type: kind, Workspaces: []protocol.WorkspaceView{}, ReviewsAsked: []protocol.ReviewOutcomeView{}}
	if populated {
		answer.Workspaces = []protocol.WorkspaceView{{Key: "sha256:remote", Name: "Office", Live: true, Agents: []string{"claude-1"}}}
		answer.ReviewsAsked = []protocol.ReviewOutcomeView{{AskID: "review-1", Reviewer: "claude-1", Repository: "github.com/pairtask/pairtask", Change: "253", State: "refused", Reason: "not_shared"}}
	}
	return answer
}

func pairedAnswerWire(remote net.Conn, answer protocol.Message) error {
	_ = remote.SetDeadline(time.Now().Add(3 * time.Second))
	var request protocol.Message
	if err := protocol.ReadFrame(bufio.NewReader(remote), &request); err != nil {
		return err
	}
	answer.RequestID = request.RequestID
	if err := protocol.WriteFrame(remote, answer); err != nil {
		return err
	}
	return protocol.WriteFrame(remote, protocol.Message{Type: protocol.TypeMsg, MessageID: "after-result", From: "peer"})
}

func pairedResultRoundTrip(t *testing.T, answer protocol.Message) {
	t.Helper()
	c, remote := pipeClient(t) // starts the production socket read loop
	c.protocolVersion = protocol.Version
	wireDone := make(chan error, 1)
	go func() { wireDone <- pairedAnswerWire(remote, answer) }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	assertPairedAnswer(t, ctx, c, answer)
	requireNoError(t, <-wireDone)
	inbox, err := c.Receive(ctx, 10, time.Second)
	requireNoError(t, err)
	if len(inbox.Messages) != 1 {
		t.Fatalf("result leaked to inbox: %+v", inbox.Messages)
	}
	assertEqual(t, "only subsequent peer message reaches inbox", inbox.Messages[0].MessageID, "after-result")
}

func assertPairedAnswer(t *testing.T, ctx context.Context, c *Client, answer protocol.Message) {
	t.Helper()
	if answer.Type == protocol.TypeWorkspacesResult {
		got, err := c.Workspaces(ctx)
		requireNoError(t, err)
		if !reflect.DeepEqual(got.Workspaces, answer.Workspaces) {
			t.Fatalf("workspaces = %+v, want %+v", got.Workspaces, answer.Workspaces)
		}
		return
	}
	got, err := c.ReviewsAsked(ctx)
	requireNoError(t, err)
	if !reflect.DeepEqual(got.Asks, answer.ReviewsAsked) {
		t.Fatalf("reviews = %+v, want %+v", got.Asks, answer.ReviewsAsked)
	}
}

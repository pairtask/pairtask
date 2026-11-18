package bridge

import (
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestFinishReviewerMCPAcceptanceForwardsExplicitRoleAndKeepsDefault(t *testing.T) {
	for _, role := range []string{"reviewer", ""} {
		t.Run("role="+role, func(t *testing.T) {
			c, remote := pipeClient(t)
			c.protocolVersion = 8
			session := connectMCP(t, c)
			request := make(chan protocol.Message, 1)
			done := make(chan error, 1)
			go func() { done <- replyToClaimTool(remote, request) }()
			args := claimToolArgs("finish_claim")
			if role != "" {
				args["role"] = role
			}
			result := call(t, session, "finish_claim", args)
			requireNoError(t, <-done)
			req := <-request
			requireClaimToolRequest(t, req, "claim_done")
			assertEqual(t, "requested completion role reaches broker", req.Role, role)
			requireConfirmedClaimResult(t, result)
		})
	}
}

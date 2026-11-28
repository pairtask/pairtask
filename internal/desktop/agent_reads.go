package desktop

import (
	"context"

	"github.com/pairtask/pairtask/internal/protocol"
)

// AgentsStatusResult is agents_status's answer: each agent of this Mac, how
// far it has read.
type AgentsStatusResult struct {
	Agents []protocol.AgentRead `json:"agents"`
}

const agentsStatusDescription = "How each agent on this Mac keeps up with its messages: whether it is connected now, how many messages it hasn't acknowledged yet, the oldest one's time, and when it last acknowledged one. " +
	"An agent is an app connected with its own agent ID. Reads nothing and marks nothing read."

// agentsStatus asks the broker for every agent's reading.
func (n *Node) agentsStatus(ctx context.Context) (AgentsStatusResult, error) {
	out, err := n.ask(ctx, protocol.Message{Type: protocol.TypeOperatorAgentReads}, protocol.TypeOperatorAgentReads)
	if err != nil {
		return AgentsStatusResult{Agents: []protocol.AgentRead{}}, err
	}
	agents := out.AgentReads
	if agents == nil {
		agents = []protocol.AgentRead{}
	}
	return AgentsStatusResult{Agents: agents}, nil
}

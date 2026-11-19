package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

type agentMessage struct {
	Text string     `json:"text"`
	TS   *time.Time `json:"ts"`
}

type operatorAgent struct {
	ID          string        `json:"id"`
	Connected   bool          `json:"connected"`
	State       *string       `json:"state"`
	Status      *string       `json:"status"`
	LastMessage *agentMessage `json:"last_message"`
}

type operatorAgents struct {
	Agents                []*operatorAgent `json:"agents"`
	RosterTruncated       bool             `json:"roster_truncated,omitempty"`
	DeclarationTotal      int              `json:"declaration_total"`
	LastMessagesTruncated bool             `json:"last_messages_truncated,omitempty"`
}

func runOperatorAgents(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	call, code := parseAgents(args, stderr)
	if code != -1 {
		return code
	}
	ctx, cancel := context.WithTimeout(ctx, call.timeout)
	defer cancel()
	result, err := readOperatorAgents(ctx, call, stderr)
	if err != nil {
		if !errors.Is(err, errReported) {
			fmt.Fprintln(stderr, "operator agents unavailable:", err)
		}
		return 1
	}
	if call.asJSON {
		return encodeJSON(stdout, stderr, result)
	}
	return writeOperatorAgents(stdout, result)
}

func writeOperatorAgents(stdout io.Writer, result operatorAgents) int {
	if result.RosterTruncated {
		fmt.Fprintf(stdout, "Agent roster truncated: showing %d agents; broker has %d declarations.\n", len(result.Agents), result.DeclarationTotal)
	}
	if result.LastMessagesTruncated {
		fmt.Fprintln(stdout, "Last messages limited to the newest 512 history entries; missing messages are unknown.")
	}
	for _, agent := range result.Agents {
		writeOperatorAgent(stdout, agent)
	}
	if len(result.Agents) == 0 {
		fmt.Fprintln(stdout, "No agents recorded.")
	}
	return 0
}

func parseAgents(args []string, stderr io.Writer) (operatorCall, int) {
	var call operatorCall
	flags := flag.NewFlagSet("operator agents", flag.ContinueOnError)
	call.register(flags)
	code := parseFlags(flags, args, "usage: pairtask operator agents [--json] [--socket PATH]", stderr, func(rest []string) error {
		if len(rest) != 0 {
			return errors.New("agents takes no positional arguments")
		}
		return call.validate()
	})
	return call, code
}

func readOperatorAgents(ctx context.Context, call operatorCall, stderr io.Writer) (operatorAgents, error) {
	out := operatorAgents{Agents: []*operatorAgent{}}
	resp, err := call.expect(ctx, protocol.Message{Type: protocol.TypeStatus}, expectation{protocol.TypeStatus, "operator agents"}, stderr)
	if err != nil {
		return out, err
	}
	snapshot := resp.Status
	if !rosterAvailable(snapshot) {
		return out, errors.New("agent status or session history unavailable")
	}
	out.RosterTruncated = snapshot.AgentStatusTruncated || snapshot.SessionsTruncated
	out.DeclarationTotal = snapshot.AgentStatusTotal
	agents := agentRoster(snapshot)
	truncated, err := lastAgentMessages(ctx, call, agents, stderr)
	out.LastMessagesTruncated = truncated
	if err != nil {
		return out, err
	}
	out.Agents = sortedAgents(agents)
	return out, nil
}

func rosterAvailable(snapshot *protocol.StatusSnapshot) bool {
	if snapshot == nil {
		return false
	}
	return snapshot.AgentStatusAvailable && snapshot.HistoryAvailable
}

func sortedAgents(agents map[string]*operatorAgent) []*operatorAgent {
	ids := make([]string, 0, len(agents))
	for id := range agents {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	out := make([]*operatorAgent, 0, len(ids))
	for _, id := range ids {
		out = append(out, agents[id])
	}
	return out
}

func agentRoster(snapshot *protocol.StatusSnapshot) map[string]*operatorAgent {
	agents := map[string]*operatorAgent{}
	for _, session := range snapshot.Sessions {
		if session.AgentID == protocol.OperatorID {
			continue
		}
		agent := ensureAgent(agents, session.AgentID)
		agent.Connected = agent.Connected || session.State == "transport_connected"
	}
	for _, status := range snapshot.AgentStatus {
		agent := ensureAgent(agents, status.AgentID)
		state, work := status.State, preview(strings.TrimSpace(status.Task+" "+status.Step), 80)
		agent.State, agent.Status = &state, &work
	}
	return agents
}

func ensureAgent(agents map[string]*operatorAgent, id string) *operatorAgent {
	if agents[id] == nil {
		agents[id] = &operatorAgent{ID: id}
	}
	return agents[id]
}

// Walk history newest first, including acknowledged messages. These one-shot
// requests never open or replace the chat's operator session and never receipt.
func lastAgentMessages(ctx context.Context, call operatorCall, agents map[string]*operatorAgent, stderr io.Writer) (bool, error) {
	remaining := len(agents)
	page := protocol.PageRequest{Limit: protocol.HistoryMax}
	for pages := 0; remaining > 0; pages++ {
		if pages == 16 {
			return true, nil
		}
		resp, err := call.expect(ctx, protocol.Message{Type: protocol.TypeOperatorHistory, PageRequest: &page}, expectation{protocol.TypeOperatorHistory, "operator agents history"}, stderr)
		if err != nil {
			return false, err
		}
		if err := validateHistory(page, resp.History); err != nil {
			return false, err
		}
		remaining -= recordLastMessages(agents, resp.History.Entries)
		if !resp.History.More {
			break
		}
		page.BeforeSeq, page.UpperSeq = resp.History.NextBeforeSeq, resp.History.UpperSeq
	}
	return false, nil
}

func recordLastMessages(agents map[string]*operatorAgent, entries []protocol.HistoryEntry) int {
	count := 0
	for _, entry := range entries {
		if entry.Direction != "received" {
			continue
		}
		agent := agents[entry.Message.From]
		if agent == nil {
			continue
		}
		if agent.LastMessage != nil {
			continue
		}
		agent.LastMessage = historyMessage(entry)
		count++
	}
	return count
}

func historyMessage(entry protocol.HistoryEntry) *agentMessage {
	message := summarizeMessage(entry.Message)
	if entry.BodyOmitted {
		message.Text = "[message text unavailable]"
	}
	return &agentMessage{Text: message.Text, TS: message.TS}
}

func writeOperatorAgent(w io.Writer, agent *operatorAgent) {
	connection := "disconnected"
	if agent.Connected {
		connection = "connected"
	}
	state, work := "undeclared", ""
	if agent.State != nil {
		state = *agent.State
	}
	if agent.Status != nil {
		work = *agent.Status
	}
	fmt.Fprintf(w, "%s %s %s %s\n", preview(agent.ID, 128), connection, preview(state, 80), work)
	if agent.LastMessage != nil {
		fmt.Fprintf(w, "  last: %s %s\n", messageAge(agent.LastMessage.TS), agent.LastMessage.Text)
	}
}

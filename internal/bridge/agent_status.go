package bridge

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pairtask/pairtask/internal/protocol"
)

// statusWait bounds one status request; there is no retry.
const statusWait = 10 * time.Second

// StatusArgs is a declaration from the model.
type StatusArgs struct {
	State     string `json:"state" jsonschema:"active, waiting, blocked, idle or done"`
	Task      string `json:"task,omitempty" jsonschema:"What you are working on, at most 120 bytes"`
	Step      string `json:"step,omitempty" jsonschema:"The current step, at most 120 bytes"`
	WaitingOn string `json:"waiting_on,omitempty" jsonschema:"Empty, or operator, peer, ci or tools"`
	Ref       string `json:"ref,omitempty" jsonschema:"A reference such as a PR, head or issue, at most 120 bytes"`
}

// statusSchema is StatusArgs' schema with its choices published: state and
// waiting_on as enums from the protocol, so a client sees what is valid
// before it asks, and the free text bounded as the broker bounds it. The
// broker still checks every value, in bytes.
func statusSchema() *jsonschema.Schema {
	schema, err := jsonschema.For[StatusArgs](nil)
	if err != nil {
		panic(err) // a fixed type; it can't fail at run time
	}
	schema.Properties["state"].Enum = choices(protocol.StatusStates)
	schema.Properties["waiting_on"].Enum = choices(protocol.StatusWaiting)
	for _, name := range []string{"task", "step", "ref"} {
		limit := protocol.StatusFieldBytes
		schema.Properties[name].MaxLength = &limit
	}
	schema.Required = []string{"state"}
	return schema
}

func choices(values []string) []any {
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = v
	}
	return out
}

// StatusResult is what both status tools return.
type StatusResult struct {
	AgentStatus *protocol.AgentStatus `json:"agent_status"`
}

type statusClient interface {
	SetStatus(context.Context, protocol.AgentStatus) (*protocol.AgentStatus, error)
	ReadStatus(context.Context, string) (*protocol.AgentStatus, error)
}

func addStatusTools(s *mcp.Server, c statusClient) {
	mcp.AddTool(s, &mcp.Tool{Name: "status", InputSchema: statusSchema(), Description: "Declare what you are doing so the operator sees it without reading your terminal: call at task start, when the step changes, when you block or wait, and when done. Not a heartbeat, not a message, not an acknowledgment or approval. Returns the broker-confirmed record; replaces the previous one."},
		func(ctx context.Context, _ *mcp.CallToolRequest, args StatusArgs) (*mcp.CallToolResult, StatusResult, error) {
			st, err := c.SetStatus(ctx, protocol.AgentStatus{State: args.State, Task: args.Task, Step: args.Step, WaitingOn: args.WaitingOn, Ref: args.Ref})
			if err != nil {
				return nil, StatusResult{}, err
			}
			return nil, StatusResult{AgentStatus: st}, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "read_status", Description: "Read an agent's latest declared status (default: your own). Returns null when none was declared; nothing is inferred. Not for polling."},
		func(ctx context.Context, _ *mcp.CallToolRequest, args struct {
			AgentID string `json:"agent_id,omitempty" jsonschema:"Agent ID to read; empty for yourself"`
		}) (*mcp.CallToolResult, StatusResult, error) {
			st, err := c.ReadStatus(ctx, args.AgentID)
			if err != nil {
				return nil, StatusResult{}, err
			}
			return nil, StatusResult{AgentStatus: st}, nil
		})
}

var errStatusUnsupported = errors.New("agent status is unsupported by this broker (protocol version 6 required); upgrade the broker")

// SetStatus declares a status and returns the broker-confirmed record.
func (c *Client) SetStatus(ctx context.Context, st protocol.AgentStatus) (*protocol.AgentStatus, error) {
	return c.statusRequest(ctx, protocol.Message{Type: protocol.TypeAgentStatusSet, AgentStatus: &st})
}

// ReadStatus reads an agent's latest record; nil when none was declared.
func (c *Client) ReadStatus(ctx context.Context, agent string) (*protocol.AgentStatus, error) {
	return c.statusRequest(ctx, protocol.Message{Type: protocol.TypeAgentStatusGet, AgentID: agent})
}

// statusRequest sends one correlated request and waits for its own answer,
// which never enters the inbox.
func (c *Client) statusRequest(ctx context.Context, msg protocol.Message) (*protocol.AgentStatus, error) {
	if c.protocolVersion < protocol.AgentStatusVersion {
		return nil, errStatusUnsupported
	}
	msg.RequestID = uuid.NewString()
	reply, err := c.expect(msg.RequestID)
	if err != nil {
		return nil, err
	}
	defer c.forget(msg.RequestID)
	if err := c.writeFrame(ctx, msg); err != nil {
		return nil, err
	}
	out, err := c.awaitStatus(ctx, reply)
	if err != nil {
		return nil, err
	}
	return statusAnswer(out)
}

// awaitStatus waits once for the answer; a timeout is not retried.
func (c *Client) awaitStatus(ctx context.Context, reply chan protocol.Message) (protocol.Message, error) {
	timer := time.NewTimer(statusWait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return protocol.Message{}, ctx.Err()
	case <-c.done:
		return protocol.Message{}, c.connectionError()
	case <-timer.C:
		return protocol.Message{}, errors.New("status request timed out; not retried")
	case out := <-reply:
		return out, nil
	}
}

func statusAnswer(out protocol.Message) (*protocol.AgentStatus, error) {
	if out.Type == protocol.TypeError {
		return nil, fmt.Errorf("%s: %s", out.Code, out.Detail)
	}
	return out.AgentStatus, nil
}

// maxPendingStatus bounds concurrent status requests per connection.
const maxPendingStatus = 16

func (c *Client) expect(id string) (chan protocol.Message, error) {
	ch := make(chan protocol.Message, 1)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pending == nil {
		c.pending = map[string]chan protocol.Message{}
	}
	if len(c.pending) >= maxPendingStatus {
		return nil, errors.New("too many status requests in flight; retry when one completes")
	}
	c.pending[id] = ch
	return ch, nil
}

func (c *Client) forget(id string) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// routeResponse hands a status answer to its waiting request, first answer
// wins and never blocks the reader. It reports whether the frame was a status
// answer; such frames never enter the inbox, even when nobody waits anymore
// (a cancelled or timed-out request).
func (c *Client) routeResponse(msg protocol.Message) bool {
	if !isStatusAnswer(msg) {
		return false
	}
	c.mu.Lock()
	ch := c.pending[msg.RequestID]
	delete(c.pending, msg.RequestID)
	c.mu.Unlock()
	if ch != nil {
		select {
		case ch <- msg:
		default:
		}
	}
	return true
}

func (c *LazyClient) SetStatus(ctx context.Context, st protocol.AgentStatus) (*protocol.AgentStatus, error) {
	client, err := c.connection(ctx)
	if err != nil {
		return nil, err
	}
	return client.SetStatus(ctx, st)
}

func (c *LazyClient) ReadStatus(ctx context.Context, agent string) (*protocol.AgentStatus, error) {
	client, err := c.connection(ctx)
	if err != nil {
		return nil, err
	}
	return client.ReadStatus(ctx, agent)
}

// The listener owns the connection once activated; status calls use it
// without consuming its queue.
func (l *Listener) SetStatus(ctx context.Context, st protocol.AgentStatus) (*protocol.AgentStatus, error) {
	if _, err := l.Activate(ctx); err != nil {
		return nil, err
	}
	return l.LazyClient.SetStatus(ctx, st)
}

func (l *Listener) ReadStatus(ctx context.Context, agent string) (*protocol.AgentStatus, error) {
	if _, err := l.Activate(ctx); err != nil {
		return nil, err
	}
	return l.LazyClient.ReadStatus(ctx, agent)
}

// activitySpacing spaces activity reports from one connection; the broker
// coalesces as well.
const activitySpacing = 30 * time.Second

// ReportActivity tells the broker that the harness was seen working in this
// connection's own session. It never connects, waits or retries: without an
// active connection, or on a broker before protocol 6, it does nothing.
func (c *Client) ReportActivity(ctx context.Context, source string) error {
	if c.protocolVersion < protocol.AgentStatusVersion {
		return nil
	}
	now := time.Now()
	c.mu.Lock()
	recent := !c.lastActivity.IsZero() && now.Sub(c.lastActivity) < activitySpacing
	if !recent {
		c.lastActivity = now
	}
	c.mu.Unlock()
	if recent {
		return nil
	}
	return c.writeFrame(ctx, protocol.Message{Type: protocol.TypeAgentActivity, Source: source})
}

// ReportActivity reports only through an already active listener; activity
// never starts a broker session.
func (l *Listener) ReportActivity(ctx context.Context, source string) error {
	l.mu.Lock()
	started := l.started
	l.mu.Unlock()
	if !started {
		return nil
	}
	client, err := l.connection(ctx)
	if err != nil {
		return err
	}
	return client.ReportActivity(ctx, source)
}

// observeToolCalls reports "claude-adapter" activity when the model calls one
// of this adapter's tools: pairtask tool activity, not general liveness.
// Listener deliveries, receipts and initialization never count. One report is
// in flight at most, off the request path, never cancelled midway.
func (l *Listener) observeToolCalls(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		result, err := next(ctx, method, req)
		if method == "tools/call" && l.reporting.CompareAndSwap(false, true) {
			go func() {
				defer l.reporting.Store(false)
				_ = l.ReportActivity(context.Background(), "claude-adapter")
			}()
		}
		return result, err
	}
}

// isStatusAnswer reports a status result or an error correlated by request_id.
// statusAnswers are the correlated answers to an agent's own requests.
var statusAnswers = map[string]bool{protocol.TypeAgentStatusResult: true, protocol.TypeClaimResult: true, protocol.TypeRoomResult: true,
	protocol.TypeReviewResult: true, protocol.TypeWorkspacesResult: true, protocol.TypeReviewsAskedResult: true}

func isStatusAnswer(msg protocol.Message) bool {
	if statusAnswers[msg.Type] {
		return true
	}
	return msg.Type == protocol.TypeError && msg.RequestID != ""
}

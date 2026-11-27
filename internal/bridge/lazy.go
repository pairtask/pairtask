package bridge

import (
	"context"
	"errors"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

// LazyClient registers with the broker on the first messaging call, not during
// MCP discovery. Once connected, it keeps the same Client (including terminal
// errors and buffered messages); it never silently reconnects after a gap.
// A redialing one (NewRedialingClient) reconnects only after a call has
// reported the gap.
type LazyClient struct {
	gate       chan struct{}
	dial       func(context.Context) (*Client, error)
	client     *Client
	closed     bool
	delegation *listenerDelegation
	redial     bool // reconnect once a gap was reported
	gapShown   bool // a call has returned the broken connection's error
}

// NewRedialingClient is a LazyClient for a broker that can move, as the
// Desktop node's does when another copy takes it over: after the
// connection breaks, the next call still answers with that error, so the
// gap is never silent, and the call after it registers again, which
// replays accepted unacknowledged durable messages.
func NewRedialingClient(cfg ClientConfig) (*LazyClient, error) {
	c, err := NewLazyClient(cfg)
	if err == nil {
		c.redial = true
	}
	return c, err
}

func NewLazyClient(cfg ClientConfig) (*LazyClient, error) {
	if err := protocol.ValidateAgentID(cfg.AgentID); err != nil {
		return nil, err
	}
	return &LazyClient{
		gate: make(chan struct{}, 1),
		dial: func(ctx context.Context) (*Client, error) {
			return Dial(ctx, cfg)
		},
	}, nil
}

// connection serializes the initial dial while allowing waiting callers to
// cancel. A failed initial dial can be retried on a later tool call.
func (c *LazyClient) connection(ctx context.Context) (*Client, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case c.gate <- struct{}{}:
	}
	defer func() { <-c.gate }()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.closed {
		return nil, errors.New("MCP connection closed")
	}
	c.dropAfterGap()
	if c.client == nil {
		client, err := c.dial(ctx)
		if err != nil {
			return nil, err
		}
		c.client = client
	}
	return c.client, nil
}

// dropAfterGap forgets a broken connection whose error a call has
// already returned, when this client redials; the gate is held.
func (c *LazyClient) dropAfterGap() {
	if !c.redial || c.client == nil || c.client.connectionError() == nil {
		return
	}
	if !c.gapShown {
		c.gapShown = true // this call answers with the gap
		return
	}
	c.client.Close()
	c.client, c.gapShown = nil, false
}

func (c *LazyClient) Send(ctx context.Context, to, text string) error {
	client, err := c.connection(ctx)
	if err != nil {
		return err
	}
	return client.Send(ctx, to, text)
}

func (c *LazyClient) Receive(ctx context.Context, limit int, wait time.Duration) (Inbox, error) {
	client, err := c.connection(ctx)
	if err != nil {
		return Inbox{}, err
	}
	return client.Receive(ctx, limit, wait)
}

// Close also handles discovery-only processes, which never opened a socket.
func (c *LazyClient) Close() {
	c.gate <- struct{}{}
	defer func() { <-c.gate }()
	c.closed = true
	if c.delegation != nil {
		c.delegation.Close()
	}
	if c.client != nil {
		c.client.Close()
	}
}

func (c *LazyClient) SendMessage(ctx context.Context, request SendRequest) (SendResult, error) {
	client, err := c.connection(ctx)
	if err != nil {
		return SendResult{}, err
	}
	return client.SendMessage(ctx, request)
}

func (c *LazyClient) Acknowledge(ctx context.Context, id string) error {
	client, err := c.connection(ctx)
	if err != nil {
		return err
	}
	return client.Acknowledge(ctx, id)
}

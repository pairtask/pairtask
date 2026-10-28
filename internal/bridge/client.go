// Package bridge connects an MCP process to the broker as one persistent agent.
package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/pairtask/pairtask/internal/forge"
	"github.com/pairtask/pairtask/internal/protocol"
)

const (
	ioTimeout        = 5 * time.Second
	maxInboxMessages = 256
	maxInboxBytes    = 4 << 20
)

type queuedMessage struct {
	message protocol.Message
	size    int
	cursor  uint64
}

// Client reads the socket continuously, independently of MCP tool calls.
// A broken connection is terminal. Explicit restart recovers durable pending work; other frames remain ephemeral.
type Client struct {
	conn            net.Conn
	writeGate       chan struct{}
	notify          chan struct{}
	done            chan struct{}
	readDone        chan struct{}
	mu              sync.Mutex
	inbox           []queuedMessage
	inboxBytes      int
	nextCursor      uint64
	changed         chan struct{}
	err             error
	sessionID       string
	protocolVersion int
	pending         map[string]chan protocol.Message // status requests by request_id
	lastActivity    time.Time                        // last agent_activity written
	forges          forge.Registry                   // review adapters on this side (#201); none means reviews are refused before any forge work
}

type Inbox struct {
	Messages                 []protocol.Message `json:"messages"`
	Connected                bool               `json:"connected"`
	Error                    string             `json:"error,omitempty"`
	TimedOut                 bool               `json:"timed_out,omitempty"`
	SessionID                string             `json:"session_id,omitempty"`
	AcknowledgmentsSupported bool               `json:"acknowledgments_supported"`
	DurabilitySupported      bool               `json:"durability_supported"`
	ReceiptsDropped          uint64             `json:"receipts_dropped,omitempty"`
}

type ClientConfig struct {
	SocketPath string
	AgentID    string
	Harness    string
	Model      string
	Rooms      []string       // v9: rooms to join on registration
	Forges     forge.Registry // #201: the forge adapters reviews may use on this side
}

// Dial registers the agent before returning. The caller must call Close.
func Dial(ctx context.Context, cfg ClientConfig) (*Client, error) {
	if err := protocol.ValidateAgentID(cfg.AgentID); err != nil {
		return nil, err
	}
	dialer := net.Dialer{Timeout: ioTimeout}
	conn, err := dialer.DialContext(ctx, "unix", cfg.SocketPath)
	if err != nil {
		return nil, fmt.Errorf("connect to broker: %w", err)
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	conn.SetDeadline(time.Now().Add(ioTimeout))
	if err := protocol.WriteFrame(conn, protocol.Message{Type: protocol.TypeHello, AgentID: cfg.AgentID, Harness: cfg.Harness, Model: cfg.Model, ProtocolVersion: protocol.Version, Rooms: cfg.Rooms}); err != nil {
		conn.Close()
		return nil, fmt.Errorf("send hello: %w", err)
	}
	r := bufio.NewReader(conn)
	var welcome protocol.Message
	if err := protocol.ReadFrame(r, &welcome); err != nil {
		conn.Close()
		return nil, fmt.Errorf("read welcome: %w", err)
	}
	if welcome.Type != protocol.TypeWelcome || welcome.AgentID != cfg.AgentID {
		conn.Close()
		return nil, fmt.Errorf("unexpected welcome: type=%s code=%s detail=%s", welcome.Type, welcome.Code, welcome.Detail)
	}
	conn.SetDeadline(time.Time{})
	c := &Client{conn: conn, writeGate: make(chan struct{}, 1), notify: make(chan struct{}, 1), done: make(chan struct{}), readDone: make(chan struct{}),
		forges: cfg.Forges}
	c.sessionID = welcome.SessionID
	c.protocolVersion = welcome.ProtocolVersion
	go c.readLoop(r)
	return c, nil
}

func (c *Client) readLoop(r *bufio.Reader) {
	defer close(c.readDone)
	for {
		var msg protocol.Message
		if err := protocol.ReadFrame(r, &msg); err != nil {
			c.fail(fmt.Errorf("broker connection closed: %w; restart the adapter to reconnect; accepted unacknowledged durable messages replay on v3; other frames may be lost", err))
			return
		}
		if c.routeResponse(msg) {
			continue
		}
		if err := c.bufferMessage(protocol.WithDisplayOrigin(msg)); err != nil {
			c.fail(err)
			return
		}
	}
}

func (c *Client) bufferMessage(msg protocol.Message) error {
	data, _ := json.Marshal(msg)
	c.mu.Lock()
	full := len(c.inbox) >= maxInboxMessages || c.inboxBytes+len(data) > maxInboxBytes
	c.mu.Unlock()
	if full {
		return errors.New("inbox overflow; connection closed and messages may be missing; drain the inbox and restart the MCP server")
	}
	// Only the read loop adds messages; concurrent consumers can only free space.
	// Send receipt before exposing the decoded message so agent acknowledgment
	// cannot overtake adapter receipt on the stream. Retain it even if writing fails.
	receiptErr := c.sendAdapterReceipt(msg)
	c.mu.Lock()
	c.nextCursor++
	c.inbox = append(c.inbox, queuedMessage{message: msg, size: len(data), cursor: c.nextCursor})
	c.inboxBytes += len(data)
	c.signalObserversLocked()
	c.mu.Unlock()
	select {
	case c.notify <- struct{}{}:
	default:
	}
	return receiptErr
}

func (c *Client) sendAdapterReceipt(msg protocol.Message) error {
	if c.protocolVersion < protocol.AcknowledgmentVersion {
		return nil
	}
	if msg.Type != protocol.TypeMsg || !msg.AckRequested {
		return nil
	}
	return c.writeFrame(context.Background(), protocol.Message{Type: protocol.TypeAck, MessageID: msg.MessageID, Stage: protocol.StageAdapterReceived})
}

func (c *Client) fail(err error) {
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return
	}
	c.err = err
	c.signalObserversLocked()
	close(c.done)
	c.mu.Unlock()
	c.conn.Close()
}

func (c *Client) Close() {
	c.fail(errors.New("MCP connection closed"))
	<-c.readDone
}

// Send is a write-only convenience for legacy clients that do not request
// acknowledgments. MCP uses SendMessage for correlated, staged acknowledgment.
func (c *Client) Send(ctx context.Context, to, text string) error {
	if to == "" {
		return errors.New("to is required (agent ID or *)")
	}
	if text == "" {
		return errors.New("text must not be empty")
	}
	payload, err := json.Marshal(struct {
		Text string `json:"text"`
	}{text})
	if err != nil {
		return err
	}
	msg := protocol.Message{Type: protocol.TypeMsg, To: to, Payload: payload}
	return c.writeFrame(ctx, msg)
}

type SendResult struct {
	Status            string `json:"status"`
	MessageID         string `json:"message_id"`
	To                string `json:"to"`
	DeliveryConfirmed bool   `json:"delivery_confirmed"`
	DurableRequested  bool   `json:"durable_requested"`
}

// SendMessage assigns an ID before writing. Acceptance and receipt events arrive
// through Receive; neither a successful write nor a timeout implies delivery.
func (c *Client) SendMessage(ctx context.Context, request SendRequest) (SendResult, error) {
	if err := c.checkPublic(request); err != nil {
		return SendResult{}, err
	}
	if err := c.checkRoom(request); err != nil {
		return SendResult{}, err
	}
	if !request.NonDurable && c.protocolVersion < protocol.DurableVersion {
		return SendResult{}, errors.New("durable delivery requires protocol version 3; upgrade the broker or explicitly set non_durable")
	}
	if c.protocolVersion < protocol.AcknowledgmentVersion {
		return SendResult{}, errors.New("broker does not support protocol version 2 acknowledgments; upgrade the broker")
	}
	msg, err := request.message()
	if err != nil {
		return SendResult{}, err
	}
	out := SendResult{Status: "written", MessageID: msg.MessageID, To: msg.To, DurableRequested: msg.Durable}
	if err := c.writeFrame(ctx, msg); err != nil {
		out.Status = "unconfirmed"
		return out, fmt.Errorf("message %s: %w; broker acceptance is unconfirmed", msg.MessageID, err)
	}
	return out, nil
}

// checkPublic refuses a "*" this bridge would send as agent-only: on a broker
// below v7 the operator would never see it. Direct sends are unaffected.
func (c *Client) checkPublic(request SendRequest) error {
	if request.To != protocol.Broadcast {
		return nil
	}
	if c.protocolVersion < protocol.PublicBroadcastVersion {
		return fmt.Errorf("unsupported public broadcast: the broker negotiated protocol version %d, and \"*\" reaches the operator only from version %d; upgrade the broker, or send to operator and to agents directly", c.protocolVersion, protocol.PublicBroadcastVersion)
	}
	if request.NonDurable {
		return errors.New("a public broadcast is always durable; remove non_durable")
	}
	return nil
}

func (c *Client) Acknowledge(ctx context.Context, id string) error {
	if c.protocolVersion < protocol.AcknowledgmentVersion {
		return errors.New("broker does not support acknowledgments; upgrade the broker")
	}
	if id == "" || len(id) > 128 {
		return errors.New("message_id must be 1 to 128 bytes")
	}
	return c.writeFrame(ctx, protocol.Message{Type: protocol.TypeAck, MessageID: id, Stage: protocol.StageAgentAcknowledged})
}

func (c *Client) writeFrame(ctx context.Context, msg protocol.Message) error {
	if err := validateFrameSize(msg); err != nil {
		return err
	}
	if err := c.acquireWriter(ctx); err != nil {
		return err
	}
	defer func() { <-c.writeGate }()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.connectionError(); err != nil {
		return err
	}
	return c.writeWithDeadline(ctx, msg)
}

func validateFrameSize(msg protocol.Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if len(data)+1 > protocol.MaxFrameBytes {
		return errors.New("message exceeds the 1 MiB frame limit")
	}
	return nil
}

func (c *Client) acquireWriter(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return c.connectionError()
	case c.writeGate <- struct{}{}:
		return nil
	}
}

func (c *Client) writeWithDeadline(ctx context.Context, msg protocol.Message) error {
	deadline := time.Now().Add(ioTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := c.conn.SetWriteDeadline(deadline); err != nil {
		c.fail(err)
		return err
	}
	// Cancellation during a write closes the stream: a partial frame cannot be retried safely.
	stop := context.AfterFunc(ctx, func() { c.fail(ctx.Err()) })
	defer stop()
	if err := protocol.WriteFrame(c.conn, msg); err != nil {
		c.fail(err)
		return err
	}
	return nil
}

func (c *Client) connectionError() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Receive consumes up to limit queued frames, waiting only when the inbox is empty.
func (c *Client) Receive(ctx context.Context, limit int, wait time.Duration) (Inbox, error) {
	if limit < 1 || limit > 100 {
		return Inbox{}, errors.New("limit must be between 1 and 100")
	}
	if wait < 0 || wait > 30*time.Second {
		return Inbox{}, errors.New("wait must be between 0 and 30 seconds")
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return Inbox{}, err
		}
		c.mu.Lock()
		out := Inbox{Messages: make([]protocol.Message, 0), Connected: c.err == nil, SessionID: c.sessionID, AcknowledgmentsSupported: c.protocolVersion >= protocol.AcknowledgmentVersion, DurabilitySupported: c.protocolVersion >= protocol.DurableVersion}
		if c.err != nil {
			out.Error = c.err.Error()
		}
		n := min(limit, len(c.inbox))
		for _, q := range c.inbox[:n] {
			out.Messages = append(out.Messages, q.message)
			c.inboxBytes -= q.size
		}
		clear(c.inbox[:n])
		c.inbox = c.inbox[n:]
		c.mu.Unlock()
		if len(out.Messages) > 0 || !out.Connected || wait == 0 {
			return out, nil
		}
		select {
		case <-ctx.Done():
			return Inbox{}, ctx.Err()
		case <-c.done:
		case <-c.notify:
		case <-timer.C:
			// Check the queue once more so a simultaneous delivery is not left behind.
			out, err := c.Receive(ctx, limit, 0)
			out.TimedOut = len(out.Messages) == 0 && out.Connected
			return out, err
		}
	}
}

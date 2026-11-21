package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/signing"
)

// Publisher submits peer data to a host. Success is never an agent acknowledgment.
type Publisher interface {
	Publish(context.Context, protocol.Message) error
}

type ListenerStatus struct {
	State                    string `json:"state"`
	SessionID                string `json:"session_id,omitempty"`
	Submitted                int    `json:"submitted"`
	LastMessageID            string `json:"last_message_id,omitempty"`
	Error                    string `json:"error,omitempty"`
	ManualCheckRequired      bool   `json:"manual_check_required"`
	AcknowledgmentsSupported bool   `json:"acknowledgments_supported"`
	DurabilitySupported      bool   `json:"durability_supported"`
	BufferedFrames           int    `json:"buffered_frames"`
	BufferedBytes            int    `json:"buffered_bytes"`
	ReceiptsDropped          uint64 `json:"receipts_dropped"`
}

// Listener is the sole broker consumer. Tools drain its bounded copy, never race
// the publisher for broker frames. Restart explicitly to recover durable frames.
type Listener struct {
	*LazyClient
	host    Publisher
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}
	notify  chan struct{}
	changed chan struct{}
	mu      sync.Mutex
	started bool
	status  ListenerStatus
	queue   []queuedMessage
	bytes   int
	// reporting is set while a tool-call activity report is in flight.
	reporting atomic.Bool
}

func NewListener(ctx context.Context, client *LazyClient, host Publisher) *Listener {
	ctx, cancel := context.WithCancel(ctx)
	return &Listener{LazyClient: client, host: host, ctx: ctx, cancel: cancel,
		done: make(chan struct{}), notify: make(chan struct{}, 1),
		status: ListenerStatus{State: "inactive", ManualCheckRequired: true}}
}

// Activate is called by an explicit tool or an opted-in host readiness handler.
// Constructing the server alone never dials.
func (l *Listener) Activate(ctx context.Context) (ListenerStatus, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.started {
		return l.statusLocked(), nil
	}
	if err := l.ctx.Err(); err != nil {
		return l.status, err
	}
	c, err := l.connection(ctx)
	if err != nil {
		l.status.Error = err.Error()
		return l.status, err
	}
	l.started = true
	l.status.State = "listening_delivery_unconfirmed"
	l.status.Error = ""
	l.status.SessionID = c.sessionID
	l.status.AcknowledgmentsSupported = c.protocolVersion >= protocol.AcknowledgmentVersion
	l.status.DurabilitySupported = c.protocolVersion >= protocol.DurableVersion
	go l.pump(c)
	return l.statusLocked(), nil
}

func (l *Listener) Status() ListenerStatus {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.statusLocked()
}

func (l *Listener) pump(c *Client) {
	defer close(l.done)
	for {
		inbox, err := c.Receive(l.ctx, 1, 30*time.Second)
		if err != nil {
			l.fail(err)
			return
		}
		if err := l.deliver(inbox.Messages); err != nil {
			l.fail(err)
			return
		}
		if !inbox.Connected {
			l.fail(errors.New(inbox.Error))
			return
		}
	}
}

func (l *Listener) deliver(messages []protocol.Message) error {
	for _, msg := range messages {
		if err := l.retain(msg); err != nil {
			return err
		}
		if unpublished[msg.Type] || quiet(msg) {
			continue
		}
		ctx, cancel := context.WithTimeout(l.ctx, ioTimeout)
		err := l.host.Publish(ctx, msg)
		cancel()
		if err != nil {
			return err
		}
		l.mu.Lock()
		l.status.Submitted++
		l.status.LastMessageID = msg.MessageID
		l.mu.Unlock()
		if err := l.reportSummary(msg); err != nil {
			return err
		}
	}
	return nil
}

// unpublished are the frames kept for receive and never submitted to the
// host: receipts, and the answer to this adapter's own summary receipt.
var unpublished = map[string]bool{protocol.TypeAck: true, protocol.TypeAttentionSubmittedResult: true}

// reportSummary tells the broker the host was shown a DND summary (#422),
// only after it was: the broker then stops owing it. The receipt names
// the generation and nothing else; it acknowledges no message.
func (l *Listener) reportSummary(msg protocol.Message) error {
	if msg.Type != protocol.TypeAttentionSummary || msg.AttentionSummary == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(l.ctx, ioTimeout)
	defer cancel()
	c, err := l.connection(ctx)
	if err != nil {
		return err
	}
	return c.writeFrame(ctx, protocol.Message{Type: protocol.TypeAttentionSubmitted, AttentionGeneration: msg.AttentionSummary.Generation})
}

// quiet: the broker says this message must not interrupt (#422). A
// suppressed one is kept for receive and never submitted to the host; an
// urgent one interrupts only as the local operator's, and never with a
// seal that doesn't hold. A message without a disposition notifies.
func quiet(msg protocol.Message) bool {
	switch msg.Attention {
	case protocol.AttentionSuppressed:
		return true
	case protocol.AttentionUrgent:
		return msg.Origin != protocol.OriginOperator || invalidSeal(msg)
	}
	return false
}

// invalidSeal: this adapter judged the message's seal and it doesn't hold.
func invalidSeal(msg protocol.Message) bool {
	return msg.Verification != nil && msg.Verification.State == signing.Invalid
}

func (l *Listener) retain(msg protocol.Message) error {
	data, _ := json.Marshal(msg)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.releaseAcknowledgedLocked(msg)
	if !l.makeRoomLocked(len(data)) {
		if msg.Type == protocol.TypeAck {
			l.status.ReceiptsDropped++
			return nil
		}
		return errors.New("listener inbox overflow (frame or byte limit); drain receive and restart; messages may be missing")
	}
	l.queue = append(l.queue, queuedMessage{message: msg, size: len(data)})
	l.bytes += len(data)
	l.signalLocked()
	return nil
}

func (l *Listener) signalLocked() {
	if l.changed != nil {
		close(l.changed)
		l.changed = nil
	}
	select {
	case l.notify <- struct{}{}:
	default:
	}
}

func (l *Listener) fail(err error) {
	l.mu.Lock()
	l.status.State = "disconnected"
	l.status.Error = err.Error() + "; restart required; accepted unacknowledged durable messages replay on v3; other frames may be lost"
	l.signalLocked()
	l.mu.Unlock()
	if l.ctx.Err() == nil {
		log.Printf("collab listener disconnected: %s", l.Status().Error)
	}
	l.LazyClient.Close()
}

func (l *Listener) Close() {
	l.cancel()
	l.LazyClient.Close()
	l.mu.Lock()
	started := l.started
	l.status.State = "stopped"
	l.signalLocked()
	l.mu.Unlock()
	if started {
		<-l.done
	}
	l.mu.Lock()
	l.status.State = "stopped"
	l.signalLocked()
	l.mu.Unlock()
}

// Receive retains the polling fallback even when a channel is silently ignored.
func (l *Listener) Receive(ctx context.Context, limit int, wait time.Duration) (Inbox, error) {
	if err := validateListenerRead(limit, wait); err != nil {
		return Inbox{}, err
	}
	if err := ctx.Err(); err != nil {
		return Inbox{}, err
	}
	if _, err := l.Activate(ctx); err != nil {
		return Inbox{}, err
	}
	if wait == 0 {
		return l.drain(limit), nil
	}
	return l.waitForInbox(ctx, limit, wait)
}

func validateListenerRead(limit int, wait time.Duration) error {
	if limit < 1 || limit > 100 {
		return errors.New("limit must be between 1 and 100")
	}
	if wait < 0 || wait > 30*time.Second {
		return errors.New("wait must be between 0 and 30 seconds")
	}
	return nil
}

func (l *Listener) waitForInbox(ctx context.Context, limit int, wait time.Duration) (Inbox, error) {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return Inbox{}, err
		}
		out := l.drain(limit)
		if len(out.Messages) > 0 || !out.Connected {
			return out, nil
		}
		select {
		case <-ctx.Done():
			return Inbox{}, ctx.Err()
		case <-l.notify:
		case <-timer.C:
			return l.timedOutInbox(limit), nil
		}
	}
}

func (l *Listener) timedOutInbox(limit int) Inbox {
	out := l.drain(limit)
	out.TimedOut = len(out.Messages) == 0 && out.Connected
	return out
}

func (l *Listener) drain(limit int) Inbox {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := l.inboxLocked()
	n := min(limit, len(l.queue))
	for _, q := range l.queue[:n] {
		out.Messages = append(out.Messages, q.message)
		l.bytes -= q.size
	}
	clear(l.queue[:n])
	l.queue = l.queue[n:]
	return out
}

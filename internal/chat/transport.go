// Package chat implements the interactive operator chat over a live operator
// session. All socket writes happen in Bubble Tea commands, never in Update.
package chat

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Transport is one open operator session connection.
type Transport interface {
	// Request writes the frame and returns its request_id. The model assigns
	// IDs itself so a response can't arrive before its request is recorded;
	// Request assigns one only when the message has none.
	Request(protocol.Message) (string, error)
	// Incoming carries responses and events; it closes on disconnect.
	Incoming() <-chan protocol.Message
	Close() error
}

// Dialer opens the operator socket and an operator session.
type Dialer func(ctx context.Context) (Transport, protocol.SessionInfo, error)

// OwnerBusyError reports that another session owns the operator inbox.
type OwnerBusyError struct {
	Owner protocol.SessionInfo
}

func (e *OwnerBusyError) Error() string {
	return fmt.Sprintf("another operator session (%q, opened %s) owns the inbox", e.Owner.SessionID, e.Owner.OpenedAt.Format(time.RFC3339))
}

// ErrMalformed marks a broker response the chat could not accept.
var ErrMalformed = errors.New("malformed broker response")

// requiredCapabilities are the session features the chat depends on.
var requiredCapabilities = []string{"events", "snapshot", "history", "message_stages"}

// SocketDialer connects to the operator socket at path.
func SocketDialer(path string, timeout time.Duration) Dialer {
	return func(ctx context.Context) (Transport, protocol.SessionInfo, error) {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		var d net.Dialer
		conn, err := d.DialContext(ctx, "unix", path)
		if err != nil {
			return nil, protocol.SessionInfo{}, err
		}
		t, info, err := openSession(ctx, conn)
		if err != nil {
			conn.Close()
		}
		return t, info, err
	}
}

// openSession runs the open handshake; cancelling ctx interrupts it.
func openSession(ctx context.Context, conn net.Conn) (Transport, protocol.SessionInfo, error) {
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}
	stop := context.AfterFunc(ctx, func() { conn.SetDeadline(time.Now()) })
	t := &socketTransport{conn: conn, r: bufio.NewReader(conn), incoming: make(chan protocol.Message, 64), done: make(chan struct{})}
	resp, err := t.handshake()
	if !stop() {
		return nil, protocol.SessionInfo{}, ctx.Err()
	}
	if err != nil {
		return nil, protocol.SessionInfo{}, err
	}
	conn.SetDeadline(time.Time{})
	info, err := sessionInfo(resp)
	if err != nil {
		return nil, info, err
	}
	go t.read()
	return t, info, nil
}

func (t *socketTransport) handshake() (protocol.Message, error) {
	var resp protocol.Message
	// The chat reads every pairing's history, tagged (#276), and where a
	// request's change moved and what replaced it (#259).
	open := protocol.Message{Type: protocol.TypeOperatorSessionOpen, RequestID: "open", Client: "chat", ProtocolVersion: protocol.ReviewSupersedeVersion}
	if err := t.write(open); err != nil {
		return resp, err
	}
	err := protocol.ReadFrame(t.r, &resp)
	return resp, err
}

// sessionInfo validates the open response and the capability handshake.
func sessionInfo(resp protocol.Message) (protocol.SessionInfo, error) {
	if resp.Type == protocol.TypeError && resp.RequestID == "open" {
		return protocol.SessionInfo{}, openRejected(resp)
	}
	if !validOpen(resp) {
		return protocol.SessionInfo{}, fmt.Errorf("%w: unexpected open response", ErrMalformed)
	}
	if want := missingCapability(resp.Session.Capabilities); want != "" {
		return protocol.SessionInfo{}, fmt.Errorf("broker lacks the %q capability; upgrade the sandbox", want)
	}
	return *resp.Session, nil
}

// openRejected reports owner_busy distinctly from other open errors.
func openRejected(resp protocol.Message) error {
	if resp.Code == protocol.ErrOwnerBusy && resp.Owner != nil {
		return &OwnerBusyError{Owner: *resp.Owner}
	}
	return fmt.Errorf("open rejected: %q: %q", resp.Code, resp.Detail)
}

func missingCapability(caps []string) string {
	for _, want := range requiredCapabilities {
		if !hasCapability(caps, want) {
			return want
		}
	}
	return ""
}

func validOpen(resp protocol.Message) bool {
	return resp.Type == protocol.TypeOperatorSession && resp.RequestID == "open" && resp.Session != nil &&
		resp.Session.SessionID != "" && resp.Session.Epoch != ""
}

func hasCapability(caps []string, want string) bool {
	for _, c := range caps {
		if c == want {
			return true
		}
	}
	return false
}

type socketTransport struct {
	conn     net.Conn
	r        *bufio.Reader
	mu       sync.Mutex
	next     atomic.Uint64
	incoming chan protocol.Message
	err      atomic.Value
	done     chan struct{}
	once     sync.Once
}

// Err reports why the connection closed: a malformed frame wraps ErrMalformed.
func (t *socketTransport) Err() error {
	err, _ := t.err.Load().(error)
	return err
}

func (t *socketTransport) Request(msg protocol.Message) (string, error) {
	if msg.RequestID == "" {
		msg.RequestID = "t" + strconv.FormatUint(t.next.Add(1), 10)
	}
	return msg.RequestID, t.write(msg)
}

func (t *socketTransport) write(msg protocol.Message) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return protocol.WriteFrame(t.conn, msg)
}

func (t *socketTransport) Incoming() <-chan protocol.Message { return t.incoming }

func (t *socketTransport) Close() error {
	t.once.Do(func() { close(t.done) })
	return t.conn.Close()
}

// read forwards frames until the connection closes; idle reading never times out.
func (t *socketTransport) read() {
	defer close(t.incoming)
	for {
		var msg protocol.Message
		if err := protocol.ReadFrame(t.r, &msg); err != nil {
			t.err.Store(closeReason(err))
			return
		}
		select {
		case t.incoming <- msg:
		case <-t.done: // The consumer is gone; don't block forever.
			return
		}
	}
}

// closeReason separates a malformed frame from an ordinary disconnect.
func closeReason(err error) error {
	if isDisconnect(err) {
		return err
	}
	return fmt.Errorf("%w: %v", ErrMalformed, err)
}

func isDisconnect(err error) bool {
	var netErr *net.OpError
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return true
	}
	return errors.As(err, &netErr)
}

// transportErr returns a transport's close reason when it reports one.
func transportErr(t Transport) error {
	if r, ok := t.(interface{ Err() error }); ok {
		return r.Err()
	}
	return nil
}

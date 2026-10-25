package chat

import (
	"context"
	"errors"
	"io"
	"strconv"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Bounds limit what the chat keeps and asks for at once.
type Bounds struct {
	Intents        int
	InFlight       int
	ReceiptBatch   int
	CatchupPages   int
	CatchupEntries int
	Entries        int
	Bytes          int
}

// DefaultBounds are the contract defaults.
func DefaultBounds() Bounds {
	return Bounds{Intents: 1, InFlight: 4, ReceiptBatch: 32, CatchupPages: 8, CatchupEntries: 256, Entries: 1024, Bytes: 32 << 20}
}

// BackoffPolicy bounds reconnect delays: Base doubles up to Max, and resets
// once a connection stayed healthy for Healthy.
type BackoffPolicy struct {
	Base, Max, Healthy time.Duration
}

func defaultBackoff() BackoffPolicy {
	return BackoffPolicy{Base: 500 * time.Millisecond, Max: 30 * time.Second, Healthy: time.Minute}
}

// Options configure a chat model.
type Options struct {
	Dial    Dialer
	Bounds  Bounds
	Now     func() time.Time
	Backoff BackoffPolicy
}

// Connection states.
const (
	Connecting   = "connecting"
	Connected    = "connected"
	Reconnecting = "reconnecting"
	OwnerBusy    = "owner_busy"
	Stopped      = "stopped"
	Closed       = "closed"
)

// Messages produced by the model's own commands.
type (
	DialedMsg struct {
		Gen       uint64
		Session   protocol.SessionInfo
		Err       error
		transport Transport
	}
	FrameMsg struct {
		Gen   uint64
		Frame protocol.Message
	}
	ClosedMsg struct {
		Gen uint64
		Err error
	}
	SentMsg struct {
		Gen       uint64
		RequestID string
		Err       error
	}
	RetryMsg struct {
		Gen uint64
	}
	receiptRetryMsg struct {
		Gen uint64
	}
)

// Model is the chat's Bubble Tea model.
type Model struct {
	opts Options

	conn      string
	gen       uint64
	transport Transport
	session   protocol.SessionInfo
	epoch     string
	revision  uint64
	baseline  bool
	failures  int
	healthyAt time.Time
	exitCode  int
	quitArmed bool
	fatal     string
	ctx       context.Context // cancelled on quit, ending any connection attempt
	cancel    context.CancelFunc

	nextReq int
	pending map[string]pendingRequest
	outbox  []protocol.Message

	timeline
	inbox    inboxState
	history  historyState
	send     sendState
	agents   agentView
	statuses statusView
	claims   claimView

	compose    composer
	conv       conversations
	recalling  *recall // Up in an empty draft walks sent messages back in
	panel      *panel
	view       viewport
	cutoff     uint64 // /clear: rows with seq <= cutoff are hidden
	find       string // the last /find query
	found      uint64 // seq of the last /find match
	keyboard   bool
	status     string
	bulk       *bulkAck              // a running /ack all or /ack from
	bulkSeq    int                   // numbers bulk runs for their request tokens
	usageDirty bool                  // an inbox change since the last usage read
	usage      *protocol.InboxStatus // the last inbox status read, for the header
	width      int
	height     int
}

type pendingRequest struct {
	kind  string
	ids   []string
	page  protocol.PageRequest
	epoch string // the broker epoch the request was made in
}

// New builds a chat model; it connects when the program starts.
func New(opts Options) *Model {
	if opts.Bounds == (Bounds{}) {
		opts.Bounds = DefaultBounds()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Backoff == (BackoffPolicy{}) {
		opts.Backoff = defaultBackoff()
	}
	m := &Model{opts: opts, conn: Connecting, pending: map[string]pendingRequest{}, width: 100, height: 30}
	m.ctx, m.cancel = context.WithCancel(context.Background())
	m.timeline = newTimeline()
	m.inbox.queuedAcks = map[string]*queuedAck{}
	m.inbox.quiet = map[string]bool{}
	m.compose = newComposer()
	m.conv.saved = map[viewKey]*view{}
	m.conv.hidden = map[viewKey]bool{}
	m.view.following = true
	m.resize(tea.WindowSizeMsg{Width: m.width, Height: m.height})
	return m
}

func (m *Model) Init() tea.Cmd {
	return m.dial()
}

// ExitCode is 1 when the chat could not open a session at startup.
func (m *Model) ExitCode() int { return m.exitCode }

func (m *Model) dial() tea.Cmd {
	m.gen++
	gen, dial, ctx := m.gen, m.opts.Dial, m.ctx
	return func() tea.Msg {
		t, info, err := dial(ctx)
		return DialedMsg{Gen: gen, Session: info, Err: err, transport: t}
	}
}

func listen(gen uint64, t Transport) tea.Cmd {
	return func() tea.Msg {
		frame, ok := <-t.Incoming()
		if !ok {
			return ClosedMsg{Gen: gen, Err: transportErr(t)}
		}
		return FrameMsg{Gen: gen, Frame: frame}
	}
}

// request records the request before its command writes it. Beyond the
// in-flight bound, requests wait in the outbox.
func (m *Model) request(kind string, msg protocol.Message, ids ...string) tea.Cmd {
	if m.transport == nil {
		return nil
	}
	m.nextReq++
	msg.RequestID = "q" + strconv.Itoa(m.nextReq)
	req := pendingRequest{kind: kind, ids: ids, epoch: m.epoch}
	if msg.PageRequest != nil {
		req.page = *msg.PageRequest
	}
	m.pending[msg.RequestID] = req
	if m.inFlight() > m.opts.Bounds.InFlight {
		m.outbox = append(m.outbox, msg)
		return nil
	}
	return m.write(msg)
}

// ready reports a connected session with a loaded baseline.
func (m *Model) ready() bool {
	return m.transport != nil && m.baseline
}

// requested reports whether a request of kind for id is in flight or queued.
func (m *Model) requested(kind, id string) bool {
	for _, req := range m.pending {
		if req.is(kind, id) {
			return true
		}
	}
	return false
}

func (r pendingRequest) is(kind, id string) bool {
	if r.kind != kind || len(r.ids) == 0 {
		return false
	}
	return r.ids[0] == id
}

func (m *Model) inFlight() int {
	return len(m.pending) - len(m.outbox)
}

func (m *Model) write(msg protocol.Message) tea.Cmd {
	gen, t := m.gen, m.transport
	return func() tea.Msg {
		id, err := t.Request(msg)
		return SentMsg{Gen: gen, RequestID: id, Err: err}
	}
}

// release frees a finished request's slot and writes the next queued one.
func (m *Model) release(id string) (pendingRequest, tea.Cmd) {
	req, ok := m.pending[id]
	if !ok {
		return pendingRequest{}, nil
	}
	delete(m.pending, id)
	if len(m.outbox) == 0 {
		return req, nil
	}
	next := m.outbox[0]
	m.outbox = m.outbox[1:]
	return req, m.write(next)
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if cmd, ok := m.connectionUpdate(msg); ok {
		return m, cmd
	}
	return m, m.terminalUpdate(msg)
}

// connectionUpdate handles broker connection messages.
func (m *Model) connectionUpdate(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case DialedMsg:
		return m.dialed(msg), true
	case FrameMsg:
		return m.frame(msg), true
	case ClosedMsg:
		return m.closed(msg.Gen, msg.Err), true
	case SentMsg:
		return m.sent(msg), true
	case RetryMsg:
		return m.retry(msg), true
	case receiptRetryMsg:
		return m.retryReceipts(msg), true
	case statusTick:
		return m.onStatusTick(), true
	}
	return nil, false
}

// terminalUpdate handles input and terminal messages.
func (m *Model) terminalUpdate(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.resize(msg)
	case tea.KeyboardEnhancementsMsg:
		m.keyboard = msg.SupportsKeyDisambiguation()
	case tea.PasteMsg:
		return m.paste(msg)
	case tea.MouseWheelMsg:
		return m.wheel(msg)
	case tea.KeyPressMsg:
		return m.key(msg)
	}
	return nil
}

func (m *Model) dialed(msg DialedMsg) tea.Cmd {
	if msg.Gen != m.gen {
		closeTransport(msg.transport)
		return nil
	}
	if msg.Err != nil {
		return m.dialFailed(msg.Err)
	}
	m.transport, m.session, m.conn = msg.transport, msg.Session, Connected
	m.healthyAt = m.opts.Now()
	if msg.Session.Epoch != m.epoch {
		m.newEpoch(msg.Session.Epoch)
	}
	m.status = ""
	return tea.Batch(listen(m.gen, m.transport), m.resync())
}

func (m *Model) dialFailed(err error) tea.Cmd {
	var busy *OwnerBusyError
	if asOwnerBusy(err, &busy) {
		return m.ownerBusy(busy)
	}
	// Before any session existed, a failure ends the chat with its reason.
	if m.session.SessionID == "" {
		m.fatal = err.Error()
		m.exitCode = 1
		m.conn = Closed
		return tea.Quit
	}
	return m.scheduleRetry(err)
}

// ownerBusy exits at startup; on reconnect it stops retrying and waits for
// an explicit retry or quit.
func (m *Model) ownerBusy(busy *OwnerBusyError) tea.Cmd {
	m.conn = OwnerBusy
	m.status = busy.Error()
	if m.session.SessionID == "" {
		m.exitCode = 1
		m.fatal = busy.Error() + "; close that chat and try again"
		return tea.Quit
	}
	m.status += "; ctrl+r retries, ctrl+q quits"
	return nil
}

func (m *Model) closed(gen uint64, err error) tea.Cmd {
	if gen != m.gen || m.conn == Closed {
		return nil
	}
	closeTransport(m.transport)
	m.transport, m.baseline = nil, false
	m.pending, m.outbox = map[string]pendingRequest{}, nil
	m.inbox.disconnected()
	m.history.disconnected()
	m.send.disconnected()
	m.bulkDisconnected()
	if isMalformed(err) {
		return m.stop(err)
	}
	if m.opts.Now().Sub(m.healthyAt) >= m.opts.Backoff.Healthy {
		m.failures = 0
	}
	return m.scheduleRetry(err)
}

// stop reports a malformed broker frame; retrying wouldn't fix it, so the
// chat waits for an explicit retry or quit.
func (m *Model) stop(err error) tea.Cmd {
	m.conn = Stopped
	m.status = "disconnected: " + err.Error() + "; ctrl+r retries, ctrl+q quits"
	return nil
}

func (m *Model) scheduleRetry(err error) tea.Cmd {
	m.conn = Reconnecting
	m.failures++
	delay := m.opts.Backoff.Base << min(m.failures-1, 16)
	delay = min(delay, m.opts.Backoff.Max)
	m.status = "disconnected; reconnecting in " + delay.String()
	if err != nil {
		m.status += " (" + err.Error() + ")"
	}
	gen := m.gen
	return tea.Tick(delay, func(time.Time) tea.Msg { return RetryMsg{Gen: gen} })
}

func (m *Model) retry(msg RetryMsg) tea.Cmd {
	if msg.Gen != m.gen || m.conn != Reconnecting {
		return nil
	}
	return m.dial()
}

func (m *Model) sent(msg SentMsg) tea.Cmd {
	if msg.Gen != m.gen || msg.Err == nil {
		return nil
	}
	// A failed write means the connection is gone; the listener reports it.
	closeTransport(m.transport)
	return nil
}

// newEpoch clears revision comparisons and suspends queued acknowledgments.
func (m *Model) newEpoch(epoch string) {
	if m.epoch != "" {
		m.inbox.suspendAll("broker restarted")
	}
	m.epoch, m.revision = epoch, 0
	m.resetSequenceState()
}

// resetSequenceState drops what refers to the old epoch's sequence space in
// every conversation: /clear cutoffs and /find cursors.
func (m *Model) resetSequenceState() {
	m.cutoff, m.found = 0, 0
	for _, v := range m.conv.saved {
		v.cutoff, v.found = 0, 0
	}
}

// resync installs a new baseline: snapshot first, then inbox and history.
func (m *Model) resync() tea.Cmd {
	m.baseline = false
	return m.request(kindSnapshot, protocol.Message{Type: protocol.TypeOperatorSnapshot})
}

func (m *Model) quit() tea.Cmd {
	if m.send.unresolved() && !m.quitArmed {
		m.quitArmed = true
		m.status = "send " + strconv.Quote(m.send.intent.ID) + " has an unknown outcome; this chat can't resolve it after exit (collab operator history shows accepted messages). ctrl+q again quits"
		return nil
	}
	m.conn = Closed
	m.cancel()
	closeTransport(m.transport)
	return tea.Quit
}

func closeTransport(t Transport) {
	if t != nil {
		t.Close()
	}
}

func asOwnerBusy(err error, target **OwnerBusyError) bool {
	return errors.As(err, target)
}

// Run starts the chat program on a terminal and returns the final model.
func Run(ctx context.Context, opts Options, input io.Reader, output io.Writer) (*Model, error) {
	m := New(opts)
	// The caller owns SIGINT/SIGTERM through ctx. Bubble Tea's own handler would
	// race it and can block forever sending to a loop that already exited,
	// before the terminal is restored. Resize handling is separate and stays.
	_, err := tea.NewProgram(m, tea.WithContext(ctx), tea.WithInput(input), tea.WithOutput(output),
		tea.WithoutSignalHandler()).Run()
	m.cancel()
	closeTransport(m.transport)
	if errors.Is(err, tea.ErrProgramKilled) && ctx.Err() != nil {
		err = nil
	}
	return m, err
}

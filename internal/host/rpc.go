// Package host connects a managed Codex App Server session to the broker.
package host

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/google/uuid"
)

type Frame struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}

// Host inventories can exceed 8 MiB (for example plugin/list includes icons),
// and since 2026-11 Codex's remote plugin catalog sends a plugin/list over
// 32 MiB at startup (#408). This limit is independent of the broker's 1 MiB
// peer-message limit.
const maxHostFrameBytes = 128 << 20

// ErrFrameTooLarge is a frame over the host frame limit: the stream can't
// continue past it, so the session ends with this, named, rather than a
// bare scanner error.
var ErrFrameTooLarge = fmt.Errorf("a frame is larger than %d MiB", maxHostFrameBytes>>20)

// Wire serializes writes and closes the stream on cancellation of a partial
// write. A closed or malformed stream is terminal; requests are never retried.
type Wire struct {
	writer io.WriteCloser
	gate   chan struct{}
}

func NewWire(writer io.WriteCloser) *Wire { return &Wire{writer: writer, gate: make(chan struct{}, 1)} }

func (w *Wire) Write(ctx context.Context, frame Frame) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case w.gate <- struct{}{}:
	}
	defer func() { <-w.gate }()
	if err := ctx.Err(); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { w.writer.Close() })
	defer stop()
	return json.NewEncoder(w.writer).Encode(frame)
}

func ReadFrames(reader io.Reader, handle func(Frame) error) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), maxHostFrameBytes)
	for scanner.Scan() {
		var frame Frame
		if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil {
			return err
		}
		if err := handle(frame); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return scanError(err)
	}
	return io.EOF
}

// scanError is the scanner's error, with a frame over the limit named as
// ErrFrameTooLarge.
func scanError(err error) error {
	if errors.Is(err, bufio.ErrTooLong) {
		return ErrFrameTooLarge
	}
	return err
}

type Calls struct {
	wire    *Wire
	mu      sync.Mutex
	pending map[string]chan Frame
}

func NewCalls(wire *Wire) *Calls { return &Calls{wire: wire, pending: make(map[string]chan Frame)} }

func (c *Calls) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id, _ := json.Marshal("collab-" + uuid.NewString())
	data, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	reply := make(chan Frame, 1)
	c.mu.Lock()
	c.pending[string(id)] = reply
	c.mu.Unlock()
	defer c.remove(string(id))
	if err := c.wire.Write(ctx, Frame{ID: id, Method: method, Params: data}); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case frame := <-reply:
		if len(frame.Error) > 0 {
			return nil, fmt.Errorf("App Server %s: %s", method, frame.Error)
		}
		return frame.Result, nil
	}
}

func (c *Calls) remove(id string) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// Resolve consumes replies to internal requests, including late replies after
// cancellation. Their reserved IDs must never escape to the operator client.
func (c *Calls) Resolve(frame Frame) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	reply, ok := c.pending[string(frame.ID)]
	if !ok {
		return internalID(frame.ID)
	}
	select {
	case reply <- frame:
	default:
	}
	return true
}

func internalID(id json.RawMessage) bool {
	var value string
	if json.Unmarshal(id, &value) != nil {
		return false
	}
	return strings.HasPrefix(value, "collab-")
}

func errorFrame(id json.RawMessage, err error) Frame {
	data, _ := json.Marshal(map[string]any{"code": -32602, "message": err.Error()})
	return Frame{ID: id, Error: data}
}

var errNoThread = errors.New("no managed thread; start a new thread through this proxy first")

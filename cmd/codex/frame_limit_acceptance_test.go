package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/pairtask/pairtask/internal/host"
)

// Codex's remote plugin catalog made the startup plugin/list frame larger than
// the old 32 MiB limit (#408); the host limit is now 128 MiB.
const (
	overOldLimit  = 40 << 20
	overHostLimit = 128 << 20
)

// filler is an endless run of one byte, so an oversized frame is streamed
// rather than held in memory.
type filler byte

func (f filler) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(f)
	}
	return len(p), nil
}

// frameStream is before, then one plugin/list frame whose result string is n
// bytes, then after, each on its own line.
func frameStream(before string, n int64, after string) io.Reader {
	return io.MultiReader(
		strings.NewReader(before+"\n"+`{"id":"plugins","result":"`),
		io.LimitReader(filler('x'), n),
		strings.NewReader("\"}\n"+after+"\n"))
}

// countingOutput counts what the proxy writes to the operator client.
type countingOutput struct {
	mu    sync.Mutex
	bytes int
	lines int
}

func (c *countingOutput) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bytes += len(p)
	c.lines += strings.Count(string(p), "\n")
	return len(p), nil
}

func (c *countingOutput) Close() error { return nil }

func TestHostFrameOverTheOldLimitReachesTheOperatorClient(t *testing.T) {
	operator := &countingOutput{}
	p := host.NewProxy(host.NewWire(&countingOutput{}), host.NewWire(operator))
	err := readHostFrames(context.Background(), p,
		frameStream(`{"method":"before"}`, overOldLimit, `{"method":"after"}`))
	if err != io.EOF {
		t.Fatalf("session ended with %v, want io.EOF", err)
	}
	if operator.lines != 3 || operator.bytes < overOldLimit {
		t.Fatalf("operator client got %d frames in %d bytes, want 3 frames over %d bytes",
			operator.lines, operator.bytes, overOldLimit)
	}
}

func TestOversizedHostFrameNamesTheAppServer(t *testing.T) {
	if raceDetector {
		t.Skip("a frame over the limit fills a 128 MiB buffer; the non-race job covers it")
	}
	operator := &countingOutput{}
	p := host.NewProxy(host.NewWire(&countingOutput{}), host.NewWire(operator))
	p.RestrictedOperator = true
	err := readHostFrames(context.Background(), p,
		frameStream(`{"method":"before"}`, overHostLimit, `{"method":"after"}`))
	if !errors.Is(err, host.ErrFrameTooLarge) {
		t.Fatalf("got %v, want host.ErrFrameTooLarge", err)
	}
	if !strings.Contains(err.Error(), "Codex App Server") {
		t.Fatalf("error doesn't name the App Server: %v", err)
	}
	if strings.Contains(err.Error(), "accounting is unconfirmed") {
		t.Fatalf("an oversized frame reads as a disconnect: %v", err)
	}
	if operator.lines != 1 {
		t.Fatalf("operator client got %d frames, want only the one before the oversized frame", operator.lines)
	}
}

func TestOversizedOperatorFrameNamesTheOperatorClient(t *testing.T) {
	if raceDetector {
		t.Skip("a frame over the limit fills a 128 MiB buffer; the non-race job covers it")
	}
	var methods []string
	err := readOperatorFrames(context.Background(),
		frameStream(`{"method":"before"}`, overHostLimit, `{"method":"after"}`),
		func(f host.Frame) error {
			methods = append(methods, f.Method)
			return nil
		})
	if !errors.Is(err, host.ErrFrameTooLarge) {
		t.Fatalf("got %v, want host.ErrFrameTooLarge", err)
	}
	if !strings.Contains(err.Error(), "operator client") || strings.Contains(err.Error(), "App Server") {
		t.Fatalf("error doesn't name the operator client alone: %v", err)
	}
	if strings.Join(methods, ",") != "before" {
		t.Fatalf("dispatched %v, want only the frame before the oversized one", methods)
	}
}

func TestOperatorEndOfStreamIsStillPlainEOF(t *testing.T) {
	err := readOperatorFrames(context.Background(), strings.NewReader(`{"method":"one"}`+"\n"),
		func(host.Frame) error { return nil })
	if err != io.EOF {
		t.Fatalf("got %v, want io.EOF itself", err)
	}
}

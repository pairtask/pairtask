//go:build unix

package firstcontact_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
)

func TestFirstContactAcceptanceRejectsLengthBeforeReadingBody(t *testing.T) {
	for _, size := range []uint32{0, 4097, ^uint32(0)} {
		r, c := rawClient(t, identity(t), rawClientConfig(certificate(t, identity(t), time.Now())))
		mustOK(t, r.err)
		done := make(chan error, 1)
		go func() { _, err := c.Write(binary.BigEndian.AppendUint32(nil, size)); done <- err }()
		_, err := r.conn.Frames(testContext(t)).Recv()
		equal(t, errors.Is(err, peer.ErrPairingProtocol), true)
		mustOK(t, <-done)
	}
}
func TestFirstContactAcceptanceTruncatedFramesFail(t *testing.T) {
	for _, data := range [][]byte{{0, 0}, {0, 0, 0, 4, 'a', 'b'}} {
		r, c := rawClient(t, identity(t), rawClientConfig(certificate(t, identity(t), time.Now())))
		mustOK(t, r.err)
		done := make(chan error, 1)
		go func() { _, err := c.Write(data); c.NetConn().Close(); done <- err }()
		_, err := r.conn.Frames(testContext(t)).Recv()
		mustFail(t, err)
		mustOK(t, <-done)
	}
}
func TestFirstContactAcceptanceFramesAreBoundedAndPreserveBoundaries(t *testing.T) {
	r, c := rawClient(t, identity(t), rawClientConfig(certificate(t, identity(t), time.Now())))
	mustOK(t, r.err)
	f := r.conn.Frames(testContext(t))
	for _, invalid := range [][]byte{nil, make([]byte, 4097)} {
		equal(t, errors.Is(f.Send(invalid), peer.ErrPairingProtocol), true)
	}
	for _, frame := range [][]byte{[]byte("x"), bytes.Repeat([]byte{17}, 4096), []byte("next")} {
		done := make(chan error, 1)
		go func() { done <- f.Send(frame) }()
		var prefix [4]byte
		_, err := io.ReadFull(c, prefix[:])
		mustOK(t, err)
		equal(t, binary.BigEndian.Uint32(prefix[:]), uint32(len(frame)))
		got := make([]byte, len(frame))
		_, err = io.ReadFull(c, got)
		mustOK(t, err)
		mustOK(t, <-done)
		equal(t, bytes.Equal(got, frame), true)
	}
}
func TestFirstContactAcceptanceFragmentedStreamPreservesFrames(t *testing.T) {
	r, c := rawClient(t, identity(t), rawClientConfig(certificate(t, identity(t), time.Now())))
	mustOK(t, r.err)
	data := append(binary.BigEndian.AppendUint32(nil, 3), []byte("one")...)
	data = append(data, append(binary.BigEndian.AppendUint32(nil, 3), []byte("two")...)...)
	done := make(chan error, 1)
	go func() {
		for _, b := range data {
			if _, err := c.Write([]byte{b}); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	f := r.conn.Frames(testContext(t))
	for _, want := range []string{"one", "two"} {
		b, err := f.Recv()
		mustOK(t, err)
		equal(t, string(b), want)
	}
	mustOK(t, <-done)
}
func TestFirstContactAcceptanceCancellationStopsFramedIO(t *testing.T) {
	for _, write := range []bool{false, true} {
		t.Run(map[bool]string{false: "read", true: "write"}[write], func(t *testing.T) {
			a, _ := connected(t, identity(t), identity(t))
			ctx, cancel := context.WithCancel(testContext(t))
			done := make(chan error, 1)
			go func() {
				f := a.Frames(ctx)
				if write {
					done <- f.Send([]byte("blocked"))
					return
				}
				_, err := f.Recv()
				done <- err
			}()
			select {
			case e := <-done:
				t.Fatalf("I/O returned before cancellation: %v", e)
			case <-time.After(20 * time.Millisecond):
			}
			cancel()
			select {
			case e := <-done:
				equal(t, errors.Is(e, context.Canceled), true)
			case <-time.After(time.Second):
				t.Fatal("framed I/O ignored cancellation")
			}
		})
	}
}

func TestFirstContactAcceptanceReaderAndWriterDeadlinesStayIndependent(t *testing.T) {
	a, b := connected(t, identity(t), identity(t))
	ctx := testContext(t)
	readCtx, cancelRead := context.WithCancel(ctx)
	defer cancelRead()
	readDone, writeDone := make(chan error, 1), make(chan error, 1)
	go func() { _, err := a.Frames(readCtx).Recv(); readDone <- err }()
	go func() { writeDone <- a.Frames(ctx).Send([]byte("outgoing")) }()
	got, err := b.Frames(ctx).Recv()
	mustOK(t, err)
	equal(t, string(got), "outgoing")
	mustOK(t, <-writeDone)
	cancelRead()
	select {
	case err := <-readDone:
		equal(t, errors.Is(err, context.Canceled), true)
	case <-time.After(time.Second):
		t.Fatal("completed writer cleared reader cancellation")
	}
	go func() { writeDone <- b.Frames(ctx).Send([]byte("new reader")) }()
	got, err = a.Frames(ctx).Recv()
	mustOK(t, err)
	mustOK(t, <-writeDone)
	equal(t, string(got), "new reader")
}

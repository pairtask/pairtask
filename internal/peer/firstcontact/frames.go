package firstcontact

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"

	"github.com/pairtask/pairtask/internal/peer"
)

type streamFrames struct {
	ctx  context.Context
	conn net.Conn
}

func (s streamFrames) Send(p []byte) error {
	if err := checkFrameSize(len(p)); err != nil {
		return err
	}
	frame := binary.BigEndian.AppendUint32(make([]byte, 0, 4+len(p)), uint32(len(p)))
	frame = append(frame, p...)
	return guarded(s.ctx, s.conn.SetWriteDeadline, func() error { return writeAll(s.conn, frame) })
}

// writeAll writes until done or an error; a short write without an error
// is reported rather than retried forever.
func writeAll(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, err := w.Write(b)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}

func (s streamFrames) Recv() ([]byte, error) {
	var body []byte
	err := guarded(s.ctx, s.conn.SetReadDeadline, func() error {
		var prefix [4]byte
		if _, err := io.ReadFull(s.conn, prefix[:]); err != nil {
			return err
		}
		size := binary.BigEndian.Uint32(prefix[:])
		if err := checkFrameSize(int(size)); err != nil {
			return err
		}
		body = make([]byte, size)
		_, err := io.ReadFull(s.conn, body)
		return err
	})
	return body, err
}

// checkFrameSize refuses an empty or oversized frame before any body is
// read or allocated.
func checkFrameSize(n int) error {
	if n == 0 || n > peer.MaxFrameBytes {
		return fmt.Errorf("%w: frame of %d bytes", peer.ErrPairingProtocol, n)
	}
	return nil
}

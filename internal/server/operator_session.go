package server

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"time"

	"github.com/pairtask/pairtask/internal/hub"
	"github.com/pairtask/pairtask/internal/protocol"
)

// operatorSessionPeer is an authenticated operator connection and its frame reader.
type operatorSessionPeer struct {
	conn net.Conn
	r    *bufio.Reader
	uid  int
}

// runSession serves a live operator session: the open response goes first,
// then one writer drains the session queue while this goroutine reads requests.
func (o *OperatorServer) runSession(ctx context.Context, peer operatorSessionPeer, open protocol.Message) {
	conn, r, uid := peer.conn, peer.r, peer.uid
	if !validRequestID(open.RequestID) {
		o.reply(conn, protocol.Message{Type: protocol.TypeError, Code: protocol.ErrMalformedFrame, Detail: "request_id must be 1 to 128 bytes"})
		return
	}
	sess := hub.NewOperatorSession(uid)
	resp := o.hub.OpenOperatorSession(ctx, open, sess)
	if resp.Type == protocol.TypeOperatorSession {
		// Release a claimed session even if its open response can't be written.
		defer o.hub.CloseOperatorSession(sess)
	}
	if o.reply(conn, resp) != nil || resp.Type != protocol.TypeOperatorSession {
		return
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	written := make(chan struct{})
	go o.writeSession(ctx, conn, sess, written)
	o.readSession(ctx, r, sess)
	sess.Finish()
	<-written
}

// readSession forwards requests; envelope violations end the session after
// their error has been written.
func (o *OperatorServer) readSession(ctx context.Context, r *bufio.Reader, sess *hub.OperatorSession) {
	for {
		var msg protocol.Message
		if err := protocol.ReadFrame(r, &msg); err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				sess.Control(sessionError("", "unparseable frame; closing the session"))
			}
			return
		}
		if !validRequestID(msg.RequestID) {
			sess.Control(sessionError("", "request_id must be 1 to 128 bytes; closing the session"))
			return
		}
		if err := sess.Begin(msg.RequestID); err != nil {
			sess.Control(sessionError(msg.RequestID, err.Error()+"; closing the session"))
			return
		}
		if !o.hub.SessionRequest(ctx, sess, msg) {
			return
		}
	}
}

// writeSession is the session's single writer; a write that misses its
// deadline closes the connection, which releases ownership.
func (o *OperatorServer) writeSession(ctx context.Context, conn net.Conn, sess *hub.OperatorSession, done chan<- struct{}) {
	defer close(done)
	defer conn.Close()
	for {
		msg, ok := sess.Next(ctx)
		if !ok {
			return
		}
		// Free the ID before writing: the client may reuse it as soon as the
		// bytes arrive. A failed write ends the session anyway.
		sess.Handed(msg)
		conn.SetWriteDeadline(time.Now().Add(writeTimeout))
		if err := protocol.WriteFrame(conn, msg); err != nil {
			o.log.Debug("operator session write error", "error", err)
			return
		}
	}
}

func validRequestID(id string) bool {
	return id != "" && len(id) <= 128
}

func sessionError(requestID, detail string) protocol.Message {
	return protocol.Message{Type: protocol.TypeError, Code: protocol.ErrMalformedFrame, RequestID: requestID, Detail: detail}
}

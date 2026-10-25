// Package status implements read-only broker inspection without a messaging ID.
package status

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Fetch never sends hello or retries through a messaging adapter. The returned
// snapshot is suitable for JSON output even when the connection fails.
func Fetch(ctx context.Context, path string) (protocol.StatusSnapshot, error) {
	out := protocol.UnavailableStatus()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return failed(out, fmt.Errorf("connect to broker: %w", err))
	}
	defer conn.Close()
	out.Reachable = true
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return failed(out, err)
	}
	if err := protocol.WriteFrame(conn, protocol.Message{Type: protocol.TypeStatus}); err != nil {
		return failed(out, err)
	}
	var reply protocol.Message
	if err := protocol.ReadFrame(bufio.NewReader(conn), &reply); err != nil {
		return failed(out, fmt.Errorf("read broker status: %w", err))
	}
	return statusReply(out, reply)
}

func statusReply(out protocol.StatusSnapshot, reply protocol.Message) (protocol.StatusSnapshot, error) {
	if reply.Type == protocol.TypeError && reply.Code == protocol.ErrExpectedHello {
		out.Health = "unsupported"
		return failed(out, errors.New("broker does not support read-only status; upgrade the broker"))
	}
	if reply.Type != protocol.TypeStatus || reply.Status == nil {
		return failed(out, errors.New("invalid broker status response"))
	}
	if !supportedSchema(reply.Status.SchemaVersion) {
		out.Health = "unsupported"
		return failed(out, errors.New("unsupported broker status schema"))
	}
	out = withDeclarations(*reply.Status)
	if out.Health != "ready" {
		return failed(out, errors.New("broker status is "+out.Health+": "+out.Error))
	}
	return out, nil
}

func failed(out protocol.StatusSnapshot, err error) (protocol.StatusSnapshot, error) {
	out.Error = err.Error()
	return out, err
}

// supportedSchema accepts this schema and schema 1, which lacks declarations.
func supportedSchema(v int) bool { return v == 1 || v == protocol.StatusSchemaVersion }

// withDeclarations normalises the declarations: a schema 1 broker reports
// none, which means unknown, not none; the list is never null.
func withDeclarations(out protocol.StatusSnapshot) protocol.StatusSnapshot {
	if out.SchemaVersion < protocol.StatusSchemaVersion {
		out.AgentStatus, out.AgentStatusAvailable, out.AgentStatusTotal, out.AgentStatusTruncated = nil, false, 0, false
	}
	if out.AgentStatus == nil {
		out.AgentStatus = []protocol.AgentStatus{}
	}
	return out
}

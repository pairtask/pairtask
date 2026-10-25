package status

import (
	"bytes"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

func WriteText(w io.Writer, out protocol.StatusSnapshot) error {
	var b bytes.Buffer
	fmt.Fprintf(&b, "Broker: %s (socket reachable: %t)\n", out.Health, out.Reachable)
	fmt.Fprintf(&b, "Snapshot: %s\n", timestamp(out.SnapshotAt))
	fmt.Fprintf(&b, "Connected sessions: %s\n", count(out.ConnectedSessions))
	pending := "unavailable"
	if out.DurablePending != nil {
		pending = fmt.Sprint(out.DurablePending.Total)
	}
	fmt.Fprintf(&b, "Durable pending recipient deliveries: %s\n", pending)
	fmt.Fprintf(&b, "Operator inbox pending: %s\n", operatorPending(out.OperatorPending))
	fmt.Fprintln(&b, "Legacy unacknowledged counts: unavailable")
	if out.Error != "" {
		fmt.Fprintf(&b, "Error: %q\n", out.Error)
	}
	writeSessions(&b, out)
	writeAgentStatus(&b, out)
	writePending(&b, out.DurablePending)
	fmt.Fprintln(&b, "\nConnected describes transport only. Pending requires explicit agent acknowledgment.")
	fmt.Fprintln(&b, "Last seen is registration/latest inbound frame, not a heartbeat; historical values and disconnect reasons are unavailable.")
	fmt.Fprintln(&b, "Session history is retained until operator-managed database maintenance; stale means no live owner and no recorded disconnect.")
	fmt.Fprintln(&b, "Agent status is what each agent last declared, never inferred; previous means an earlier session declared it.")
	_, err := w.Write(b.Bytes())
	return err
}

func writeSessions(w io.Writer, out protocol.StatusSnapshot) {
	fmt.Fprintf(w, "\nSessions (limit %d, truncated: %t, history available: %t):\n", out.SessionLimit, out.SessionsTruncated, out.HistoryAvailable)
	t := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(t, "AGENT\tSESSION\tSTATE\tCONNECTED AT\tLAST SEEN\tDISCONNECTED AT")
	for _, s := range out.Sessions {
		// IDs may contain control characters; quote them for terminal safety.
		fmt.Fprintf(t, "%q\t%q\t%q\t%s\t%s\t%s\n", s.AgentID, s.SessionID, s.State,
			s.ConnectedAt.UTC().Format(time.RFC3339Nano), timestamp(s.LastSeenAt), timestamp(s.DisconnectedAt))
	}
	t.Flush()
}

func writePending(w io.Writer, p *protocol.PendingStatus) {
	if p == nil {
		return
	}
	fmt.Fprintf(w, "\nPending durable inboxes (limit %d, truncated: %t):\n", p.RecipientLimit, p.RecipientsTruncated)
	for _, r := range p.Recipients {
		fmt.Fprintf(w, "  %q: %d\n", r.AgentID, r.Pending)
	}
}

// operatorPending counts the same rows as the durable total; nil means an
// older broker, so the count is unknown rather than zero.
func operatorPending(n *int) string {
	if n == nil {
		return "unknown"
	}
	return fmt.Sprint(*n)
}

func timestamp(t *time.Time) string {
	if t == nil {
		return "unavailable"
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func count(n *int) string {
	if n == nil {
		return "unavailable"
	}
	return fmt.Sprint(*n)
}

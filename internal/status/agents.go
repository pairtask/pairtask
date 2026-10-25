package status

import (
	"fmt"
	"io"
	"regexp"
	"text/tabwriter"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

// writeAgentStatus lists the agents' declarations as declared, with their
// ages; nothing is inferred from age or from a disconnect.
func writeAgentStatus(w io.Writer, out protocol.StatusSnapshot) {
	if !out.AgentStatusAvailable {
		fmt.Fprintf(w, "\nAgent status: unavailable (%s)\n", agentStatusGap(out))
		return
	}
	fmt.Fprintf(w, "\nAgent status (shown %d of %d, truncated: %t):\n", len(out.AgentStatus), out.AgentStatusTotal, out.AgentStatusTruncated)
	now := snapshotTime(out)
	t := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(t, "AGENT\tSTATE\tWAITING\tTASK\tSTEP\tREF\tDECLARED\tACTIVITY\tSESSION")
	for _, s := range out.AgentStatus {
		// Declared text comes from agents; quote it for terminal safety.
		fmt.Fprintf(t, "%q\t%q\t%q\t%q\t%q\t%q\t%s\t%s\t%s\n", s.AgentID, s.State, s.WaitingOn, s.Task, s.Step, ShortRef(s.Ref),
			s.DeclaredAt.UTC().Format(time.RFC3339)+" ("+Age(now, s.DeclaredAt)+")", activity(now, s.Activity), sessionLabel(s.Connected))
	}
	for _, id := range undeclared(out) {
		fmt.Fprintf(t, "%q\tno status declared\t\t\t\t\t\t\t\n", id)
	}
	t.Flush()
}

func agentStatusGap(out protocol.StatusSnapshot) string {
	if out.SchemaVersion < protocol.StatusSchemaVersion {
		return fmt.Sprintf("broker status schema %d", out.SchemaVersion)
	}
	return "not read"
}

// undeclared is the connected agents without a declaration. Only a complete
// list proves absence; a truncated one may have left the record out.
func undeclared(out protocol.StatusSnapshot) []string {
	if out.AgentStatusTruncated {
		return nil
	}
	declared := make(map[string]bool, len(out.AgentStatus))
	for _, s := range out.AgentStatus {
		declared[s.AgentID] = true
	}
	var ids []string
	for _, s := range out.Sessions {
		if s.State == "transport_connected" && !declared[s.AgentID] {
			declared[s.AgentID] = true
			ids = append(ids, s.AgentID)
		}
	}
	return ids
}

func activity(now time.Time, a *protocol.Activity) string {
	if a == nil {
		return "unavailable"
	}
	return Age(now, a.At) + " ago via " + fmt.Sprintf("%q", a.Source)
}

func sessionLabel(connected bool) string {
	if connected {
		return "current"
	}
	return "previous"
}

func snapshotTime(out protocol.StatusSnapshot) time.Time {
	if out.SnapshotAt != nil {
		return *out.SnapshotAt
	}
	return time.Now()
}

// Age is a compact elapsed time: 45s, 3m, 2h, 4d.
func Age(now, t time.Time) string {
	d := max(0, now.Sub(t))
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

var commitID = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

// ShortRef shortens a full commit ID to 12 characters for display; any other
// ref is shown as is. The full value stays in JSON and in copies.
func ShortRef(ref string) string {
	if commitID.MatchString(ref) {
		return ref[:12]
	}
	return ref
}

// writeClaims lists the claims of the newest claimed operator messages.
func writeClaims(w io.Writer, out protocol.StatusSnapshot) {
	if !out.ClaimsAvailable {
		fmt.Fprintln(w, "\nClaims: unavailable")
		return
	}
	fmt.Fprintf(w, "\nClaims (messages shown %d of %d, truncated: %t):\n", len(out.Claims), out.ClaimsTotal, out.ClaimsTruncated)
	now := snapshotTime(out)
	t := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(t, "MESSAGE\tTASK\tROLE\tAGENT\tSTATE\tCLAIMED\tTOPIC")
	for _, set := range out.Claims {
		for _, c := range set.Claims {
			fmt.Fprintf(t, "%q\t%q\t%s\t%q\t%s\t%s\t%q\n", set.OperatorMessageID, c.Task, c.Role, c.AgentID, c.State, Age(now, c.ClaimedAt)+" ago", c.Topic)
		}
	}
	t.Flush()
	for _, set := range out.Claims {
		if set.Truncated {
			fmt.Fprintf(w, "  message %q: showing %d of %d claims\n", set.OperatorMessageID, len(set.Claims), set.Total)
		}
	}
}

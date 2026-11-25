package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/pairtask/pairtask/internal/desktop"
	"github.com/pairtask/pairtask/internal/protocol"
)

const statusUsage = `usage: pairtask-desktop status [--dir DIR] [--json]`

// nodeStatus is what `status` reports: who runs this machine's node, as
// what, and with which settings.
type nodeStatus struct {
	Dir      string             `json:"dir"`
	Name     string             `json:"name"`               // the name it pairs under, as the running node says
	Identity string             `json:"identity,omitempty"` // its fingerprint
	Owner    *desktop.OwnerInfo `json:"owner,omitempty"`    // the copy running it now; none when nothing runs it
	Agent    string             `json:"agent"`              // the login item's state
	Settings desktop.Settings   `json:"settings"`
	Problem  string             `json:"problem,omitempty"`
}

// runStatus reports the node in the data directory without ever running
// it: it reads what the running copy recorded and asks that copy.
func runStatus(args []string, env agentEnv, out io.Writer) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	dirFlag := fs.String("dir", "", "the data directory (default: the user config directory's pairtask-desktop)")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return errors.New(statusUsage)
	}
	dir, err := dataDir(*dirFlag)
	if err != nil {
		return err
	}
	st := readStatus(dir, env)
	if *asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(st)
	}
	printStatus(out, st)
	return nil
}

// readStatus gathers the status; what it can't learn it says.
func readStatus(dir string, env agentEnv) nodeStatus {
	st := nodeStatus{Dir: dir, Agent: agentState(env)}
	var problems []error
	s, err := desktop.ReadSettings(dir)
	st.Settings = s
	problems = append(problems, err)
	o, ok, err := desktop.ReadOwnerInfo(dir)
	problems = append(problems, err)
	if ok && o.Running() {
		st.Owner = &o
		st.Name, st.Identity, err = askNode(dir)
		problems = append(problems, err)
	}
	if err := errors.Join(problems...); err != nil {
		st.Problem = err.Error()
	}
	return st
}

// agentState is the login item's state, where there is one.
func agentState(env agentEnv) string {
	if env.supported() != nil {
		return "not available here (macOS only)"
	}
	return env.state()
}

// askNode asks the running node its name and identity, as a client.
func askNode(dir string) (name, identity string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	n, err := desktop.Connect(ctx, dir)
	if err != nil {
		return "", "", err
	}
	defer n.Close()
	out := n.Call(ctx, protocol.Message{Type: protocol.TypeOperatorPairing, PairingOp: protocol.PairingOpList})
	if out.PairingResult == nil {
		return "", "", fmt.Errorf("the node didn't answer: %s %s", out.Code, out.Detail)
	}
	return out.PairingResult.SelfName, out.PairingResult.SelfIdentity, nil
}

func printStatus(out io.Writer, st nodeStatus) {
	line := func(label, value string) { fmt.Fprintf(out, "%-14s %s\n", label+":", value) }
	line("data folder", st.Dir)
	if st.Owner == nil {
		line("runs now", "nothing: no copy runs this node")
	} else {
		who := "a client (the app or Claude's extension)"
		if st.Owner.Role == desktop.RoleAgent {
			who = "the always-on agent"
		}
		line("runs now", fmt.Sprintf("%s, pid %d, since %s", who, st.Owner.PID, st.Owner.Since.Format(time.RFC3339)))
		line("name", st.Name)
		line("identity", st.Identity)
	}
	line("agent", st.Agent)
	line("name setting", orNone(st.Settings.Name))
	line("pairbook", orNone(st.Settings.Pairbook))
	notify := "on"
	if st.Settings.Notify != nil && !*st.Settings.Notify {
		notify = "off"
	}
	line("notifications", notify)
	line("clients", "the app, Claude's extension and other MCP clients reach the running copy per call over "+desktop.SocketFile)
	if st.Problem != "" {
		line("problem", st.Problem)
	}
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

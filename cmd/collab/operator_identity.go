package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/pairtask/pairtask/internal/personkey"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/signing"
)

const operatorIdentityUsage = "usage: pairtask operator identity (init | status) [--socket PATH] [--json]"

// identityReport is what identity init and status show.
type identityReport struct {
	PersonKey  string `json:"person_key,omitempty"`
	Dir        string `json:"dir"`
	Registered bool   `json:"registered"`
	protocol.IdentityView
}

// identityRequest is an identity command: init or status, the broker's
// socket, the key directory and the output form.
type identityRequest struct {
	op, socket, dir string
	asJSON          bool
}

// runOperatorIdentity makes this account's signing keys (init), the
// explicit step that creates a key the workspace then trusts, or shows
// them and whether the broker has the person key pinned (status) (#115).
func runOperatorIdentity(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	req, code := parseIdentity(args, stderr)
	if code != -1 {
		return code
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	report, err := identityOp(ctx, req)
	if err != nil {
		fmt.Fprintln(stderr, "operator identity:", err)
		return 1
	}
	return writeIdentity(report, req.asJSON, stdout, stderr)
}

func parseIdentity(args []string, stderr io.Writer) (identityRequest, int) {
	var req identityRequest
	if !identityOpNamed(args) {
		fmt.Fprintln(stderr, operatorIdentityUsage)
		return req, 2
	}
	flags := flag.NewFlagSet("operator identity", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&req.socket, "socket", defaultOperatorSocket, "operator Unix socket path (secured only)")
	flags.BoolVar(&req.asJSON, "json", false, "emit the result as JSON")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
		fmt.Fprintln(stderr, operatorIdentityUsage)
		return req, 2
	}
	req.op = args[0]
	return req, -1
}

// identityOpNamed: args start with init or status.
func identityOpNamed(args []string) bool {
	return len(args) > 0 && slices.Contains([]string{"init", "status"}, args[0])
}

// identityOp creates and registers keys (init) or reports them (status).
func identityOp(ctx context.Context, req identityRequest) (identityReport, error) {
	dir, err := personkey.ResolveDir()
	if err != nil {
		return identityReport{}, err
	}
	req.dir = dir
	op := identityStatus
	if req.op == "init" {
		op = identityInit
	}
	report, err := op(ctx, req)
	report.Dir = dir
	return report, err
}

// errNotSecured: init found no broker to register a key with, so it made
// none; outside the secured runtime a key would sit where agents can read it.
var errNotSecured = errors.New("no operator broker answers here, so no key was made: run `pairtask identity init` on the host, which runs this in the secured runtime")

// identityInit makes the keys, only when there are none and the broker
// answers, and registers the person key.
func identityInit(ctx context.Context, req identityRequest) (identityReport, error) {
	if _, err := identityStatus(ctx, req); err != nil {
		return identityReport{}, fmt.Errorf("%w (%v)", errNotSecured, err)
	}
	keys, err := personkey.Create(req.dir)
	if errors.Is(err, personkey.ErrExists) {
		return identityReport{}, errors.New("this account has keys already; `pairtask operator identity status` shows them")
	} else if err != nil {
		return identityReport{}, err
	}
	view, err := registerPerson(ctx, req.socket, keys)
	return identityReport{PersonKey: signing.KeyID(keys.PersonPublic()), Registered: err == nil, IdentityView: view}, err
}

// identityStatus reports this account's keys and the broker's roots.
func identityStatus(ctx context.Context, req identityRequest) (identityReport, error) {
	var report identityReport
	if keys, err := personkey.Load(req.dir); err == nil {
		report.PersonKey = signing.KeyID(keys.PersonPublic())
	} else if !errors.Is(err, personkey.ErrNoKeys) {
		return report, err
	}
	resp, err := sendOperator(ctx, req.socket, protocol.Message{Type: protocol.TypeOperatorIdentity, MessageID: uuid.NewString(), IdentityOp: protocol.IdentityOpStatus})
	if err != nil {
		return report, err
	}
	if resp.Type != protocol.TypeOperatorIdentityResult || resp.Identity == nil {
		return report, errors.New(resp.Code + " " + resp.Detail)
	}
	report.IdentityView = *resp.Identity
	report.Registered = report.pinned()
	return report, nil
}

// pinned: the broker holds this account's person key, approved.
func (r identityReport) pinned() bool {
	return r.PersonKey != "" && slices.ContainsFunc(r.Roots, r.isPersonKey)
}

func (r identityReport) isPersonKey(root protocol.SigningRoot) bool {
	return root.KeyID == r.PersonKey && root.Person && root.Approved
}

func writeIdentity(report identityReport, asJSON bool, stdout, stderr io.Writer) int {
	if asJSON {
		if err := json.NewEncoder(stdout).Encode(report); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	switch {
	case report.PersonKey == "":
		fmt.Fprintln(stdout, "no keys: your messages go unsigned; `pairtask operator identity init` makes them")
	case report.Registered:
		fmt.Fprintf(stdout, "person key %s, registered: your messages are signed\n", report.PersonKey)
	default:
		fmt.Fprintf(stdout, "person key %s, not registered with this broker: the chat registers it when it starts\n", report.PersonKey)
	}
	fmt.Fprintf(stdout, "workspace %s\nkeys in %s\n", report.Workspace, report.Dir)
	return 0
}

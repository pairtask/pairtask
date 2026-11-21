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

const operatorIdentityUsage = "usage: pairtask operator identity (init | anchor | confirm | status) [--no-recovery] [--socket PATH] [--json]"

// identityReport is what identity init and status show.
type identityReport struct {
	PersonKey  string `json:"person_key,omitempty"`
	Dir        string `json:"dir"`
	Registered bool   `json:"registered"`
	protocol.IdentityView
	anchorView
	local bool // anchor and confirm: nothing was asked of the broker
}

// identityRequest is an identity command: init or status, the broker's
// socket, the key directory and the output form.
type identityRequest struct {
	op, socket, dir    string
	asJSON, noRecovery bool
	out                io.Writer // where a recovery code and its prompts go
}

// runOperatorIdentity makes this account's signing keys (init), the
// explicit step that creates a key the workspace then trusts, or shows
// them and whether the broker has the person key pinned (status) (#115).
func runOperatorIdentity(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	req, code := parseIdentity(args, stderr)
	if code != -1 {
		return code
	}
	req.out = stdout
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
	flags.BoolVar(&req.noRecovery, "no-recovery", false, "make no recovery key (init, anchor; not recommended)")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
		fmt.Fprintln(stderr, operatorIdentityUsage)
		return req, 2
	}
	req.op = args[0]
	if err := req.check(); err != nil {
		fmt.Fprintln(stderr, "operator identity:", err)
		return req, 2
	}
	return req, -1
}

// check: --no-recovery only where an anchor is made, and no JSON where a
// recovery code would be shown or typed.
func (r identityRequest) check() error {
	if r.noRecovery && !r.makesAnchor() {
		return errors.New("--no-recovery is for init and anchor")
	}
	if r.asJSON && r.handlesCode() {
		return errJSONRecovery
	}
	return nil
}

// makesAnchor: the command makes the identity anchor.
func (r identityRequest) makesAnchor() bool { return r.op == "init" || r.op == "anchor" }

// handlesCode: the command shows or reads a recovery code.
func (r identityRequest) handlesCode() bool {
	if r.op == "confirm" {
		return true
	}
	return r.makesAnchor() && !r.noRecovery
}

// identityOpNamed: args start with init or status.
func identityOpNamed(args []string) bool {
	return len(args) > 0 && identityOps[args[0]] != nil
}

// identityOp creates and registers keys (init) or reports them (status).
func identityOp(ctx context.Context, req identityRequest) (identityReport, error) {
	dir, err := personkey.ResolveDir()
	if err != nil {
		return identityReport{}, err
	}
	req.dir = dir
	report, err := identityOps[req.op](ctx, req)
	report.Dir = dir
	return report, err
}

// identityOps are the identity commands by name.
var identityOps map[string]func(context.Context, identityRequest) (identityReport, error)

func init() {
	identityOps = map[string]func(context.Context, identityRequest) (identityReport, error){
		"init": identityInit, "anchor": identityAnchor, "confirm": identityConfirm, "status": identityStatus,
	}
}

// errNotSecured: init found no broker to register a key with, so it made
// none; outside the secured runtime a key would sit where agents can read it.
var errNotSecured = errors.New("no operator broker answers here, so no key was made: run `pairtask identity init` on the host, which runs this in the secured runtime")

// identityInit makes the keys, only when there are none and the broker
// answers, and registers the person key. Keys already there, whole or
// not, are reported as they are before the broker is asked.
func identityInit(ctx context.Context, req identityRequest) (identityReport, error) {
	if err := noKeysYet(req.dir); err != nil {
		return identityReport{}, err
	}
	if _, err := brokerIdentity(ctx, req.socket); err != nil {
		return identityReport{}, fmt.Errorf("%w (%v)", errNotSecured, err)
	}
	keys, err := personkey.Create(req.dir)
	if errors.Is(err, personkey.ErrExists) {
		return identityReport{}, errKeysExist
	} else if err != nil {
		return identityReport{}, err
	}
	view, err := registerPerson(ctx, req.socket, keys)
	report := identityReport{PersonKey: signing.KeyID(keys.PersonPublic()), Registered: err == nil, IdentityView: view}
	if err != nil {
		return report, err
	}
	return report, makeAnchor(req, &report)
}

var errKeysExist = errors.New("this account has keys already; `pairtask identity status` on the host shows them")

// noKeysYet: dir holds no keys; else why init can't make them there.
func noKeysYet(dir string) error {
	_, err := personkey.Load(dir)
	switch {
	case err == nil:
		return errKeysExist
	case errors.Is(err, personkey.ErrNoKeys):
		return nil
	}
	return err
}

// identityStatus reports this account's keys and the broker's roots.
func identityStatus(ctx context.Context, req identityRequest) (identityReport, error) {
	var report identityReport
	if keys, err := personkey.Load(req.dir); err == nil {
		report.PersonKey = signing.KeyID(keys.PersonPublic())
	} else if !errors.Is(err, personkey.ErrNoKeys) {
		return report, err
	}
	view, err := brokerIdentity(ctx, req.socket)
	if err != nil {
		return report, err
	}
	report.IdentityView = view
	report.Registered = report.pinned()
	report.anchorView = anchorOf(req.dir)
	return report, nil
}

// brokerIdentity is the broker's identity status, from its operator socket.
func brokerIdentity(ctx context.Context, socket string) (protocol.IdentityView, error) {
	resp, err := sendOperator(ctx, socket, protocol.Message{Type: protocol.TypeOperatorIdentity, MessageID: uuid.NewString(), IdentityOp: protocol.IdentityOpStatus})
	if err != nil {
		return protocol.IdentityView{}, err
	}
	if resp.Type != protocol.TypeOperatorIdentityResult || resp.Identity == nil {
		return protocol.IdentityView{}, errors.New(resp.Code + " " + resp.Detail)
	}
	return *resp.Identity, nil
}

// pinned: the broker holds this account's person key, approved.
func (r identityReport) pinned() bool {
	return r.PersonKey != "" && slices.ContainsFunc(r.Roots, r.isPersonKey)
}

func (r identityReport) isPersonKey(root protocol.SigningRoot) bool {
	return root.KeyID == r.PersonKey && root.Person && root.Approved
}

func writeIdentity(report identityReport, asJSON bool, stdout, stderr io.Writer) int {
	if err := reportIdentity(report, asJSON, stdout); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

// reportIdentity writes the report: anchor and confirm locally, the rest
// with the broker's view; as JSON or text.
func reportIdentity(report identityReport, asJSON bool, stdout io.Writer) error {
	switch {
	case report.local:
		return writeLocal(report, asJSON, stdout)
	case asJSON:
		return json.NewEncoder(stdout).Encode(report)
	}
	writeIdentityText(report, stdout)
	return nil
}

func writeIdentityText(report identityReport, stdout io.Writer) {
	switch {
	case report.PersonKey == "":
		fmt.Fprintln(stdout, "no keys: your messages go unsigned; `pairtask identity init` on the host makes them")
	case report.Registered:
		fmt.Fprintf(stdout, "person key %s, registered: your messages are signed\n", report.PersonKey)
	default:
		fmt.Fprintf(stdout, "person key %s, not registered with this broker: the chat registers it when it starts\n", report.PersonKey)
	}
	if report.PersonKey != "" {
		writeAnchor(report.anchorView, stdout)
	}
	fmt.Fprintf(stdout, "workspace %s\nkeys in %s\n", report.Workspace, report.Dir)
}

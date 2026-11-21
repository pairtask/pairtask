package main

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"

	"github.com/pairtask/pairtask/internal/identity"
	"github.com/pairtask/pairtask/internal/personkey"
	"github.com/pairtask/pairtask/internal/signing"
)

// The identity anchor and its recovery code (#392): made once, beside the
// keys. The recovery code is shown once, on this terminal only (never in
// JSON, a log or an argument), and the operator types two of its groups
// back; `identity confirm` checks the whole code later.

// identityInput is where typed answers come from; tests replace it.
var identityInput io.Reader = os.Stdin

// confirmGroups are the code's groups (1-based, after "PTR1") typed back.
var confirmGroups = [2]int{3, 8}

var errJSONRecovery = errors.New("--json can't show a recovery code: run it without --json, or with --no-recovery")

// anchorView is what status shows about the anchor.
type anchorView struct {
	Anchor            string `json:"anchor"` // none | ok | invalid
	Lineage           string `json:"lineage,omitempty"`
	Recovery          bool   `json:"recovery,omitempty"`
	RecoveryConfirmed bool   `json:"recovery_confirmed,omitempty"`
}

// identityAnchor makes the anchor for keys made before it.
func identityAnchor(ctx context.Context, req identityRequest) (identityReport, error) {
	keys, err := personkey.Load(req.dir)
	if err != nil {
		return identityReport{}, err
	}
	report := identityReport{PersonKey: keyIDOf(keys)}
	err = makeAnchor(req, &report)
	return report, err
}

// makeAnchor makes the anchor now and, with recovery, shows its code once
// and asks for two groups back.
func makeAnchor(req identityRequest, report *identityReport) error {
	_, code, err := personkey.CreateAnchor(req.dir, !req.noRecovery, time.Now())
	if err != nil {
		return err
	}
	report.anchorView = anchorOf(req.dir)
	if code == "" {
		return nil
	}
	return confirmTyped(req, code)
}

// errCodeLost: the anchor was made, but its only showing of the recovery
// code failed. The code is gone; nothing prints it anywhere else.
var errCodeLost = errors.New("the identity anchor was made, but its recovery code could not be shown and is lost: recovery is unconfirmed; ask for help before relying on this identity")

// confirmTyped shows code and records confirmation when two of its groups
// come back right, within three tries. If the code can't be shown, it
// stops there: no prompt, and the code goes nowhere else.
func confirmTyped(req identityRequest, code string) error {
	if _, err := fmt.Fprintf(req.out, "\nYour recovery code. Write it down now: it is shown only this once and is never stored.\n\n    %s\n\n", code); err != nil {
		return errCodeLost
	}
	groups := strings.Split(code, "-")[1:]
	in := bufio.NewScanner(identityInput)
	for try := 0; try < 3; try++ {
		fmt.Fprintf(req.out, "Type group %d and group %d to confirm: ", confirmGroups[0], confirmGroups[1])
		if !in.Scan() {
			break
		}
		if typedGroups(in.Text(), groups) {
			return confirmed(req)
		}
	}
	fmt.Fprintln(req.out, "Not confirmed. `pairtask identity confirm` checks the whole code later.")
	return nil
}

func confirmed(req identityRequest) error {
	if err := personkey.ConfirmRecovery(req.dir); err != nil {
		return err
	}
	fmt.Fprintln(req.out, "Confirmed.")
	return nil
}

func typedGroups(line string, groups []string) bool {
	typed := strings.Fields(strings.ToUpper(strings.ReplaceAll(line, "-", " ")))
	want := []string{groups[confirmGroups[0]-1], groups[confirmGroups[1]-1]}
	return len(typed) == 2 && typed[0] == want[0] && typed[1] == want[1]
}

// identityConfirm reads the whole recovery code and records confirmation
// when it is the anchor's recovery key.
func identityConfirm(ctx context.Context, req identityRequest) (identityReport, error) {
	keys, err := personkey.Load(req.dir)
	if err != nil {
		return identityReport{}, err
	}
	recovery, err := anchoredRecovery(keys)
	if err != nil {
		return identityReport{}, err
	}
	fmt.Fprint(req.out, "Type your recovery code: ")
	typed, err := readHidden()
	if err != nil {
		return identityReport{}, err
	}
	r, err := personkey.DecodeRecovery(typed)
	if err != nil || !r.Public().(ed25519.PublicKey).Equal(recovery) {
		return identityReport{}, errors.New("that isn't this identity's recovery code")
	}
	if err := personkey.ConfirmRecovery(req.dir); err != nil {
		return identityReport{}, err
	}
	return identityReport{PersonKey: keyIDOf(keys), anchorView: anchorOf(req.dir)}, nil
}

// readHidden reads one line, without echo when it comes from a terminal.
func readHidden() (string, error) {
	if f, ok := identityInput.(*os.File); ok && term.IsTerminal(f.Fd()) {
		b, err := term.ReadPassword(f.Fd())
		return string(b), err
	}
	in := bufio.NewScanner(identityInput)
	if !in.Scan() {
		return "", errors.New("no recovery code typed")
	}
	return in.Text(), nil
}

// anchoredRecovery is the recovery key the stored anchor names.
func anchoredRecovery(keys *personkey.Keys) (ed25519.PublicKey, error) {
	body, err := keys.Anchor()
	if err != nil {
		return nil, err
	}
	a, err := identity.ParseAnchor(body)
	if err != nil || a.Recovery == nil {
		return nil, errors.New("this identity has no recovery key")
	}
	return a.Recovery, nil
}

func keyIDOf(keys *personkey.Keys) string { return signing.KeyID(keys.PersonPublic()) }

// anchorOf is the anchor's state in dir.
func anchorOf(dir string) anchorView {
	keys, err := personkey.Load(dir)
	if err != nil {
		return anchorView{Anchor: "none"}
	}
	body, err := keys.Anchor()
	switch {
	case errors.Is(err, personkey.ErrNoAnchor):
		return anchorView{Anchor: "none"}
	case err != nil:
		return anchorView{Anchor: "invalid"}
	}
	a, _ := identity.ParseAnchor(body)
	l := a.Lineage()
	return anchorView{Anchor: "ok", Lineage: hex.EncodeToString(l[:]), Recovery: a.Recovery != nil, RecoveryConfirmed: personkey.RecoveryConfirmed(dir)}
}

// writeAnchor adds the anchor lines to the text report.
func writeAnchor(v anchorView, stdout io.Writer) {
	switch v.Anchor {
	case "none":
		fmt.Fprintln(stdout, "no identity anchor yet: `pairtask identity anchor` makes it (once)")
	case "invalid":
		fmt.Fprintln(stdout, "identity anchor damaged or not this person's: it is never replaced automatically")
	case "ok":
		fmt.Fprintf(stdout, "identity %s\n", v.Lineage[:16])
		writeRecoveryState(v, stdout)
	}
}

func writeRecoveryState(v anchorView, stdout io.Writer) {
	switch {
	case !v.Recovery:
		fmt.Fprintln(stdout, "warning: no recovery key: losing this machine loses this identity")
	case !v.RecoveryConfirmed:
		fmt.Fprintln(stdout, "warning: recovery code not confirmed: `pairtask identity confirm`")
	}
}

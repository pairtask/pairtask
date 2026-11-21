package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/personkey"
)

type brokenRecoveryOutput struct{}

func (brokenRecoveryOutput) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestHostAnchorReportsLostRecoveryOutput(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keys")
	t.Setenv(personkey.DirEnv, dir)
	k, err := personkey.Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	code := run(context.Background(), []string{"operator", "identity", "anchor"}, brokenRecoveryOutput{}, &stderr)
	if code == 0 || strings.Contains(stderr.String(), "PTR1-") {
		t.Fatal("output failure silently succeeded or disclosed the code on stderr")
	}
	if _, err := k.Anchor(); err != nil {
		t.Fatalf("expected durable anchor after output failure: %v", err)
	}
	if personkey.RecoveryConfirmed(dir) {
		t.Fatal("lost code marked confirmed")
	}
}

func TestHostAnchorJSONCannotConsumeRecovery(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keys")
	t.Setenv(personkey.DirEnv, dir)
	k, err := personkey.Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"operator", "identity", "anchor", "--json"}, &out, &stderr)
	if code == 0 || strings.Contains(out.String()+stderr.String(), "PTR1-") {
		t.Fatal("JSON mode consumed or disclosed a recovery code")
	}
	if _, err := k.Anchor(); !errors.Is(err, personkey.ErrNoAnchor) {
		t.Fatalf("unexpected registration after rejected JSON mode: %v", err)
	}
}

// Read the recovery code as an operator would and answer the two-group prompt.
type recoveryInput struct{ output *bytes.Buffer }

func (r recoveryInput) Read(p []byte) (int, error) {
	for _, word := range strings.Fields(r.output.String()) {
		groups := strings.Split(word, "-")
		if len(groups) == 12 && groups[0] == "PTR1" {
			return copy(p, groups[3]+" "+groups[8]+"\n"), io.EOF
		}
	}
	return 0, io.EOF
}

func TestHostAnchorConfirmationThroughCLI(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keys")
	t.Setenv(personkey.DirEnv, dir)
	if _, err := personkey.Create(dir); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	oldInput := identityInput
	identityInput = recoveryInput{&stdout}
	t.Cleanup(func() { identityInput = oldInput })
	code := run(context.Background(), []string{"operator", "identity", "anchor"}, &stdout, &stderr)
	if code != 0 || !personkey.RecoveryConfirmed(dir) {
		t.Fatalf("host anchor confirmation failed (exit %d)", code)
	}
	if strings.Count(stdout.String(), "PTR1-") != 1 || strings.Contains(stderr.String(), "PTR1-") {
		t.Fatal("unexpected recovery disclosure")
	}
	stdout.Reset()
	stderr.Reset()
	code = run(context.Background(), []string{"operator", "identity", "anchor"}, &stdout, &stderr)
	if code == 0 || strings.Contains(stdout.String()+stderr.String(), "PTR1-") {
		t.Fatal("repeat anchor registration should refuse without a code")
	}
}

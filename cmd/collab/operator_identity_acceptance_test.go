package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/personkey"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/signing"
)

func cliSigningKeys(t *testing.T) *personkey.Keys {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir, err := personkey.DefaultDir()
	if err != nil {
		t.Fatal(err)
	}
	keys, err := personkey.Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

func TestOperatorIdentityInitWithoutBrokerDoesNotMakeKeys(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "identity")
	t.Setenv(personkey.DirEnv, dir)
	missingSocket := filepath.Join(t.TempDir(), "absent.sock")
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"operator", "identity", "init", "--socket", missingSocket}, &out, &stderr)
	if code != 1 {
		t.Fatalf("init without broker exit %d, stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "pairtask identity init") {
		t.Fatalf("missing host command guidance: %s", stderr.String())
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("init without broker made a key directory: %v", err)
	}
}

func TestOperatorIdentityInitReportsPartialKeysBeforeBrokerError(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "identity")
	t.Setenv(personkey.DirEnv, dir)
	if _, err := personkey.Create(dir); err != nil {
		t.Fatal(err)
	}
	personPath := filepath.Join(dir, "person.key")
	personBefore, err := os.ReadFile(personPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "device.key")); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"operator", "identity", "init", "--socket", filepath.Join(t.TempDir(), "absent.sock")}, &out, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), personkey.ErrIncomplete.Error()) {
		t.Fatalf("partial key error hidden by broker error: exit %d, stderr %s", code, stderr.String())
	}
	if strings.Contains(stderr.String(), "no operator broker answers") {
		t.Fatalf("partial key was mistaken for a missing broker: %s", stderr.String())
	}
	assertPartialKeyUnchanged(t, dir, personBefore)
}

func TestOperatorIdentityGuidanceUsesHostCommands(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "identity")
	t.Setenv(personkey.DirEnv, dir)
	if _, err := personkey.Create(dir); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"operator", "identity", "init", "--socket", filepath.Join(t.TempDir(), "absent.sock")}, &out, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "pairtask identity status") {
		t.Fatalf("existing-key guidance is not the host command: exit %d, stderr %s", code, stderr.String())
	}
	out.Reset()
	stderr.Reset()
	if code := writeIdentity(identityReport{Dir: dir}, false, &out, &stderr); code != 0 {
		t.Fatalf("status output failed: %d, %s", code, stderr.String())
	}
	if !strings.Contains(out.String(), "pairtask identity init") {
		t.Fatalf("missing-key guidance is not the host command: %s", out.String())
	}
}

func assertPartialKeyUnchanged(t *testing.T, dir string, personBefore []byte) {
	t.Helper()
	personPath := filepath.Join(dir, "person.key")
	personAfter, err := os.ReadFile(personPath)
	if err != nil || !bytes.Equal(personAfter, personBefore) {
		t.Fatalf("init changed the remaining person key: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "device.key")); !os.IsNotExist(err) {
		t.Fatalf("init silently replaced the missing device key: %v", err)
	}
}

func multiOperatorSocket(t *testing.T, handle func(net.Conn)) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "signed-cli-")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "broker.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
			handle(conn)
			_ = conn.Close()
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); <-done; _ = os.RemoveAll(dir) })
	return path
}

func cliIdentitySocket(t *testing.T, workspace string, refuse bool) (string, <-chan protocol.Message) {
	t.Helper()
	requests := make(chan protocol.Message, 2)
	socket := multiOperatorSocket(t, func(c net.Conn) {
		var m protocol.Message
		if err := protocol.ReadFrame(bufio.NewReader(c), &m); err != nil {
			return
		}
		requests <- m
		if m.Type == protocol.TypeOperatorIdentity {
			answerIdentityRequest(c, workspace, refuse)
			return
		}
		_ = protocol.WriteFrame(c, protocol.Message{Type: "ack", Stage: "accepted", MessageID: m.MessageID, Seq: 42,
			Recipients: []protocol.Recipient{{AgentID: "alpha"}}})
	})
	return socket, requests
}

func answerIdentityRequest(c net.Conn, workspace string, refuse bool) {
	if refuse {
		_ = protocol.WriteFrame(c, protocol.Message{Type: "error", Code: "forbidden", Detail: "key refused"})
		return
	}
	_ = protocol.WriteFrame(c, protocol.Message{Type: protocol.TypeOperatorIdentityResult, Identity: &protocol.IdentityView{Workspace: workspace}})
}

func TestOperatorIdentityAcceptanceCLISignsBeforeBrokerSend(t *testing.T) {
	keys := cliSigningKeys(t)
	seed := sha256.Sum256([]byte("cli-workspace"))
	workspace := signing.KeyID(ed25519.NewKeyFromSeed(seed[:]).Public().(ed25519.PublicKey))
	socket, requests := cliIdentitySocket(t, workspace, false)
	var out, stderr bytes.Buffer
	if code := run(context.Background(), []string{"operator", "send", "--socket", socket, "--to", "alpha", "--message-id", "cli-signed", "hello"}, &out, &stderr); code != 0 {
		t.Fatalf("signed send exit %d: %s", code, stderr.String())
	}
	requireCLIRegistration(t, <-requests, keys)
	requireCLISignedSend(t, <-requests, keys, workspace)
}

func requireCLIRegistration(t *testing.T, registration protocol.Message, keys *personkey.Keys) {
	t.Helper()
	if registration.Type != protocol.TypeOperatorIdentity {
		t.Fatalf("send preceded key registration: %+v", registration)
	}
	if registration.IdentityOp != protocol.IdentityOpRegister {
		t.Fatalf("identity operation: %+v", registration)
	}
	if registration.IdentityKey != base64.StdEncoding.EncodeToString(keys.PersonPublic()) {
		t.Fatalf("registered another person key: %+v", registration)
	}
}

func requireCLISignedSend(t *testing.T, sent protocol.Message, keys *personkey.Keys, workspace string) {
	t.Helper()
	if sent.Type != protocol.TypeOperatorSend {
		t.Fatalf("operator client sent the wrong frame: %+v", sent)
	}
	if sent.Seal == "" {
		t.Fatal("operator client sent unsigned despite keys")
	}
	seal, err := protocol.DecodeSeal(sent.Seal)
	if err != nil {
		t.Fatal(err)
	}
	roots := protocol.RootsOf([]protocol.SigningRoot{{KeyID: signing.KeyID(keys.PersonPublic()), Person: true, Workspace: workspace, Approved: true}})
	result := signing.Verify(&seal, roots, time.Now())
	if result.State != signing.Verified {
		t.Fatalf("client signature did not verify: %+v", result)
	}
	if result.Signer == nil {
		t.Fatal("verified client message has no signer")
	}
	if result.Signer.Basis != signing.BasisPerson {
		t.Fatalf("client signer basis: %s", result.Signer.Basis)
	}
	if err := seal.Matches(signing.Frame{MessageID: sent.MessageID, SenderKind: signing.SenderOperator,
		SenderID: signing.OperatorID, Text: "hello", Addresses: []string{"alpha"}}); err != nil {
		t.Fatalf("client signed another message: %v", err)
	}
}

func TestOperatorIdentityAcceptanceRegistrationFailureCannotSilentlyDowngrade(t *testing.T) {
	cliSigningKeys(t)
	socket, requests := cliIdentitySocket(t, "", true)
	var out, stderr bytes.Buffer
	args := []string{"operator", "send", "--socket", socket, "--to", "alpha", "hello"}
	if code := run(context.Background(), args, &out, &stderr); code == 0 {
		t.Fatal("client silently sent unsigned after person-key refusal")
	}
	if first := <-requests; first.Type != protocol.TypeOperatorIdentity {
		t.Fatalf("client skipped registration: %+v", first)
	}
	select {
	case sent := <-requests:
		t.Fatalf("client sent after key refusal: %+v", sent)
	case <-time.After(50 * time.Millisecond):
	}
	unsignedArgs := []string{"operator", "send", "--socket", socket, "--to", "alpha", "--unsigned", "hello"}
	if code := run(context.Background(), unsignedArgs, &out, &stderr); code != 0 {
		t.Fatalf("explicit unsigned send failed: %s", stderr.String())
	}
	sent := <-requests
	if sent.Type != protocol.TypeOperatorSend {
		t.Fatalf("explicit unsigned send did not take the chosen path: %+v", sent)
	}
	if sent.Seal != "" {
		t.Fatalf("explicit unsigned send had a seal: %+v", sent)
	}
}

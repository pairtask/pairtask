//go:build linux

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/personkey"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/pairbook"
)

func TestChatAutoSyncRenewsOwnedSpaceWhileJoinWaits(t *testing.T) {
	d := &flowDirectory{addresses: map[string]pairbook.SignedAddress{}}
	owner, applicant := startFlowNode(t, d), startFlowNode(t, d)
	owner.command(t, "create", "--pairbook", "https://directory.example")
	// Use a short validity period so the real clock reaches half-life.
	d.mu.Lock()
	m := d.current.SpaceMap
	m.Version++
	now := time.Now()
	m.IssuedAt = now.UnixMilli()
	m.ExpiresAt = now.Add(4 * time.Second).UnixMilli()
	short := pairbook.SignSpaceMap(owner.keys.PersonKey(), m)
	d.current = short
	d.mu.Unlock()
	ctx := context.Background()
	offlineOK(t, owner.h.KeepSpaceMap(ctx, m.Space, short))
	var link inviteLink
	offlineOK(t, json.Unmarshal([]byte(owner.command(t, "invite")), &link))
	applicant.command(t, "join", link.Link)
	t.Setenv(personkey.DirEnv, owner.keyDir)
	time.Sleep(time.Until(now.Add(2 * time.Second)))
	stop := startSpaceAutoSync(ctx, operatorCall{socket: owner.socket, timeout: time.Second}, time.Hour)
	t.Cleanup(stop)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, mark, ok, err := owner.st.SpaceMap(ctx, m.Space)
		offlineOK(t, err)
		if ok && mark.Version == m.Version+1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	stop()
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.current.Version != m.Version+1 {
		t.Fatalf("initial pass did not renew v%d: now v%d", m.Version, d.current.Version)
	}
	if len(d.pending) != 1 || len(d.current.People) != 1 {
		t.Fatal("sync changed the pending join or admitted the applicant")
	}
}

func TestChatAutoSyncSkipsForeignOwnerAndReloadsIdentity(t *testing.T) {
	dir := chatSyncIdentity(t)
	path, seen := pairingCLISocket(t, func(map[string]any) map[string]any { return spaceCLIResult() })
	a := spaceAutoSync{call: operatorCall{socket: path, timeout: time.Second}}
	a.pass(context.Background())
	spaceCommandCheck(t, len(seen) == 1, "non-owner made %d requests, want one", len(seen))
	spaceCommandCheck(t, decodeSpaceCLI(t, <-seen).Op == protocol.SpaceOpList, "non-owner performed a write")
	offlineOK(t, os.Rename(dir, dir+"-removed"))
	a.pass(context.Background())
	if len(seen) != 0 {
		t.Fatal("next pass kept an identity that was removed from the host")
	}
}

func TestChatAutoSyncSkipsMissingIdentity(t *testing.T) {
	for _, kind := range []string{"no keys", "no anchor"} {
		t.Run(kind, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "identity")
			if kind == "no anchor" {
				_, err := personkey.Create(dir)
				offlineOK(t, err)
			}
			t.Setenv(personkey.DirEnv, dir)
			path, seen := pairingCLISocket(t, func(map[string]any) map[string]any { return spaceCLIResult() })
			a := spaceAutoSync{call: operatorCall{socket: path, timeout: time.Second}}
			a.pass(context.Background())
			if len(seen) != 0 {
				t.Fatalf("made %d operator calls without a signing identity", len(seen))
			}
		})
	}
}

func TestChatAutoSyncStopCancelsOutstandingCall(t *testing.T) {
	chatSyncIdentity(t)
	release := make(chan struct{})
	path, seen := pairingCLISocket(t, func(map[string]any) map[string]any { <-release; return spaceCLIResult() })
	defer close(release)
	stop := startSpaceAutoSync(context.Background(), operatorCall{socket: path, timeout: 20 * time.Second}, time.Hour)
	defer stop()
	select {
	case req := <-seen:
		if decodeSpaceCLI(t, req).Op != protocol.SpaceOpList {
			t.Fatal("expected list")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not start")
	}
	done := make(chan struct{})
	go func() { stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stop waited for the socket timeout")
	}
}

func TestChatAutoSyncRetriesListOnNextTick(t *testing.T) {
	chatSyncIdentity(t)
	path, seen := pairingCLISocket(t, func(map[string]any) map[string]any {
		return map[string]any{"type": protocol.TypeError, "code": protocol.ErrSpace, "detail": "temporarily unavailable"}
	})
	stop := startSpaceAutoSync(context.Background(), operatorCall{socket: path, timeout: time.Second}, 40*time.Millisecond)
	defer stop()
	for range 2 {
		select {
		case msg := <-seen:
			spaceCommandCheck(t, decodeSpaceCLI(t, msg).Op == protocol.SpaceOpList, "expected a list retry")
		case <-time.After(2 * time.Second):
			t.Fatal("worker failed to retry on its next tick")
		}
	}
}

func chatSyncIdentity(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "person")
	_, err := personkey.Create(dir)
	offlineOK(t, err)
	_, _, err = personkey.CreateAnchor(dir, false, time.Now().Add(-time.Hour))
	offlineOK(t, err)
	t.Setenv(personkey.DirEnv, dir)
	return dir
}

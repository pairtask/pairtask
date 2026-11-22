package hub

import (
	"context"
	"crypto"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/personkey"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/space"
	"github.com/pairtask/pairtask/internal/spaceowner"
	"github.com/pairtask/pairtask/pairbook"
)

type retryDirectory struct {
	space.MapDirectory
	puts      []space.MapPut
	submitted int
	reply     error
}

func (d *retryDirectory) PutMap(_ context.Context, _ space.Pin, p space.MapPut) error {
	d.puts = append(d.puts, p)
	return d.reply
}
func (d *retryDirectory) Map(context.Context, space.Pin) (pairbook.SignedSpaceMap, error) {
	return pairbook.SignedSpaceMap{}, errors.New("read unavailable")
}
func (d *retryDirectory) PostMapInvite(context.Context, space.Pin, pairbook.SignedMapInvite) error {
	return d.reply
}
func (d *retryDirectory) SubmitChange(context.Context, space.Pin, pairbook.SignedChange, *[32]byte) error {
	d.submitted++
	return d.reply
}

type retryFixture struct {
	h       *Hub
	d       *retryDirectory
	me      spaceowner.Me
	binding pairbook.Binding
	m       pairbook.SignedSpaceMap
	pin     space.Pin
}

func newRetryFixture(t *testing.T) retryFixture {
	t.Helper()
	h := newTestHub(t)
	t.Cleanup(h.authority.close)
	if problem := h.loadIdentity(context.Background()); problem != "" {
		t.Fatal(problem)
	}
	d := new(retryDirectory)
	h.SetSpaces(SpaceConfig{Maps: func(string, crypto.Signer) (space.MapDirectory, error) { return d, nil }})
	now := time.Now()
	dir := filepath.Join(t.TempDir(), "keys")
	keys, err := personkey.Create(dir)
	messageWorkerOK(t, err)
	_, _, err = personkey.CreateAnchor(dir, false, now.Add(-time.Hour))
	messageWorkerOK(t, err)
	me, err := spaceowner.Load(keys)
	messageWorkerOK(t, err)
	a, err := h.AcceptBinding(BindingRequest{Lineage: pairbook.LineageHex(me.Lineage), Epoch: 1, Person: me.KeyID(), At: now})
	messageWorkerOK(t, err)
	b := pairbook.Binding{Claim: me.Claim(a.WorkspaceKey, 1, now), Acceptance: a.Acceptance, WorkspaceKey: a.WorkspaceKey}
	m, err := spaceowner.NewSpace(me, spaceowner.Genesis{Space: hubSpaceID, Name: "Owner", Own: b}, now)
	messageWorkerOK(t, err)
	return retryFixture{h, d, me, b, m, space.Pin{Space: m.Space, Base: hubDirectory, Owner: me.KeyID(), JoinedAt: now}}
}
func runMapOp(t *testing.T, h *Hub, r protocol.SpaceRequest, record any) error {
	t.Helper()
	ctx := context.Background()
	raw, err := json.Marshal(record)
	messageWorkerOK(t, err)
	r.Record = raw
	step, refusal := spaceOps[r.Op](h, ctx, r)
	if refusal != nil {
		return refusal
	}
	if step.job == nil {
		return nil
	}
	_, err = step.job(ctx, ctx)
	return err
}

func TestMapOutboxRetryFinishesPublication(t *testing.T) {
	f := newRetryFixture(t)
	ctx := context.Background()
	f.d.reply = errors.New("lost response")
	req := protocol.SpaceRequest{Op: protocol.SpaceOpMapPublish, Space: f.pin.Space, Directory: f.pin.Base}
	err := runMapOp(t, f.h, req, mapPut{Put: space.MapPut{Map: f.m}})
	if err == nil {
		t.Fatal("publish unexpectedly succeeded")
	}
	f.d.reply = nil
	f.h.refreshPass(ctx)
	if len(f.d.puts) != 2 {
		t.Fatalf("got %d map writes", len(f.d.puts))
	}
	_, mark, ok, err := f.h.store.SpaceMap(ctx, f.m.Space)
	messageWorkerOK(t, err)
	if !ok || mark != f.m.Mark() {
		t.Fatal("successful PUT retry lost its local map when the later GET failed")
	}
	items, err := f.h.store.SpaceSends(ctx)
	messageWorkerOK(t, err)
	if len(items) != 0 {
		t.Fatal("completed publish still queued")
	}
}

func TestMapInviteReturnsDirectoryRefusal(t *testing.T) {
	f := newRetryFixture(t)
	ctx := context.Background()
	messageWorkerOK(t, f.h.store.PinSpace(ctx, f.pin))
	messageWorkerOK(t, f.h.KeepSpaceMap(ctx, f.m.Space, f.m))
	invite, err := f.me.Invite(spaceowner.Current{Map: f.m, Mark: f.m.Mark()}, [32]byte{1}, time.Now(), time.Hour)
	messageWorkerOK(t, err)
	denied := &space.Refusal{Status: 403}
	f.d.reply = denied
	err = runMapOp(t, f.h, protocol.SpaceRequest{Op: protocol.SpaceOpMapInvite, Space: f.m.Space}, invite)
	if !errors.Is(err, denied) {
		t.Fatalf("expected directory refusal, got %v", err)
	}
}

func TestMapJoinHonorsExistingDirectoryPin(t *testing.T) {
	f := newRetryFixture(t)
	messageWorkerOK(t, f.h.store.PinSpace(context.Background(), f.pin))
	link := space.Invite{Space: f.m.Space, Owner: f.pin.Owner, Base: "https://another.example", Token: [32]byte{1}}
	ch := f.me.Change(spaceowner.Request{Action: pairbook.ChangeJoin, Space: f.m.Space, Base: f.m.Mark(), Name: "Member", Binding: f.binding, TokenHash: sha256.Sum256(link.Token[:])}, time.Now())
	err := runMapOp(t, f.h, protocol.SpaceRequest{Op: protocol.SpaceOpMapSubmit, Link: link.String()}, ch)
	if err == nil {
		t.Error("conflicting directory pin accepted")
	}
	if f.d.submitted != 0 {
		t.Fatal("join left broker despite conflicting pin")
	}
}

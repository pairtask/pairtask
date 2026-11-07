package messaging_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

type controlProbe struct {
	id            []byte
	pending       *messaging.ContactState
	calls         []string
	closeOnAccept bool
	invalidated   bool
	stored        []messaging.ContactStateStored
	accepted      []messaging.ContactState
	failAt        string
}

func (p *controlProbe) call(name string) error {
	p.calls = append(p.calls, name)
	if p.invalidated {
		return fmt.Errorf("store call after self-invalidation: %s", name)
	}
	if p.failAt == name {
		return fmt.Errorf("control store failure: %s", name)
	}
	return nil
}
func (p *controlProbe) PairingID(context.Context) ([]byte, error) {
	err := p.call("id")
	return p.id, err
}
func (p *controlProbe) PendingNotice(context.Context) (*messaging.ContactState, error) {
	err := p.call("pending")
	return p.pending, err
}
func (p *controlProbe) AcceptNotice(_ context.Context, n messaging.ContactState) (messaging.ControlAnswer, error) {
	if err := p.call("accept"); err != nil {
		return messaging.ControlAnswer{}, err
	}
	p.accepted = append(p.accepted, n)
	p.invalidated = p.closeOnAccept
	return messaging.ControlAnswer{Ack: messaging.ContactStateStored{PairingID: n.PairingID, Gen: n.Gen}, Close: p.closeOnAccept}, nil
}
func (p *controlProbe) NoticeStored(_ context.Context, a messaging.ContactStateStored) error {
	if err := p.call("stored"); err != nil {
		return err
	}
	p.stored = append(p.stored, a)
	return nil
}
func controlSessionOpen(frames *scriptFrames, p *controlProbe, dial bool) (messaging.ControlResult, error) {
	s := messaging.ControlSession{Frames: frames, Store: p, Contact: sessionContact}
	if dial {
		return s.Dial(context.Background())
	}
	return s.Serve(context.Background())
}
func controlNoticeFrame(t *testing.T, n messaging.ContactState) []byte {
	t.Helper()
	b, e := messaging.EncodeContactState(n)
	return sessionFrame(t, b, e)
}
func controlAckFrame(t *testing.T, n messaging.ContactState) []byte {
	t.Helper()
	b, e := messaging.EncodeContactStateStored(messaging.ContactStateStored{PairingID: n.PairingID, Gen: n.Gen})
	return sessionFrame(t, b, e)
}
func controlEnd(t *testing.T) []byte {
	t.Helper()
	b, e := messaging.EncodeEnd()
	return sessionFrame(t, b, e)
}

func controlClosedCalls(t *testing.T, p *controlProbe, dial bool) {
	t.Helper()
	want := []string{"id", "accept"}
	if dial {
		want = []string{"id", "pending", "accept"}
	}
	if !reflect.DeepEqual(p.calls, want) {
		t.Fatalf("store used after invalidation: %v", p.calls)
	}
}

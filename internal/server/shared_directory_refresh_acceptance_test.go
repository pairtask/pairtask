//go:build linux

package server

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

func directoryWaitRound(t *testing.T, side instantSide, before time.Time) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !side.f.h.MessageSyncStatus(side.key).LastSuccess.After(before) {
		if time.Now().After(deadline) {
			t.Fatal("idle directory exchange did not finish")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func directoryChangedEvents(t *testing.T, owner *sessionProbe, contact string) int {
	t.Helper()
	n := 0
	for _, frame := range owner.barrier() {
		if frame.text("type") != protocol.TypeEvent {
			continue
		}
		event := sessionDecode[protocol.SessionEvent](t, frame, "event")
		if event.Kind == protocol.EventContactMessagesChanged && event.ContactKey == contact {
			n++
		}
	}
	return n
}

func TestSharedDirectoryRefreshAcceptanceIdleShareAndUnshareNotifyReceiver(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, unshare := range []bool{false, true} {
			t.Run(fmt.Sprintf("reverse=%v/unshare=%v", reverse, unshare), func(t *testing.T) {
				p := newInstantPair(t)
				sides := p.sides()
				if reverse {
					sides[0], sides[1] = sides[1], sides[0]
				}
				sender, receiver := sides[0], sides[1]
				sender.f.peer("visible-agent", protocol.Version)
				if unshare {
					before := receiver.f.h.MessageSyncStatus(receiver.key).LastSuccess
					sender.shareRemoteAgents(t, "visible-agent")
					directoryWaitRound(t, receiver, before)
				}
				owner := sessionOpen(receiver.f.operatorFixture)
				owner.snapshot()
				before := receiver.f.h.MessageSyncStatus(receiver.key).LastSuccess
				op, want := protocol.ContactOpShare, []string{"visible-agent"}
				if unshare {
					op, want = protocol.ContactOpUnshare, nil
				}
				r := sender.f.send(protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: op, ContactKey: sender.key, ContactAgents: []string{"visible-agent"}, MessageID: "change-directory"})
				operatorEqual(t, "grant change accepted", r.Type, protocol.TypeOperatorContactResult)
				directoryWaitRound(t, receiver, before)
				got, err := receiver.f.st.PeerMessages().PeerDirectory(context.Background(), receiver.key)
				operatorNoError(t, err)
				operatorEqual(t, "receiver directory changed without messages", slices.Equal(got, want), true)
				operatorEqual(t, "directory change pushes one receiver UI event", directoryChangedEvents(t, owner, receiver.key), 1)
				for _, side := range sides {
					operatorEqual(t, "no user messages needed", side.f.count("peer_messages"), 0)
				}
				p.assertOneConnection(t)
			})
		}
	}
}

func TestSharedDirectoryRefreshAcceptanceUnchangedIdleRoundDoesNotNotify(t *testing.T) {
	p := newInstantPair(t)
	sides := p.sides()
	sender, receiver := sides[0], sides[1]
	owner := sessionOpen(receiver.f.operatorFixture)
	owner.snapshot()
	before := receiver.f.h.MessageSyncStatus(receiver.key).LastSuccess
	sender.f.h.SyncMessages(sender.key)
	directoryWaitRound(t, receiver, before)
	operatorEqual(t, "unchanged directory emits no change event", directoryChangedEvents(t, owner, receiver.key), 0)
	p.assertOneConnection(t)
}

package pairing_test

import (
	"bytes"
	"sync"
	"testing"

	"github.com/the-sarge/cpace"
)

func candidateInputs() (cpace.Input, cpace.Input) {
	a := cpace.Input{Password: []byte("047291"), SelfID: []byte("joiner-key"), PeerID: []byte("inviter-key"), Context: []byte("pairtask-evaluation-v1/invite-17"), SessionID: bytes.Repeat([]byte{7}, 32)}
	b := cpace.Input{Password: bytes.Clone(a.Password), SelfID: bytes.Clone(a.PeerID), PeerID: bytes.Clone(a.SelfID), Context: bytes.Clone(a.Context), SessionID: bytes.Clone(a.SessionID)}
	return a, b
}

func candidateStart(t *testing.T, a, b cpace.Input) (*cpace.Initiator, *cpace.Responder, []byte) {
	t.Helper()
	i, msg, err := cpace.Start(a)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = i.Close() })
	r, reply, err := cpace.Respond(b, msg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return i, r, reply
}

func candidateFinishInitiator(t *testing.T, i *cpace.Initiator, reply []byte) ([]byte, *cpace.Session) {
	t.Helper()
	final, session, err := i.Finish(reply)
	acceptanceRequireNoError(t, err)
	t.Cleanup(func() { _ = session.Close() })
	return final, session
}

func candidateFinishResponder(t *testing.T, r *cpace.Responder, final []byte) *cpace.Session {
	t.Helper()
	session, err := r.Finish(final)
	acceptanceRequireNoError(t, err)
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func candidateExport(t *testing.T, session *cpace.Session, purpose string) []byte {
	t.Helper()
	key, err := session.Export([]byte(purpose), nil, 32)
	acceptanceRequireNoError(t, err)
	return key
}

func candidateRequireRejected(t *testing.T, session *cpace.Session, err error) {
	t.Helper()
	acceptanceRequireError(t, err)
	acceptanceRequireEqual(t, "rejected exchange session", session, nil)
}

func TestCandidateAcceptanceConfirmedKeysAndIdentity(t *testing.T) {
	a, b := candidateInputs()
	i, r, reply := candidateStart(t, a, b)
	final, sa := candidateFinishInitiator(t, i, reply)
	sb := candidateFinishResponder(t, r, final)
	ka := candidateExport(t, sa, "test-purpose")
	kb := candidateExport(t, sb, "test-purpose")
	acceptanceRequireBytes(t, "confirmed keys", ka, kb)
	acceptanceRequireBytes(t, "transcript IDs", sa.TranscriptID(), sb.TranscriptID())
	acceptanceRequireBytes(t, "initiator peer identity", sa.PeerID(), b.SelfID)
	acceptanceRequireBytes(t, "responder peer identity", sb.PeerID(), a.SelfID)
	other := candidateExport(t, sa, "other-purpose")
	acceptanceRequireEqual(t, "export purposes collide", bytes.Equal(ka, other), false)
	_ = sa.Close()
	_, err := sa.Export([]byte("test-purpose"), nil, 32)
	acceptanceRequireError(t, err)
}

func TestCandidateAcceptanceMismatchedInputsFail(t *testing.T) {
	cases := []struct {
		name   string
		change func(*cpace.Input)
	}{
		{"wrong-code", func(b *cpace.Input) { b.Password = []byte("047292") }},
		{"context", func(b *cpace.Input) { b.Context = []byte("other-invitation") }},
		{"session", func(b *cpace.Input) { b.SessionID = bytes.Repeat([]byte{8}, 32) }},
		{"self-key", func(b *cpace.Input) { b.SelfID = []byte("substituted-inviter") }},
		{"peer-key", func(b *cpace.Input) { b.PeerID = []byte("substituted-joiner") }},
		{"swapped-roles", func(b *cpace.Input) { b.SelfID, b.PeerID = b.PeerID, b.SelfID }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, b := candidateInputs()
			tc.change(&b)
			i, msg, err := cpace.Start(a)
			if err != nil {
				t.Fatal(err)
			}
			defer i.Close()
			r, reply, err := cpace.Respond(b, msg)
			if err != nil {
				return
			}
			defer r.Close()
			final, session, err := i.Finish(reply)
			candidateRequireRejected(t, session, err)
			acceptanceRequireEqual(t, "failed finish output length", len(final), 0)
			_, session, err = i.Finish(reply)
			candidateRequireRejected(t, session, err)
		})
	}
}

func TestCandidateAcceptanceReplayAndConcurrentUse(t *testing.T) {
	a, b := candidateInputs()
	i, r, reply := candidateStart(t, a, b)
	final, sa, err := i.Finish(reply)
	if err != nil {
		t.Fatal(err)
	}
	defer sa.Close()
	_, r2, _ := candidateStart(t, a, b)
	replaySession, replayErr := r2.Finish(final)
	candidateRequireRejected(t, replaySession, replayErr)
	var wg sync.WaitGroup
	successes := make(chan *cpace.Session, 8)
	copyR := *r
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			target := []*cpace.Responder{r, &copyR}[n%2]
			s, err := target.Finish(final)
			if err == nil {
				successes <- s
			}
		}(n)
	}
	wg.Wait()
	close(successes)
	count := 0
	for s := range successes {
		count++
		_ = s.Close()
	}
	if count != 1 {
		t.Fatalf("single-use state has %d successful finishers", count)
	}
}

func candidateMalformedFrame(msg []byte, kind string) []byte {
	switch kind {
	case "empty":
		return nil
	case "truncated":
		return msg[:len(msg)-1]
	case "trailing":
		return append(bytes.Clone(msg), 0)
	case "corrupt":
		changed := bytes.Clone(msg)
		changed[len(changed)-1] ^= 1
		return changed
	default:
		panic("unknown malformed frame case: " + kind)
	}
}

func candidateMalformedExchange(t *testing.T, phase, kind string) {
	t.Helper()
	a, b := candidateInputs()
	i, r, reply := candidateStart(t, a, b)
	if phase == "reply" {
		_, session, err := i.Finish(candidateMalformedFrame(reply, kind))
		candidateRequireRejected(t, session, err)
		return
	}
	final, _ := candidateFinishInitiator(t, i, reply)
	session, err := r.Finish(candidateMalformedFrame(final, kind))
	candidateRequireRejected(t, session, err)
}

func candidateMalformedInitialFrames(t *testing.T) {
	t.Helper()
	_, b := candidateInputs()
	bad := [][]byte{nil, {0}, {0xc1, 1, 1}, bytes.Repeat([]byte{0}, 131073), {0xc1, 1, 1, 0xff, 0xff, 0xff, 0xff}}
	for n, msg := range bad {
		r, _, err := cpace.Respond(b, msg)
		if r != nil {
			_ = r.Close()
		}
		if err == nil {
			t.Fatalf("malformed initial frame %d accepted", n)
		}
	}
}

func TestCandidateAcceptanceMalformedMessages(t *testing.T) {
	candidateMalformedInitialFrames(t)
	for _, phase := range []string{"reply", "confirm"} {
		t.Run(phase, func(t *testing.T) {
			for _, kind := range []string{"empty", "truncated", "trailing", "corrupt"} {
				t.Run(kind, func(t *testing.T) { candidateMalformedExchange(t, phase, kind) })
			}
		})
	}
}

func TestCandidateAcceptanceCopiesInputsAndOutputs(t *testing.T) {
	a, b := candidateInputs()
	i, r, reply := candidateStart(t, a, b)
	for _, v := range [][]byte{a.Password, a.SelfID, a.PeerID, a.Context, a.SessionID, b.Password, b.SelfID, b.PeerID, b.Context, b.SessionID} {
		clear(v)
	}
	final, sa, err := i.Finish(reply)
	if err != nil {
		t.Fatalf("caller mutation changed exchange: %v", err)
	}
	defer sa.Close()
	sb, err := r.Finish(final)
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Close()
	id := sa.PeerID()
	saved := bytes.Clone(id)
	clear(id)
	if !bytes.Equal(sa.PeerID(), saved) {
		t.Fatal("caller could mutate authenticated identity")
	}
}

func TestCandidateAcceptanceAssociatedDataNeedsApplicationCheck(t *testing.T) {
	a, b := candidateInputs()
	a.LocalAssociatedData = []byte("expected-channel-A")
	b.LocalAssociatedData = []byte("different-channel-B")
	i, r, reply := candidateStart(t, a, b)
	final, sa, err := i.Finish(reply)
	if err != nil {
		t.Fatal(err)
	}
	defer sa.Close()
	sb, err := r.Finish(final)
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Close()
	// AD is authenticated, not required to be equal: our shared channel binding
	// must go in Context/SessionID, or be compared explicitly after confirmation.
	if !bytes.Equal(sa.PeerAssociatedData(), b.LocalAssociatedData) || bytes.Equal(sa.PeerAssociatedData(), a.LocalAssociatedData) {
		t.Fatal("unexpected associated-data semantics")
	}
}

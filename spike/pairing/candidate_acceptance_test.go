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

func TestCandidateAcceptanceConfirmedKeysAndIdentity(t *testing.T) {
	a, b := candidateInputs()
	i, r, reply := candidateStart(t, a, b)
	last, sa, err := i.Finish(reply)
	if err != nil {
		t.Fatal(err)
	}
	defer sa.Close()
	sb, err := r.Finish(last)
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Close()
	ka, err := sa.Export([]byte("test-purpose"), nil, 32)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := sb.Export([]byte("test-purpose"), nil, 32)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ka, kb) || !bytes.Equal(sa.TranscriptID(), sb.TranscriptID()) {
		t.Fatal("confirmed peers disagree")
	}
	if !bytes.Equal(sa.PeerID(), b.SelfID) || !bytes.Equal(sb.PeerID(), a.SelfID) {
		t.Fatal("peer identity changed")
	}
	other, err := sa.Export([]byte("other-purpose"), nil, 32)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(ka, other) {
		t.Fatal("export purpose not separated")
	}
	_ = sa.Close()
	if _, err := sa.Export([]byte("test-purpose"), nil, 32); err == nil {
		t.Fatal("closed session exports secrets")
	}
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
			if err == nil || session != nil || len(final) != 0 {
				t.Fatal("mismatched inputs yielded authenticated output")
			}
			if _, session, err := i.Finish(reply); err == nil || session != nil {
				t.Fatal("failed state was reusable")
			}
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
	if s, err := r2.Finish(final); err == nil || s != nil {
		t.Fatal("confirmation replay accepted by a fresh exchange")
	}
	var wg sync.WaitGroup
	successes := make(chan *cpace.Session, 8)
	copyR := *r
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			target := r
			if n%2 == 1 {
				target = &copyR
			}
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

func TestCandidateAcceptanceMalformedMessages(t *testing.T) {
	a, b := candidateInputs()
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
	for _, phase := range []string{"reply", "confirm"} {
		t.Run(phase, func(t *testing.T) {
			for _, kind := range []string{"empty", "truncated", "trailing", "corrupt"} {
				t.Run(kind, func(t *testing.T) {
					i, r, reply := candidateStart(t, a, b)
					msg := reply
					if phase == "confirm" {
						last, s, err := i.Finish(reply)
						if err != nil {
							t.Fatal(err)
						}
						defer s.Close()
						msg = last
					}
					switch kind {
					case "empty":
						msg = nil
					case "truncated":
						msg = msg[:len(msg)-1]
					case "trailing":
						msg = append(bytes.Clone(msg), 0)
					case "corrupt":
						msg = bytes.Clone(msg)
						msg[len(msg)-1] ^= 1
					}
					var s *cpace.Session
					var err error
					if phase == "reply" {
						_, s, err = i.Finish(msg)
					} else {
						s, err = r.Finish(msg)
					}
					if err == nil || s != nil {
						t.Fatal("malformed message produced a session")
					}
				})
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

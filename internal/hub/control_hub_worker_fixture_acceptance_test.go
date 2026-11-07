package hub

import (
	"context"
	"database/sql"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

type controlWorkerFixture struct {
	*addressFixture
	raw     *sql.DB
	tick    chan time.Time
	now     atomic.Int64
	started chan string
}

func newControlWorkerFixture(t *testing.T) *controlWorkerFixture {
	t.Helper()
	f := newAddressFixture(t)
	f.joined(t)
	raw, err := sql.Open("sqlite", strings.TrimSuffix(f.h.store.PeerDir(), ".peer"))
	messageWorkerOK(t, err)
	t.Cleanup(func() { raw.Close() })
	// This side holds an address but is not the ordinary dialer.
	_, err = raw.Exec(`UPDATE peer_contacts SET invited=1 WHERE identity_key=?`, f.key)
	messageWorkerOK(t, err)
	messageSyncEqual(t, dials(f.contact(t), f.h.identity.Fingerprint()), false)
	bound, err := f.h.store.PeerMessages().Admit(context.Background(), f.key)
	messageWorkerOK(t, err)
	messageWorkerOK(t, bound.SawVersion(context.Background(), 9))
	w := &controlWorkerFixture{addressFixture: f, raw: raw, tick: make(chan time.Time), started: make(chan string, 16)}
	w.now.Store(time.Now().Unix())
	return w
}
func (w *controlWorkerFixture) run(t *testing.T) {
	t.Helper()
	w.h.SetPairing(PairingConfig{Clock: func() time.Time { return time.Unix(w.now.Load(), 0) }, MessageTick: w.tick,
		Dial: func(ctx context.Context, destination string) (net.Conn, error) {
			w.started <- destination
			return nil, context.DeadlineExceeded
		}})
	go w.h.Run(w.ctx)
	t.Cleanup(func() { w.cancel(); addressAwait(t, w.h.Done()) })
	w.h.startMessageWorker(w.ctx)
}
func (w *controlWorkerFixture) change(t *testing.T, op, id string) int64 {
	t.Helper()
	reply := addressAwait(t, w.submit(protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: op, ContactKey: w.key, MessageID: id}))
	messageSyncEqual(t, reply.Type, protocol.TypeOperatorContactResult)
	var gen int64
	messageWorkerOK(t, w.raw.QueryRow(`SELECT generation FROM peer_contacts WHERE identity_key=?`, w.key).Scan(&gen))
	return gen
}
func (w *controlWorkerFixture) advance(seconds int64) {
	w.now.Add(seconds)
	w.tick <- time.Unix(w.now.Load(), 0)
}
func (w *controlWorkerFixture) attempts(t *testing.T, gen int64, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		var got int
		err := w.raw.QueryRow(`SELECT attempts FROM contact_notices_out WHERE contact=? AND gen=?`, w.key, gen).Scan(&got)
		matches := err == nil && got == want
		if matches {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("attempts=%d err=%v, want %d", got, err, want)
		}
		time.Sleep(time.Millisecond)
	}
}
func (w *controlWorkerFixture) noDial(t *testing.T) {
	t.Helper()
	select {
	case destination := <-w.started:
		t.Fatalf("unexpected control dial to %s", destination)
	case <-time.After(80 * time.Millisecond):
	}
}

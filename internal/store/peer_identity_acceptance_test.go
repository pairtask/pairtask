//go:build unix

package store_test

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/store"
)

func TestPeerIdentityStoreAcceptanceMarkerSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broker.db")
	db := pairSQLite(t, path)
	ctx := context.Background()
	_, found, err := db.IdentityFingerprint(ctx)
	pairOK(t, err)
	pairEqual(t, found, false)
	fp := "sha256:" + strings.Repeat("a", 64)
	pairOK(t, db.InitIdentity(ctx, fp))
	pairOK(t, db.InitIdentity(ctx, fp))
	err = db.InitIdentity(ctx, "sha256:"+strings.Repeat("b", 64))
	pairEqual(t, errors.Is(err, peer.ErrIdentityMismatch), true)
	pairOK(t, db.Close())
	reopened := pairSQLite(t, path)
	got, found, err := reopened.IdentityFingerprint(ctx)
	pairOK(t, err)
	pairEqual(t, found, true)
	pairEqual(t, got, fp)
	var count int
	pairOK(t, pairRaw(t, path).QueryRow("SELECT count(*) FROM peer_identity").Scan(&count))
	pairEqual(t, count, 1)
}

func TestPeerIdentityStoreAcceptanceConcurrentMarkersHaveOneWinner(t *testing.T) {
	db := pairSQLite(t, filepath.Join(t.TempDir(), "broker.db"))
	var wg sync.WaitGroup
	type result struct {
		fp  string
		err error
	}
	results := make(chan result, 12)
	for n := range 12 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			fp := fmt.Sprintf("sha256:%064x", n)
			results <- result{fp, db.InitIdentity(context.Background(), fp)}
		}(n)
	}
	wg.Wait()
	close(results)
	winners := 0
	winner := ""
	for r := range results {
		if r.err == nil {
			winners++
			winner = r.fp
		} else {
			pairEqual(t, errors.Is(r.err, peer.ErrIdentityMismatch), true)
		}
	}
	pairEqual(t, winners, 1)
	got, found, err := db.IdentityFingerprint(context.Background())
	pairOK(t, err)
	pairEqual(t, found, true)
	pairEqual(t, got, winner)
}

func TestPeerIdentityStoreAcceptanceDatabasePathsAreIsolated(t *testing.T) {
	base := t.TempDir()
	var first string
	for _, name := range []string{"first.db", "second.db"} {
		path := filepath.Join(base, name)
		db := pairSQLite(t, path)
		pairEqual(t, db.PeerDir(), path+".peer")
		id, err := peer.LoadOrCreateIdentity(context.Background(), db.PeerDir(), db, rand.Reader)
		pairOK(t, err)
		if first == "" {
			first = id.Fingerprint()
		} else {
			pairEqual(t, id.Fingerprint() != first, true)
		}
	}
	for _, path := range []string{"", ":memory:"} {
		db, err := store.Open(path)
		pairOK(t, err)
		pairEqual(t, db.PeerDir(), "")
		pairOK(t, db.Close())
	}
}

func TestPeerIdentityStoreAcceptanceKeyLossAfterRestartIsVisible(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broker.db")
	db := pairSQLite(t, path)
	id, err := peer.LoadOrCreateIdentity(context.Background(), db.PeerDir(), db, rand.Reader)
	pairOK(t, err)
	fp := id.Fingerprint()
	pairOK(t, db.Close())
	pairOK(t, os.Remove(filepath.Join(path+".peer", "identity.key")))
	reopened := pairSQLite(t, path)
	_, err = peer.LoadOrCreateIdentity(context.Background(), reopened.PeerDir(), reopened, rand.Reader)
	pairEqual(t, errors.Is(err, peer.ErrIdentityMissing), true)
	stored, found, err := reopened.IdentityFingerprint(context.Background())
	pairOK(t, err)
	pairEqual(t, found, true)
	pairEqual(t, stored, fp)
	_, err = os.Lstat(filepath.Join(path+".peer", "identity.key"))
	pairEqual(t, errors.Is(err, os.ErrNotExist), true)
}

func TestPeerIdentityStoreAcceptanceFailedMarkerWriteRecoversSameKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broker.db")
	db := pairSQLite(t, path)
	raw := pairRaw(t, path)
	_, err := raw.Exec(`CREATE TRIGGER fail_identity BEFORE INSERT ON peer_identity BEGIN SELECT RAISE(ABORT,'marker unavailable'); END`)
	pairOK(t, err)
	_, err = peer.LoadOrCreateIdentity(context.Background(), db.PeerDir(), db, rand.Reader)
	if err == nil {
		t.Fatal("marker failure reported success")
	}
	before, err := os.ReadFile(filepath.Join(db.PeerDir(), "identity.key"))
	pairOK(t, err)
	_, err = raw.Exec("DROP TRIGGER fail_identity")
	pairOK(t, err)
	id, err := peer.LoadOrCreateIdentity(context.Background(), db.PeerDir(), db, rand.Reader)
	pairOK(t, err)
	after, err := os.ReadFile(filepath.Join(db.PeerDir(), "identity.key"))
	pairOK(t, err)
	pairEqual(t, string(before), string(after))
	fp, found, err := db.IdentityFingerprint(context.Background())
	pairOK(t, err)
	pairEqual(t, found, true)
	pairEqual(t, fp, id.Fingerprint())
}

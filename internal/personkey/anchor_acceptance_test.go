package personkey_test

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/identity"
	"github.com/pairtask/pairtask/internal/personkey"
)

func checkAnchorError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func newHostKeys(t *testing.T) (string, *personkey.Keys) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "host")
	keys, err := personkey.Create(dir)
	checkAnchorError(t, err)
	return dir, keys
}

func checkAnchorOnDisk(t *testing.T, dir string, want []byte) {
	t.Helper()
	keys, err := personkey.Load(dir)
	checkAnchorError(t, err)
	got, err := keys.Anchor()
	checkAnchorError(t, err)
	if !bytes.Equal(got, want) {
		t.Fatal("reloaded anchor differs from original")
	}
}

func checkRecoveryMaterial(t *testing.T, a identity.Anchor, code string, recovery bool) {
	t.Helper()
	if (code != "") != recovery {
		t.Fatal("unexpected recovery code presence")
	}
	if (len(a.Recovery) > 0) != recovery {
		t.Fatal("unexpected recovery key presence")
	}
	if recovery {
		key, err := personkey.DecodeRecovery(code)
		checkAnchorError(t, err)
		if !key.Public().(ed25519.PublicKey).Equal(a.Recovery) {
			t.Fatal("printed code does not recover anchored key")
		}
	}
}

func checkCodeAbsentFromDisk(t *testing.T, dir, code string) {
	t.Helper()
	files, err := os.ReadDir(dir)
	checkAnchorError(t, err)
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(dir, f.Name()))
		checkAnchorError(t, err)
		if bytes.Contains(data, []byte(code)) {
			t.Fatal("one-time recovery code stored on disk")
		}
	}
}

func TestHostAnchorSurvivesReload(t *testing.T) {
	for _, tc := range []struct {
		name     string
		recovery bool
	}{{"with recovery", true}, {"explicitly without recovery", false}} {
		t.Run(tc.name, func(t *testing.T) {
			dir, keys := newHostKeys(t)
			raw, code, err := personkey.CreateAnchor(dir, tc.recovery, time.Unix(1700000000, 0))
			checkAnchorError(t, err)
			a, err := identity.ParseAnchor(raw)
			checkAnchorError(t, err)
			checkAnchorError(t, a.Check())
			if !a.Person.Equal(keys.PersonPublic()) {
				t.Fatal("anchor names a different person")
			}
			checkRecoveryMaterial(t, a, code, tc.recovery)
			_, again, err := personkey.CreateAnchor(dir, !tc.recovery, time.Unix(1700000001, 0))
			if !errors.Is(err, personkey.ErrAnchorExists) || again != "" {
				t.Fatal("expected registration retry to refuse without a code")
			}
			checkAnchorOnDisk(t, dir, raw)
			if tc.recovery {
				checkCodeAbsentFromDisk(t, dir, code)
			}
		})
	}
}

type registrationResult struct {
	raw  []byte
	code string
	err  error
}

func TestConcurrentHostAnchorCreation(t *testing.T) {
	dir, _ := newHostKeys(t)
	results := registerConcurrently(dir)
	var winner []byte
	count := 0
	for r := range results {
		if r.code == "" {
			if !errors.Is(r.err, personkey.ErrAnchorExists) {
				t.Fatalf("registration loser: %v", r.err)
			}
			continue
		}
		if r.err != nil {
			t.Fatal("failed anchor registration returned recovery material")
		}
		winner = r.raw
		count++
	}
	if count != 1 {
		t.Fatalf("recovery codes issued: %d, want 1", count)
	}
	checkAnchorOnDisk(t, dir, winner)
}

func registerConcurrently(dir string) <-chan registrationResult {
	results := make(chan registrationResult, 8)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			raw, code, err := personkey.CreateAnchor(dir, true, time.Now())
			results <- registrationResult{raw, code, err}
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	return results
}

func TestRecoveryCodePublishedVector(t *testing.T) {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i)
	}
	const vector = "PTR1-AAAQE-AYEAU-DAOCA-JBIFQ-YDIOB-4IBCE-QTCQK-RMFYY-DENBW-HA5DY-PUPZY"
	code, err := personkey.EncodeRecovery(seed)
	if err != nil || code != vector {
		t.Fatal("recovery encoder differs from published byte vector")
	}
	key := ed25519.NewKeyFromSeed(seed)
	for _, input := range []string{vector, strings.ToLower(vector), strings.ReplaceAll(vector, "-", " \n")} {
		got, err := personkey.DecodeRecovery(input)
		if err != nil || !bytes.Equal(key, got) {
			t.Fatal("valid transcription failed")
		}
	}
	checkInvalidRecoveryTranscriptions(t, vector)
}

func checkInvalidRecoveryTranscriptions(t *testing.T, vector string) {
	t.Helper()
	for _, input := range []string{"", vector[1:], vector + "A", "PTR2" + vector[4:], strings.Replace(vector, "AAAQE", "AAAQA", 1), vector[:len(vector)-1] + "A", vector[:len(vector)-1] + "Z"} {
		if _, err := personkey.DecodeRecovery(input); !errors.Is(err, personkey.ErrRecoveryCode) {
			t.Fatal("invalid recovery code accepted")
		}
	}
}

func TestHostAnchorRefusesMismatchedPerson(t *testing.T) {
	one, _ := newHostKeys(t)
	two, _ := newHostKeys(t)
	other, _, err := personkey.CreateAnchor(one, false, time.Now())
	checkAnchorError(t, err)
	path := filepath.Join(two, "anchor.bin")
	checkAnchorError(t, os.WriteFile(path, other, 0600))
	k, err := personkey.Load(two)
	if err == nil {
		_, err = k.Anchor()
	}
	if !errors.Is(err, personkey.ErrAnchorInvalid) {
		t.Fatalf("mismatched anchor error: %v", err)
	}
	_, code, err := personkey.CreateAnchor(two, true, time.Now())
	if err == nil || code != "" {
		t.Fatal("registration overwrote mismatched lineage")
	}
	got, err := os.ReadFile(path)
	checkAnchorError(t, err)
	if !bytes.Equal(got, other) {
		t.Fatal("damaged anchor was changed")
	}
}

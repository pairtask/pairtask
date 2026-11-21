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

func TestHostAnchorSurvivesReload(t *testing.T) {
	for _, tc := range []struct {
		name     string
		recovery bool
	}{{"with recovery", true}, {"explicitly without recovery", false}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "host")
			keys, err := personkey.Create(dir)
			if err != nil {
				t.Fatal(err)
			}
			raw, code, err := personkey.CreateAnchor(dir, tc.recovery, time.Unix(1700000000, 0))
			if err != nil {
				t.Fatal(err)
			}
			a, err := identity.ParseAnchor(raw)
			if err != nil || a.Check() != nil || !bytes.Equal(a.Person, keys.PersonPublic()) {
				t.Fatal("anchor does not verify under original host person key")
			}
			if (code != "") != tc.recovery || (len(a.Recovery) > 0) != tc.recovery {
				t.Fatal("recovery mode does not match anchor")
			}
			if tc.recovery {
				recovered, err := personkey.DecodeRecovery(code)
				if err != nil || !bytes.Equal(recovered.Public().(ed25519.PublicKey), a.Recovery) {
					t.Fatal("printed code does not recover anchored key")
				}
			}
			_, again, retryErr := personkey.CreateAnchor(dir, !tc.recovery, time.Unix(1700000001, 0))
			if again != "" || !errors.Is(retryErr, personkey.ErrAnchorExists) {
				t.Fatal("retry disclosed another recovery code")
			}
			reloaded, err := personkey.Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			held, err := reloaded.Anchor()
			if err != nil || !bytes.Equal(held, raw) {
				t.Fatal("host lineage changed after retry and reload")
			}
			files, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, file := range files {
				data, err := os.ReadFile(filepath.Join(dir, file.Name()))
				if err != nil {
					t.Fatal(err)
				}
				if code != "" && bytes.Contains(data, []byte(code)) {
					t.Fatal("one-time recovery code written into host identity directory")
				}
			}
		})
	}
}

func TestConcurrentHostAnchorCreation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "host")
	if _, err := personkey.Create(dir); err != nil {
		t.Fatal(err)
	}
	type result struct {
		raw  []byte
		code string
		err  error
	}
	results := make(chan result, 8)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			raw, code, err := personkey.CreateAnchor(dir, true, time.Now())
			results <- result{raw, code, err}
		}()
	}
	close(start)
	workers.Wait()
	close(results)
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
	keys, err := personkey.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	held, err := keys.Anchor()
	if err != nil || !bytes.Equal(held, winner) {
		t.Fatal("stored anchor differs from winning registration")
	}
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
	for _, input := range []string{"", vector[1:], vector + "A", "PTR2" + vector[4:], strings.Replace(vector, "AAAQE", "AAAQA", 1), vector[:len(vector)-1] + "A", vector[:len(vector)-1] + "Z"} {
		if _, err := personkey.DecodeRecovery(input); !errors.Is(err, personkey.ErrRecoveryCode) {
			t.Fatal("invalid recovery code accepted")
		}
	}
}

func TestHostAnchorRefusesMismatchedPerson(t *testing.T) {
	one, two := filepath.Join(t.TempDir(), "one"), filepath.Join(t.TempDir(), "two")
	for _, dir := range []string{one, two} {
		if _, err := personkey.Create(dir); err != nil {
			t.Fatal(err)
		}
	}
	other, _, err := personkey.CreateAnchor(one, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(two, "anchor.bin")
	if err := os.WriteFile(path, other, 0600); err != nil {
		t.Fatal(err)
	}
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
	if err != nil || !bytes.Equal(got, other) {
		t.Fatal("damaged anchor was changed")
	}
}

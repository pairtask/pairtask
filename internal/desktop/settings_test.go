package desktop

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
)

func TestSettingsGivenTogetherAreAllKept(t *testing.T) {
	for i := range 20 {
		dir := t.TempDir()
		var wg sync.WaitGroup
		start := make(chan struct{})
		for _, given := range []Settings{{Name: "Box"}, {Pairbook: "https://book.example"}, {Address: "box:7445"}} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if _, _, err := storeSettings(dir, given); err != nil {
					t.Error(err)
				}
			}()
		}
		close(start)
		wg.Wait()
		s, err := ReadSettings(dir)
		if err != nil {
			t.Fatal(err)
		}
		if s.Name != "Box" || s.Pairbook != "https://book.example" || s.Address != "box:7445" {
			t.Fatalf("round %d kept %+v", i, s)
		}
	}
}

func TestARunningCopyUsesTheLatestPairbook(t *testing.T) {
	dir, err := os.MkdirTemp("", "pt-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	ctx := context.Background()
	first, err := Start(ctx, Config{Dir: dir, Pairbook: "https://old.example"})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Start(ctx, Config{Dir: dir, Pairbook: "https://new.example"})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	r, err := first.pairbookJoin(ctx, PairbookJoinArgs{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.Guidance, "new.example") {
		t.Fatalf("the copy started first still points at its own Pairbook: %s", r.Guidance)
	}
}

package peer_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/pairtask/pairtask/internal/peer"
)

func TestPairingNameVectorsAcceptancePrintableUTF8ByteBoundaries(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "pairing_name_vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Valid bool   `json:"valid"`
	}
	if err = json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, v := range vectors {
		t.Run(v.ID, func(t *testing.T) {
			if got := peer.CheckPairingName(v.Name) == nil; got != v.Valid {
				t.Fatalf("valid=%v, want %v", got, v.Valid)
			}
		})
	}
}

func TestPairingNameVectorsAcceptanceInvalidUTF8IsRejected(t *testing.T) {
	if peer.CheckPairingName(string([]byte{0xff})) == nil {
		t.Fatal("invalid UTF-8 name accepted")
	}
}

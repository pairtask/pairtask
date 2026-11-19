package messaging

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fileRefSample struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

func fileRefsMessage(t *testing.T, refs []fileRefSample) Message {
	t.Helper()
	data, err := json.Marshal(map[string]any{"ID": "refs-message", "N": 7, "CreatedAt": "2026-10-28T12:00:00Z", "Text": "opaque file metadata", "From": "sender", "To": []string{"worker"}, "files": refs})
	if err != nil {
		t.Fatal(err)
	}
	var m Message
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func fileRefsOf(t *testing.T, m Message) []fileRefSample {
	t.Helper()
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Files []fileRefSample `json:"files"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out.Files
}

func fileRefValid() fileRefSample {
	return fileRefSample{Path: "../../opaque/not-opened.bin", Size: 42, SHA256: strings.Repeat("a", 64)}
}

func fileRefsEncode(t *testing.T, m Message) []byte {
	t.Helper()
	wire, err := encoderOf(m)(m)
	if err != nil {
		t.Fatal(err)
	}
	return wire
}

func TestFileRefsWireAcceptanceRoundTripPreservesOrderAndRequiresVersion12(t *testing.T) {
	for _, count := range []int{1, 8} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			refs := make([]fileRefSample, count)
			for i := range refs {
				refs[i] = fileRefValid()
				refs[i].Path = fmt.Sprintf("../opaque/%d.bin", i)
				refs[i].Size = int64(i)
			}
			if count == 8 {
				refs[7].Path = strings.Repeat("é", 256)
				refs[7].Size = (1 << 53) - 1
			}
			m := fileRefsMessage(t, refs)
			wire := fileRefsEncode(t, m)
			kind, err := Kind(wire)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := (Session{Version: 12}).decodeMessage(kind, wire)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(fileRefsOf(t, decoded), refs) {
				t.Fatal("references dropped, reordered or changed")
			}
			if _, err := (Session{Version: 11}).decodeMessage(kind, wire); err == nil {
				t.Fatal("old session accepted refs frame")
			}
			sealed, err := EncodeSealed(wire, []byte("opaque-seal"))
			if err != nil {
				t.Fatal(err)
			}
			unsealed, err := (Session{Version: 12}).decodeMessage(FrameSealed, sealed)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(fileRefsOf(t, unsealed), refs) {
				t.Fatal("sealed envelope lost references")
			}
		})
	}
}

func TestFileRefsWireAcceptanceEveryFieldAndOrderBindHash(t *testing.T) {
	first, second := fileRefValid(), fileRefValid()
	second.Path = "second.bin"
	original := fileRefsMessage(t, []fileRefSample{first, second}).Hash()
	variants := map[string][]fileRefSample{"none": nil, "removed": {first}, "reordered": {second, first}}
	for _, field := range []string{"path", "size", "digest"} {
		changed := first
		switch field {
		case "path":
			changed.Path = "elsewhere.bin"
		case "size":
			changed.Size++
		case "digest":
			changed.SHA256 = strings.Repeat("b", 64)
		}
		variants[field] = []fileRefSample{changed, second}
	}
	for name, refs := range variants {
		t.Run(name, func(t *testing.T) {
			if fileRefsMessage(t, refs).Hash() == original {
				t.Fatal("file metadata mutation not covered by message identity")
			}
		})
	}
}

func TestFileRefsWireAcceptanceRejectsInvalidBounds(t *testing.T) {
	for _, field := range []string{"count", "path-size", "path-control", "negative-size", "unsafe-size", "digest-case", "digest-short", "digest-nonhex"} {
		t.Run(field, func(t *testing.T) {
			r := fileRefValid()
			switch field {
			case "path-size":
				r.Path = strings.Repeat("é", 257)
			case "path-control":
				r.Path = "bad\nlabel"
			case "negative-size":
				r.Size = -1
			case "unsafe-size":
				r.Size = 1 << 53
			case "digest-case":
				r.SHA256 = strings.Repeat("A", 64)
			case "digest-short":
				r.SHA256 = "a"
			case "digest-nonhex":
				r.SHA256 = strings.Repeat("z", 64)
			}
			refs := []fileRefSample{r}
			if field == "count" {
				for len(refs) < 9 {
					refs = append(refs, r)
				}
			}
			m := fileRefsMessage(t, refs)
			if _, err := encoderOf(m)(m); err == nil {
				t.Fatal("invalid references encoded")
			}
		})
	}
}

func TestFileRefsWireAcceptanceNoRefsKeepLegacyBytesAndHash(t *testing.T) {
	m := Message{ID: "legacy", N: 7, CreatedAt: time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC), Text: "unchanged wire identity"}
	wire := fileRefsEncode(t, m)
	want := "010200066c6567616379000800000000000000070014323032362d31302d32385431323a30303a30305a0017756e6368616e6765642077697265206964656e74697479"
	if hex.EncodeToString(wire) != want {
		t.Fatal("no-refs legacy frame changed")
	}
	hash := m.Hash()
	if hex.EncodeToString(hash[:]) != "bbb373ddd28b94f51a02faaa6177e4f38af05822dbe22981426372d9bac8e074" {
		t.Fatal("no-refs legacy hash changed")
	}
	old, err := (Session{Version: 10}).sealFrame(context.Background(), m, wire)
	if err != nil {
		t.Fatal(err)
	}
	new, err := (Session{Version: 12}).sealFrame(context.Background(), m, wire)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(old, new) {
		t.Fatal("no-refs sealed envelope changed at v12")
	}
}

package messaging_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/signing"
)

type sealFileRef struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

func sealFileSamples() []sealFileRef {
	return []sealFileRef{{"first.bin", 7, strings.Repeat("a", 64)}, {"second.bin", 9, strings.Repeat("b", 64)}}
}

func sealFilesOn(t *testing.T, target any, files []sealFileRef) {
	t.Helper()
	data, err := json.Marshal(target)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	fields["files"], err = json.Marshal(files)
	if err != nil {
		t.Fatal(err)
	}
	data, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatal(err)
	}
}

func sealFilesSigned(t *testing.T, files []sealFileRef) messaging.Message {
	t.Helper()
	m, _ := sealOperatorMessage(t, sealBaseInbound(), sealSigner{"refs-person", "refs-device", sessionContact})
	sealFilesOn(t, &m, files)
	seal, err := signing.ParseSeal(m.Seal)
	if err != nil {
		t.Fatal(err)
	}
	link := protocol.PeerLink{Contact: sessionContact, Receiver: sealReceiver, To: m.To}
	sealFilesOn(t, &link, files)
	seal.Header.Recipients = link.Recipients()
	m.Seal = signing.Sign(sealTestKey("refs-device"), seal.Chain, seal.RootKey, seal.Header).Marshal()
	return m
}

type sealFileCase struct {
	name             string
	signed, received []sealFileRef
	stored           bool
}

func sealFileCases() []sealFileCase {
	files := sealFileSamples()
	path, size, digest := slices.Clone(files), slices.Clone(files), slices.Clone(files)
	path[0].Path, size[0].Size, digest[0].SHA256 = "changed.bin", 8, strings.Repeat("c", 64)
	return []sealFileCase{
		{"unchanged", files, files, true}, {"legacy", nil, nil, true},
		{"removed-all", files, nil, false}, {"removed-one", files, files[:1], false},
		{"path", files, path, false}, {"size", files, size, false}, {"digest", files, digest, false},
		{"reordered", files, []sealFileRef{files[1], files[0]}, false},
		{"added-to-legacy", nil, files, false},
	}
}

func TestFileRefsSealAcceptanceStoreRejectsChangedMetadataBeforePersistence(t *testing.T) {
	for _, tc := range sealFileCases() {
		t.Run(tc.name, func(t *testing.T) {
			db := sealInboundFixture(t)
			m := sealFilesSigned(t, tc.signed)
			sealFilesOn(t, &m, tc.received)
			answer, err := db.PeerMessages().Accept(context.Background(), sessionContact, m, sessionAt)
			if tc.stored {
				requireInboundStored(t, answer, err)
				inboundPage(t, db, 1)
				return
			}
			requireInboundRefusal(t, answer, err, messaging.ReasonInvalidSeal)
			inboundPage(t, db, 0)
		})
	}
}

func sealFilesLinked(t *testing.T) (protocol.PeerLink, string) {
	t.Helper()
	plain := protocol.PeerLink{Receiver: sealReceiver, To: []string{"worker"}}
	withFiles := plain
	sealFilesOn(t, &withFiles, sealFileSamples())
	var added []string
	for _, recipient := range withFiles.Recipients() {
		if !slices.Contains(plain.Recipients(), recipient) {
			added = append(added, recipient)
		}
	}
	if len(added) != 1 {
		t.Fatalf("want one distinct file commitment in signed recipients, got %v", added)
	}
	return withFiles, added[0]
}

func TestFileRefsSealAcceptanceCommitmentCannotBeAnAgentID(t *testing.T) {
	link, commitment := sealFilesLinked(t)
	if protocol.ValidateAgentID(commitment) == nil {
		t.Fatalf("file commitment can be substituted with a valid local agent recipient: %q", commitment)
	}
	if messaging.ValidAgent(commitment) {
		t.Fatalf("file commitment can be substituted with a valid peer agent recipient: %q", commitment)
	}
	header := signing.Header{Recipients: link.Recipients()}
	if !link.Binds(header) {
		t.Fatal("matching file commitment failed to bind")
	}
	sealFilesOn(t, &link, nil)
	link.To = append(slices.Clone(link.To), commitment)
	if link.Binds(header) {
		t.Fatal("moving the file commitment into To preserved the signature binding")
	}
}

func TestFileRefsSealAcceptanceCommitmentMatchesIndependentVector(t *testing.T) {
	_, commitment := sealFilesLinked(t)
	// Independently computed from the documented domain, count and ordered
	// length-prefixed path, uint64 size and raw SHA-256 bytes for these samples.
	const want = "files-sha512:6e1404dde32cc5a690f86f0a7706561bff30f78207427c25c4fa954a7b4854ae287f96bda57f3700777e9e67bc5db92477f50c278f6f07d554772766e2be11e8"
	if commitment != want {
		t.Fatalf("file commitment encoding changed: got %q, want %q", commitment, want)
	}
}

func TestFileRefsSealAcceptanceValidAgentNamesKeepTheirMeaning(t *testing.T) {
	for _, name := range []string{"files:" + strings.Repeat("a", 64), "files-sha512:valid-agent"} {
		if err := protocol.ValidateAgentID(name); err != nil {
			t.Fatalf("existing valid agent-name syntax was restricted: %v", err)
		}
		link := protocol.PeerLink{Receiver: sealReceiver, To: []string{name}}
		if !link.Binds(signing.Header{Recipients: link.Recipients()}) {
			t.Fatalf("valid agent name no longer binds as a recipient: %q", name)
		}
	}
}

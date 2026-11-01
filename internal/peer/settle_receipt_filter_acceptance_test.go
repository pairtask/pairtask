//go:build unix

package peer_test

import (
	"bytes"
	"crypto/sha256"
	"io"
	"testing"

	"github.com/pairtask/pairtask/internal/peer"
)

func TestSettleReceiptFilterAcceptancePassesExchangeAndNonreceiptFrames(t *testing.T) {
	digest := bytes.Repeat([]byte{7}, sha256.Size)
	digest[0], digest[1] = 1, peer.FrameReceipt
	hello, err := peer.EncodeInviterHello(peer.InviterHello{
		InvitationID: "receipt-filter-invitation",
		OperationID:  "receipt-filter-operation",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		frame []byte
	}{
		{"exchange_digest_with_receipt_header", digest},
		{"encoded_nonreceipt", hello},
		{"nil", nil},
		{"empty", []byte{}},
		{"one_byte", []byte{1}},
		{"receipt_header_only", []byte{1, peer.FrameReceipt}},
		{"short_digest_with_receipt_header", digest[:sha256.Size-1]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := bytes.Clone(tc.frame)
			got, err := dropRedeemReceipt(tc.frame)
			if err != nil || !bytes.Equal(got, before) {
				t.Fatalf("filter = (%x, %v), want (%x, nil)", got, err, before)
			}
			if !bytes.Equal(tc.frame, before) {
				t.Fatal("filter modified the input frame")
			}
		})
	}
}

func TestSettleReceiptFilterAcceptanceDropsEncodedReceipt(t *testing.T) {
	frame, err := peer.EncodeReceipt(peer.JoinReceipt{
		InvitationID:       "receipt-filter-invitation",
		InviterOperationID: "receipt-filter-operation",
		Joiner:             "receipt-filter-joiner",
		TranscriptID:       bytes.Repeat([]byte{7}, sha256.Size),
	})
	if err != nil {
		t.Fatal(err)
	}
	before := bytes.Clone(frame)
	got, err := dropRedeemReceipt(frame)
	if got != nil || err != io.ErrUnexpectedEOF {
		t.Fatalf("filter = (%x, %v), want (nil, io.ErrUnexpectedEOF)", got, err)
	}
	if !bytes.Equal(frame, before) {
		t.Fatal("filter modified the receipt frame")
	}
}

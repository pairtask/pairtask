package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

const upgradeNoticeMarker = "pairtask notice: signing-upgrade"

func upgradeNotices(t *testing.T, f *operatorContactFixture) []messaging.Row {
	t.Helper()
	var notices []messaging.Row
	for _, row := range messagePage(t, f.db.PeerMessages(), f.contact) {
		if row.Direction == messaging.DirOut && row.From == "pairtask" {
			notices = append(notices, row)
		}
	}
	return notices
}

func requireUpgradeNoticeCount(t *testing.T, f *operatorContactFixture, want int) []messaging.Row {
	t.Helper()
	got := upgradeNotices(t, f)
	if len(got) != want {
		t.Fatalf("automatic upgrade notices = %d, want %d: %+v", len(got), want, got)
	}
	return got
}

func requireUpgradeNoticeWire(t *testing.T, notice messaging.Row, wording string) {
	t.Helper()
	if notice.From != "pairtask" {
		t.Fatalf("notice is not from reserved pairtask sender: %+v", notice)
	}
	if len(notice.To) != 0 {
		t.Fatalf("notice does not go to the peer operator: %+v", notice)
	}
	if notice.ReplyTo.ID != "" {
		t.Fatalf("notice unexpectedly answers a message: %+v", notice)
	}
	if len(notice.Seal) != 0 {
		t.Fatalf("notice must be an unsigned pairtask message to their operator: %+v", notice)
	}
	for _, part := range []string{upgradeNoticeMarker, wording, "Signed messages let each side check who really sent what.",
		"update pairtask", "pairtask identity init", "docs/pairing.md#signed-messages", "Sent automatically by pairtask, not typed by"} {
		if !strings.Contains(notice.Text, part) {
			t.Fatalf("upgrade notice missing %q: %q", part, notice.Text)
		}
	}
	if !strings.HasPrefix(notice.Text, upgradeNoticeMarker+"\n") {
		t.Fatalf("older chat cannot recognise the notice's first line: %q", notice.Text)
	}
}

func TestSigningUpgradeNoticeAcceptanceOncePerDetectedStateAcrossRestart(t *testing.T) {
	f := newOperatorContactFixture(t)
	ctx := context.Background()
	bound := pairingVersionBound(t, f)
	pairOK(t, bound.SawVersion(ctx, messaging.PairingVersion))
	notices := requireUpgradeNoticeCount(t, f, 1)
	requireUpgradeNoticeWire(t, notices[0], "your pairtask version can't sign them")
	pairOK(t, bound.SawVersion(ctx, messaging.PairingVersion))
	requireUpgradeNoticeCount(t, f, 1)

	pairOK(t, f.db.Close())
	f.db = pairSQLite(t, f.path)
	pairOK(t, pairingVersionBound(t, f).SawVersion(ctx, messaging.PairingVersion))
	requireUpgradeNoticeCount(t, f, 1)
}

func TestSigningUpgradeNoticeAcceptanceUpgradeThenDowngradeNotifiesAgain(t *testing.T) {
	f := newOperatorContactFixture(t)
	ctx := context.Background()
	bound := pairingVersionBound(t, f)
	pairOK(t, bound.SawVersion(ctx, messaging.PairingVersion))
	requireUpgradeNoticeCount(t, f, 1)
	pairOK(t, bound.SawVersion(ctx, messaging.SealVersion))
	requireUpgradeNoticeCount(t, f, 1)
	pairOK(t, bound.SawVersion(ctx, messaging.PairingVersion))
	notices := requireUpgradeNoticeCount(t, f, 2)
	requireUpgradeNoticeWire(t, notices[0], "went back to a pairtask version")
	pairOK(t, bound.SawVersion(ctx, messaging.PairingVersion))
	requireUpgradeNoticeCount(t, f, 2)
}

func TestSigningUpgradeNoticeAcceptanceQueueFailureCanRetryWithoutLosingDetection(t *testing.T) {
	f := newOperatorContactFixture(t)
	ctx := context.Background()
	bound := pairingVersionBound(t, f)
	_, err := f.raw.Exec(`CREATE TRIGGER reject_system_notice BEFORE INSERT ON peer_messages
WHEN NEW.from_agent='pairtask' BEGIN SELECT RAISE(ABORT, 'test notice failure'); END`)
	pairOK(t, err)
	if err := bound.SawVersion(ctx, messaging.PairingVersion); err == nil {
		t.Fatal("a failed notice queue silently consumed the legacy detection")
	}
	var version int
	pairOK(t, f.raw.QueryRow(`SELECT COALESCE(peer_version, 0) FROM peer_contacts WHERE identity_key=?`, f.contact).Scan(&version))
	pairEqual(t, version, 0)
	requireUpgradeNoticeCount(t, f, 0)
	_, err = f.raw.Exec(`DROP TRIGGER reject_system_notice`)
	pairOK(t, err)
	pairOK(t, bound.SawVersion(ctx, messaging.PairingVersion))
	requireUpgradeNoticeCount(t, f, 1)
}

func TestSigningUpgradeNoticeAcceptanceNoNoticeWithoutActiveLegacyPeer(t *testing.T) {
	for _, phase := range []string{"current", "paused", "unpaired", "root"} {
		t.Run(phase, func(t *testing.T) {
			f := newOperatorContactFixture(t)
			ctx := context.Background()
			bound := pairingVersionBound(t, f)
			switch phase {
			case "current":
				pairOK(t, bound.SawVersion(ctx, messaging.SealVersion))
			case "paused":
				_, err := f.raw.Exec(`UPDATE peer_contacts SET paused_at='2026-11-16T00:00:00Z' WHERE identity_key=?`, f.contact)
				pairOK(t, err)
				if err := bound.SawVersion(ctx, messaging.PairingVersion); !errors.Is(err, store.ErrFenced) {
					t.Fatalf("paused session changed signing state: %v", err)
				}
			case "unpaired":
				_, err := f.raw.Exec(`UPDATE peer_contacts SET unpaired_at='2026-11-16T00:00:00Z' WHERE identity_key=?`, f.contact)
				pairOK(t, err)
				if err := bound.SawVersion(ctx, messaging.PairingVersion); !errors.Is(err, store.ErrFenced) {
					t.Fatalf("unpaired session changed signing state: %v", err)
				}
			case "root":
				pairOK(t, f.db.PeerMessages().SawVersion(ctx, messaging.PairingVersion))
			}
			requireUpgradeNoticeCount(t, f, 0)
		})
	}
}

func TestSigningUpgradeNoticeAcceptanceReservedSenderCannotBeClaimedOrAddressed(t *testing.T) {
	if err := protocol.ValidateAgentID("pairtask"); err == nil {
		t.Fatal("a local agent can register as the system-notice sender")
	}
	if messaging.ValidAgent("pairtask") {
		t.Fatal("a peer can address the system-notice sender as an agent")
	}
	f := newOperatorContactFixture(t)
	ctx := context.Background()
	if _, err := f.db.PeerMessages().Share(ctx, f.contact, []string{"pairtask"}, messageAt); err == nil {
		t.Fatal("the system-notice sender can be granted to a contact")
	}
	if _, err := f.db.PeerMessages().Queue(ctx, messaging.Outgoing{Contact: f.contact, ID: "forged-notice",
		From: "pairtask", Text: upgradeNoticeMarker, Now: messageAt}); err == nil {
		t.Fatal("the public queue path can forge a system notice")
	}
	inbound := messageWire("to-system", 1)
	inbound.To = []string{"pairtask"}
	answer, err := f.db.PeerMessages().AcceptVersion(ctx, f.contact,
		messaging.Arrival{Version: int(messaging.RoutingVersion), Message: inbound, At: messageAt})
	pairOK(t, err)
	if answer.Stored {
		t.Fatal("an inbound message addressed to the reserved sender was stored")
	}
	requireUpgradeNoticeCount(t, f, 0)
}

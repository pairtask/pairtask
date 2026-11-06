package hub

import (
	"slices"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

// Pairing history (#276): a contact's messages and this side's review
// requests are kept from every pairing with it. A client at
// PairingHistoryVersion reads them all, each tagged with its pairing, and
// may name an earlier pairing to read or acknowledge its messages or read
// its requests. An older one reads the newest pairing alone, untagged, and
// naming any other is refused before anything is audited or changed. An
// operator session is served at the version it opened at; a one-shot
// request at the one it names.

// historyCapable: a client at version reads every pairing's history.
func historyCapable(version int) bool { return version >= protocol.PairingHistoryVersion }

// errHistoryVersion refuses a pairing named by a client below
// PairingHistoryVersion.
var errHistoryVersion = &routingError{protocol.ErrMalformedFrame, "naming a pairing requires protocol_version 25"}

// contactEpochRefused refuses a contact request naming a pairing from a
// client that can't.
func contactEpochRefused(msg protocol.Message) *routingError {
	if msg.ContactEpoch == 0 || historyCapable(msg.ProtocolVersion) {
		return nil
	}
	return errHistoryVersion
}

// refsEpochRefused refuses refs naming a pairing from a client at version
// that can't.
func refsEpochRefused(refs []protocol.ReviewAskRef, version int) *routingError {
	if historyCapable(version) || !slices.ContainsFunc(refs, namesPairing) {
		return nil
	}
	return errHistoryVersion
}

// namesPairing: the ref names a pairing, not the contact's current one.
func namesPairing(ref protocol.ReviewAskRef) bool { return ref.PairingEpoch != 0 }

// historyKey names a stored message in the pairing it came under, for
// what else this side knows of it.
func historyKey(r messaging.Row) store.MessageKey {
	return store.MessageKey{Contact: r.Contact, Epoch: r.PairingEpoch, ID: r.ID}
}

// untaggedMessage is a message view as an older client reads it: no
// pairing on it or on its request's outcome.
func untaggedMessage(view *protocol.ContactMessageView) {
	view.PairingEpoch = 0
	if view.ReviewOutcome != nil {
		outcome := *view.ReviewOutcome
		outcome.PairingEpoch = 0
		view.ReviewOutcome = &outcome
	}
}

// versionedView is a message view as a client at version reads it.
func versionedView(view protocol.ContactMessageView, version int) protocol.ContactMessageView {
	if !historyCapable(version) {
		untaggedMessage(&view)
	}
	return view
}

// untaggedOutcomes are requests' outcomes as an older client reads them.
func untaggedOutcomes(views []protocol.ReviewOutcomeView) {
	for i := range views {
		views[i].PairingEpoch = 0
	}
}

// untaggedReviews are an operator review page's items as an older client
// reads them.
func untaggedReviews(out *protocol.Message) {
	if out.ReviewAsked != nil {
		for i := range out.ReviewAsked.Items {
			out.ReviewAsked.Items[i].PairingEpoch = 0
		}
	}
	if out.ReviewFailed != nil {
		for i := range out.ReviewFailed.Items {
			out.ReviewFailed.Items[i].PairingEpoch = 0
		}
	}
}

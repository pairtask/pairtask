package hub

import "github.com/pairtask/pairtask/internal/protocol"

// Replacing a review request (#259): what the broker admits of
// review_ask.supersedes before the store links it.

// checkSupersedes admits a request an agent's review request replaces from
// v26 (#259).
func checkSupersedes(supersedes string, version int) *routingError {
	if supersedes != "" && version < protocol.ReviewSupersedeVersion {
		return &routingError{protocol.ErrMalformedFrame, "review_ask.supersedes requires protocol_version 26"}
	}
	return supersedesID(supersedes)
}

// supersedesID: the request replaced is named by an ID as a request's own.
func supersedesID(supersedes string) *routingError {
	if len(supersedes) > 128 {
		return &routingError{protocol.ErrMalformedFrame, "review_ask.supersedes names a request's ID, at most 128 bytes"}
	}
	return nil
}

// askSupersedes is the request a review request replaces (#259), "" for
// none or for any other message.
func askSupersedes(ask *protocol.ReviewAsk) string {
	if ask == nil {
		return ""
	}
	return ask.Supersedes
}

package chat

// What became of a request the other workspace held for its operator
// (#293), as one line on the request's own row or Asked item: held while
// it waits, and once it ended, whether it ran there and why not. The
// review's own outcomes keep their lines; this one is the hold's.

// consentWithheldWords say why a held request was withheld, by its reason.
var consentWithheldWords = map[string]string{
	"session_ended": "their session ended",
	"revoked":       "they revoked it",
	"paused":        "they paused this pairing",
}

// consentSummary is a held request's status line, "" for one never held.
func consentSummary(state, reason string) string {
	switch state {
	case "":
		return ""
	case "held":
		return "held for their operator"
	case "allowed":
		return "allowed by their operator"
	}
	return "not run: " + consentEndWords(state, reason) + consentReasonNote(reason)
}

// consentEndWords say how a hold ended without the request running.
func consentEndWords(state, reason string) string {
	switch state {
	case "declined":
		return "declined by their operator"
	case "expired":
		return "their operator never decided"
	case "withheld":
		if words := consentWithheldWords[reason]; words != "" {
			return words
		}
		return "withheld there"
	}
	return safe(state) + " there"
}

// consentReasonNote is the reason as given, for the record.
func consentReasonNote(reason string) string {
	if reason == "" {
		return ""
	}
	return " (" + safe(reason) + ")"
}

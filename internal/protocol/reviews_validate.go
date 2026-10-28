package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"unicode"
	"unicode/utf8"
)

var (
	forgeName  = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)
	repository = regexp.MustCompile(`^[A-Za-z0-9.-]+(/[!-.0-~]+)+$`) // host, then path segments of printable ASCII
	changeRef  = regexp.MustCompile(`^[!-~]+$`)                      // opaque: printable ASCII, no spaces
	revision   = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)
	reasonKey  = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)
)

// reviewOutcomes are the forge-neutral verdicts.
var reviewOutcomes = []string{OutcomeApprove, OutcomeChangesRequested, OutcomeComment}

// boundedText is UTF-8 without control characters, at most limit bytes;
// newlines and tabs are allowed in long text.
func boundedText(text string, limit int, multiline bool) error {
	if len(text) > limit {
		return fmt.Errorf("at most %d bytes", limit)
	}
	if !utf8.ValidString(text) {
		return errors.New("not valid UTF-8")
	}
	for _, r := range text {
		if unicode.IsControl(r) && !(multiline && (r == '\n' || r == '\t')) {
			return errors.New("control characters are not allowed")
		}
	}
	return nil
}

// ValidateReviewID checks a review ID: an operator message ID.
func ValidateReviewID(id string) error {
	if id == "" || len(id) > 128 {
		return errors.New("review_id is required, at most 128 bytes")
	}
	return boundedText(id, 128, false)
}

// ValidateReviewTarget checks a forge, repository and change.
func ValidateReviewTarget(t ReviewTarget) error {
	switch {
	case !forgeName.MatchString(t.Forge):
		return errors.New("forge must be 1 to 32 of a-z, 0-9 and -")
	case len(t.Repository) > ReviewRepositoryBytes || !repository.MatchString(t.Repository):
		return fmt.Errorf("repository must be host/owner/name, at most %d bytes", ReviewRepositoryBytes)
	case len(t.Change) > ReviewChangeBytes || !changeRef.MatchString(t.Change):
		return fmt.Errorf("change must be 1 to %d printable ASCII characters without spaces", ReviewChangeBytes)
	}
	return nil
}

// ValidateReviewSpec checks an operator's request.
func ValidateReviewSpec(s ReviewSpec) error {
	if err := ValidateReviewTarget(s.ReviewTarget); err != nil {
		return err
	}
	if err := ValidateAgentID(s.Reviewer); err != nil {
		return fmt.Errorf("reviewer: %w", err)
	}
	if err := boundedText(s.Scope, ReviewScopeBytes, true); err != nil {
		return fmt.Errorf("scope: %w", err)
	}
	return nil
}

// ValidateRevisions checks a pinned base and head: lowercase hex commit IDs.
func ValidateRevisions(r *Revisions) error {
	if r == nil || !revision.MatchString(r.Base) || !revision.MatchString(r.Head) {
		return errors.New("revisions need full commit IDs for base and head: 40 or 64 lowercase hex digits")
	}
	return nil
}

// ValidateVerdict checks an outcome, its summary and its findings.
func ValidateVerdict(v *Verdict) error {
	if v == nil || !oneOf(v.Outcome, reviewOutcomes) {
		return errors.New("verdict outcome must be approve, changes_requested or comment")
	}
	if err := boundedText(v.Summary, ReviewSummaryBytes, true); err != nil {
		return fmt.Errorf("summary: %w", err)
	}
	if len(v.Findings) > ReviewFindings {
		return fmt.Errorf("at most %d findings", ReviewFindings)
	}
	for i, f := range v.Findings {
		if err := validFinding(f); err != nil {
			return fmt.Errorf("finding %d: %w", i+1, err)
		}
	}
	// Escaping can multiply raw text; bound what is stored and sent.
	if encoded, err := json.Marshal(v); err != nil || len(encoded) > ReviewVerdictBytes {
		return fmt.Errorf("verdict: at most %d bytes as encoded JSON", ReviewVerdictBytes)
	}
	return nil
}

func validFinding(f Finding) error {
	if f.Path == "" {
		return errors.New("path is required")
	}
	if err := boundedText(f.Path, ReviewPathBytes, false); err != nil {
		return fmt.Errorf("path: %w", err)
	}
	if f.Line < 0 {
		return errors.New("line must not be negative")
	}
	if f.Summary == "" {
		return errors.New("summary is required")
	}
	return boundedText(f.Summary, ReviewFindingBytes, true)
}

// ValidateReviewReason checks why a review was declined or cancelled:
// short prose for people.
func ValidateReviewReason(reason string) error {
	if err := boundedText(reason, ReviewReasonBytes, false); err != nil {
		return fmt.Errorf("review_reason: %w", err)
	}
	return nil
}

// publicationStates are every publication state; which step is allowed now
// is the broker's to decide.
var publicationStates = []string{PublicationNone, PublicationPending, PublicationSubmitting, PublicationPublished,
	PublicationFailed, PublicationUncertain, PublicationStale, PublicationCancelled}

// ValidatePublicationUpdate checks one publication step's shape; whether
// the step is allowed now is the broker's to decide.
func ValidatePublicationUpdate(u *PublicationUpdate) error {
	if u == nil {
		return errors.New("publication is required")
	}
	if u.Reason != "" && !reasonKey.MatchString(u.Reason) {
		return errors.New("publication reason must be 1 to 64 of a-z, 0-9 and _")
	}
	if !oneOf(u.From, publicationStates) || !oneOf(u.To, publicationStates) {
		return errors.New("publication from and to must be publication states")
	}
	switch u.To {
	case PublicationSubmitting:
		if u.Token == "" || len(u.Token) > 128 {
			return errors.New("a step to submitting needs a token of at most 128 bytes")
		}
	case PublicationPublished:
		return validReceipt(u.Receipt)
	case PublicationFailed, PublicationUncertain:
		if u.Reason == "" {
			return errors.New("a failed or uncertain step needs a reason")
		}
	case PublicationStale:
		return ValidateRevisions(u.Observed)
	}
	return nil
}

func validReceipt(r *PublicationReceipt) error {
	if r == nil || r.ReviewID == "" || r.Identity == "" || !revision.MatchString(r.Revision) {
		return errors.New("published needs a receipt with review_id, identity and the recorded revision")
	}
	for _, field := range []string{r.ReviewID, r.Identity, r.URL} {
		if err := boundedText(field, ReviewFindingBytes, false); err != nil {
			return fmt.Errorf("receipt: %w", err)
		}
	}
	return nil
}

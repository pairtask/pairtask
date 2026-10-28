package github

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/pairtask/pairtask/internal/forge"
	"github.com/pairtask/pairtask/internal/protocol"
)

// githubEvents are the review events GitHub takes, by forge-neutral event.
var githubEvents = map[string]string{
	forge.EventApprove:        "APPROVE",
	forge.EventRequestChanges: "REQUEST_CHANGES",
	forge.EventComment:        "COMMENT",
}

// recordedStates are the states of a submitted review and what they record.
// Anything else (PENDING, DISMISSED, unknown) is not a published review.
var recordedStates = map[string]string{
	"APPROVED":          forge.EventApprove,
	"CHANGES_REQUESTED": forge.EventRequestChanges,
	"COMMENTED":         forge.EventComment,
}

// review is one review as GitHub answers it.
type review struct {
	ID       int64  `json:"id"`
	State    string `json:"state"`
	Body     string `json:"body"`
	CommitID string `json:"commit_id"`
	HTMLURL  string `json:"html_url"`
	User     struct {
		Login string `json:"login"`
	} `json:"user"`
}

// receipt is a submitted review as forge-neutral facts; ok is false for
// anything that isn't a complete, submitted review.
func (r review) receipt() (forge.Receipt, bool) {
	event, ok := recordedStates[r.State]
	if !ok || !r.complete() {
		return forge.Receipt{}, false
	}
	out := forge.Receipt{Event: event}
	out.ReviewID, out.URL, out.Identity, out.Revision = strconv.FormatInt(r.ID, 10), r.HTMLURL, r.User.Login, r.CommitID
	out.Formal = event != forge.EventComment
	return out, true
}

// complete: an ID, an account and the full commit it was recorded on.
func (r review) complete() bool {
	if r.ID <= 0 || r.User.Login == "" {
		return false
	}
	return fullRevision(r.CommitID)
}

type reviewComment struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Side string `json:"side"`
	Body string `json:"body"`
}

type reviewRequest struct {
	CommitID string          `json:"commit_id"`
	Event    string          `json:"event"`
	Body     string          `json:"body"`
	Comments []reviewComment `json:"comments,omitempty"`
}

// Publish submits one review on the pinned head, as exactly the event it
// is given. Once the request was sent, anything short of a complete
// submitted review in the answer leaves the outcome uncertain.
func (a *Adapter) Publish(ctx context.Context, s forge.Submission) (forge.Receipt, error) {
	path, err := a.pullPath(s.Target, "/reviews")
	if err != nil {
		return forge.Receipt{}, err
	}
	event, ok := githubEvents[s.Event]
	if !ok || !fullRevision(s.Pinned.Head) {
		return forge.Receipt{}, forge.Errorf(forge.ClassInvalid, "nothing to publish as %q on that head", s.Event)
	}
	body := reviewRequest{CommitID: s.Pinned.Head, Event: event, Body: reviewBody(s), Comments: lineComments(s.Findings)}
	var answer review
	if _, err := a.do(ctx, call{method: http.MethodPost, path: path, body: body}, &answer); err != nil {
		return forge.Receipt{}, err
	}
	receipt, ok := answer.receipt()
	if !ok {
		return forge.Receipt{}, forge.Errorf(forge.ClassUncertain, "GitHub's answer is not a complete submitted review")
	}
	return receipt, nil
}

// reviewBody is the summary, every finding with its place, and the marker
// on the last line.
func reviewBody(s forge.Submission) string {
	var b strings.Builder
	b.WriteString(s.Summary)
	if len(s.Findings) > 0 {
		b.WriteString("\n\nFindings:\n")
		for _, f := range s.Findings {
			b.WriteString("- " + f.Path + ":" + strconv.Itoa(f.Line) + " " + f.Summary + "\n")
		}
	}
	b.WriteString("\n" + s.Marker)
	return b.String()
}

// lineComments are the findings that name a line, on the new side.
func lineComments(findings []protocol.Finding) []reviewComment {
	var out []reviewComment
	for _, f := range findings {
		if f.Line > 0 {
			out = append(out, reviewComment{Path: f.Path, Line: f.Line, Side: "RIGHT", Body: f.Summary})
		}
	}
	return out
}

// Find looks for the one review carrying the marker with every expected
// fact. Every page is read; the same review twice counts once, two distinct
// ones are ambiguous, and a list longer than the pages read proves nothing.
func (a *Adapter) Find(ctx context.Context, t forge.Target, e forge.Expected) (forge.Receipt, bool, error) {
	path, err := a.pullPath(t, "/reviews")
	if err != nil {
		return forge.Receipt{}, false, err
	}
	matches, err := a.marked(ctx, path, e.Marker)
	if err != nil {
		return forge.Receipt{}, false, err
	}
	switch len(matches) {
	case 0:
		return forge.Receipt{}, false, nil
	case 1:
	default:
		return forge.Receipt{}, false, forge.Errorf(forge.ClassAmbiguous, "%d reviews carry the marker", len(matches))
	}
	for _, r := range matches {
		receipt, ok := r.receipt()
		if !ok || !forge.Matches(receipt, e) {
			return forge.Receipt{}, false, forge.Errorf(forge.ClassAmbiguous, "a review carries the marker but not the expected account, head or event")
		}
		return receipt, true, nil
	}
	return forge.Receipt{}, false, nil
}

// collectMarked adds the reviews carrying marker, by review ID, so the same
// review seen twice counts once.
func collectMarked(matches map[int64]review, reviews []review, marker string) {
	if marker == "" {
		return
	}
	for _, r := range reviews {
		if strings.Contains(r.Body, marker) {
			matches[r.ID] = r
		}
	}
}

// marked is every review on the pull request whose body carries marker, by
// review ID.
func (a *Adapter) marked(ctx context.Context, path, marker string) (map[int64]review, error) {
	matches := map[int64]review{}
	for page := 1; page <= a.cfg.MaxPages; page++ {
		var reviews []review
		query := url.Values{"per_page": {"100"}, "page": {strconv.Itoa(page)}}
		more, err := a.getPage(ctx, path, query, &reviews)
		if err != nil {
			return nil, err
		}
		collectMarked(matches, reviews, marker)
		if !more {
			return matches, nil
		}
	}
	return nil, forge.Errorf(forge.ClassUnavailable, "more reviews than %d pages; can't tell they were all seen", a.cfg.MaxPages)
}

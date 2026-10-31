package chat

import (
	"fmt"
	"slices"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/pairtask/pairtask/internal/protocol"
)

// /pair status in the order an operator acts on it: the paired workspaces
// and how each stands, what waits for this side, what waits for the other
// side, the invitation, this workspace, then what is settled.

// standingView is a paired workspace's standing: a dot, and the words that
// say it, so color is never the only signal.
type standingView struct {
	dot   string
	style lipgloss.Style
	words string
}

// row is the colored dot, the workspace's name, then the words.
func (s standingView) row(name string) string {
	return "  " + s.style.Render(s.dot) + " " + name + "  " + s.words
}

// standing is how a paired workspace stands, from what the broker observed:
// a missing summary is unknown, and no live link alone is never offline.
func standing(c protocol.PairingContactView) standingView {
	if c.Summary == nil {
		return standingView{"○", mutedStyle, "unknown (this broker reports no status)"}
	}
	sync := c.Summary.Sync
	switch {
	case sync.Live:
		return standingView{"●", doneStyle, "connected"}
	case !c.Confirmed:
		return standingView{"●", queueStyle, "pending: not confirmed on both sides yet"}
	case sync.LastFailure != "" || sync.Failures > 0:
		return standingView{"●", errStyle, failedWords(sync) + movedHint(c)}
	case sync.LastSuccess != nil:
		return standingView{"○", doneStyle, "idle · last synced " + when(*sync.LastSuccess)}
	}
	return standingView{"○", mutedStyle, "not yet observed"}
}

// movedHint points the side that dials a failing workspace at /pair
// address, in case it came back somewhere else.
func movedHint(c protocol.PairingContactView) string {
	if !c.Dials {
		return ""
	}
	return " · moved? /pair address HOST:PORT in its ⇄ tab"
}

// failedWords say when the last attempt failed and why, and the last
// success, when there was one.
func failedWords(s protocol.ContactSyncView) string {
	words := "last attempt failed"
	if s.LastAttempt != nil {
		words += " " + when(*s.LastAttempt)
	}
	if s.LastFailure != "" {
		words += ": " + safe(s.LastFailure)
	}
	if s.Failures > 1 {
		words += fmt.Sprintf(" (%d in a row)", s.Failures)
	}
	if s.LastSuccess != nil {
		words += " · last synced " + when(*s.LastSuccess)
	}
	return words
}

// refreshRows say that the last read failed and which read is shown.
func (m *Model) refreshRows() []string {
	if m.pairing.refreshErr == "" {
		return nil
	}
	return []string{errStyle.Render("  couldn't refresh: "+safe(m.pairing.refreshErr)) +
		mutedStyle.Render(" · showing the state read "+when(m.pairing.at))}
}

// waitingForYou are the pairing requests to decide and, per listed
// workspace, its review requests waiting for approval in its ⇄ tab.
func (m *Model) waitingForYou() []string {
	s := m.pairing.snapshot
	if s == nil {
		return nil
	}
	rows := m.waitRows(forYou)
	if s.RequestsTruncated {
		rows = append(rows, fmt.Sprintf("  (requests list is partial: %d more not shown; pending requests list first, so deciding these brings the rest into view)", s.RequestsTotal-len(s.Requests)))
	}
	rows = append(rows, reviewsWaiting(s)...)
	return section("Waiting for you", rows)
}

// reviewsWaiting are the listed workspaces with review requests waiting
// for this side; the counts cover only the listed ones.
func reviewsWaiting(s *protocol.PairingSnapshot) []string {
	var rows []string
	reported := false
	for _, c := range s.Contacts {
		if c.Summary == nil {
			continue
		}
		reported = true
		if n := c.Summary.ReviewsAwaiting; n > 0 {
			rows = append(rows, fmt.Sprintf("  ⇄ %s: %s · open its ⇄ tab (Tab) to approve or decline", safe(contactDisplay(c)), plural(n, "review request")))
		}
	}
	if reported && s.ContactsTruncated {
		rows = append(rows, mutedStyle.Render("  (review counts cover the listed contacts only; the contact list is partial)"))
	}
	return rows
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// waiting is who a listed request or redemption waits for, if anyone.
type waiting int

const (
	forYou waiting = iota
	forThem
	settled
)

// requestWaits: a pending request waits for this side; an acceptance not
// yet confirmed, for the other side.
func requestWaits(r protocol.PairingRequestView) waiting {
	switch {
	case r.State == pendingAcceptance:
		return forYou
	case r.State == "accepted" && r.DeliveredAt == nil:
		return forThem
	}
	return settled
}

// settledJoins are redemptions that need nothing more from either side.
var settledJoins = []string{"failed", "declined", "contact"}

// joinWaits: a redemption still in progress waits for the other side.
func joinWaits(j protocol.PairingJoinView) waiting {
	if slices.Contains(settledJoins, j.State) {
		return settled
	}
	return forThem
}

// waitRows are the listed requests, then the redemptions, that wait as w.
func (m *Model) waitRows(w waiting) []string {
	return append(m.requestsWaiting(w), m.joinsWaiting(w)...)
}

func (m *Model) requestsWaiting(w waiting) []string {
	var rows []string
	for _, r := range m.pairing.snapshot.Requests {
		if requestWaits(r) == w {
			rows = append(rows, m.requestRow(r)...)
		}
	}
	return rows
}

func (m *Model) joinsWaiting(w waiting) []string {
	var rows []string
	for _, j := range m.pairing.snapshot.Joins {
		if joinWaits(j) == w {
			rows = append(rows, m.joinRow(j))
		}
	}
	return rows
}

// waitingForThem are this side's acceptances not yet confirmed and its
// redemptions still in progress.
func (m *Model) waitingForThem() []string {
	if m.pairing.snapshot == nil {
		return nil
	}
	return section("Waiting for them", m.waitRows(forThem))
}

// settledRows are the decided requests and the settled redemptions.
func (m *Model) settledRows() []string {
	s := m.pairing.snapshot
	if s == nil {
		return nil
	}
	rows := m.waitRows(settled)
	if s.JoinsTruncated {
		rows = append(rows, fmt.Sprintf("  (redemptions list is partial: %d more not shown)", s.JoinsTotal-len(s.Joins)))
	}
	return section("History", rows)
}

// section is rows under a heading, or nothing without rows.
func section(title string, rows []string) []string {
	if len(rows) == 0 {
		return nil
	}
	return append([]string{"", titleStyle.Render("  " + title)}, rows...)
}

// keepSnapshot is a successful read: shown from now on, with when it was
// read, and any earlier failure to refresh forgotten.
func (m *Model) keepSnapshot(snapshot *protocol.PairingSnapshot) {
	m.pairing.snapshot, m.pairing.at, m.pairing.refreshErr = snapshot, time.Now(), ""
}

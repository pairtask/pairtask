package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
)

// Admission runs in Accept's transaction, where today's answer would be
// not_shared: the contact opted in and is active, every recipient is a
// local agent, no scope is denied and one is undecided; every later check
// passes; the count, byte and arrival bounds hold. Only then is the
// request held, and its rate token spent. Anything less changes nothing.

// consentVersion: a session that may answer Held; 0 is the newest.
func consentVersion(version int) bool {
	return version == 0 || version >= messaging.ConsentPeerVersion
}

// consentChecks are the inbound checks a held request still passes: all
// but the grant check that holding replaces.
var consentChecks = slices.DeleteFunc(slices.Clone(inboundChecks), func(c inboundCheck) bool {
	return c.reason == messaging.ReasonNotShared
})

// holdOrRefuse holds a request refused only as not shared when its
// contact may be asked, else refuses it as before.
func holdOrRefuse(ctx context.Context, tx *sql.Tx, r newRow, answer messaging.Answer) (messaging.Answer, bool, error) {
	targets, ok, err := heldTargets(ctx, tx, r)
	if err != nil {
		return messaging.Answer{}, false, err
	}
	if !ok {
		return refuseNotShared(ctx, tx, r, answer)
	}
	if answer, err := runChecks(ctx, tx, consentChecks, r); !answer.Stored || err != nil {
		return answer, false, err
	}
	return holdRequest(ctx, tx, heldRow{r, targets})
}

// refuseNotShared is today's not_shared answer: recorded for a review
// request, else refused.
func refuseNotShared(ctx context.Context, tx *sql.Tx, r newRow, answer messaging.Answer) (messaging.Answer, bool, error) {
	if recordable(r.m, answer) {
		return recordRefusal(ctx, tx, r)
	}
	return answer, false, nil
}

// runChecks is the first refusal of checks, or stored.
func runChecks(ctx context.Context, tx *sql.Tx, checks []inboundCheck, r newRow) (messaging.Answer, error) {
	for _, check := range checks {
		if refuses, err := check.refuses(ctx, tx, r.key.contact, r.m); refuses || err != nil {
			return refused(check.reason), err
		}
	}
	return stored, nil
}

// heldTargets are the request's targets when it may be held: the contact
// opted in and active, its recipients local agents, and its scopes
// holdable.
func heldTargets(ctx context.Context, tx *sql.Tx, r newRow) ([]protocol.ConsentTarget, bool, error) {
	ok, err := consentOpen(ctx, tx, r.key.contact)
	if err == nil && ok {
		ok, err = localAgents(ctx, tx, r.m.To)
	}
	if err != nil || !ok {
		return nil, false, err
	}
	targets := requestTargets(r.m)
	states, err := scopeStates(ctx, tx, targetScopes(r.key.contact, targets), r.m.Review)
	return targets, err == nil && holdable(states), err
}

// consentOpen: the contact opted in and isn't paused.
func consentOpen(ctx context.Context, tx *sql.Tx, contact string) (bool, error) {
	var open bool
	err := tx.QueryRowContext(ctx, `SELECT consent_since IS NOT NULL AND paused_at = '' FROM peer_contacts WHERE identity_key = ?`,
		contact).Scan(&open)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return open, err
}

// localAgents: every agent is one this side knows.
func localAgents(ctx context.Context, tx *sql.Tx, agents []string) (bool, error) {
	for _, agent := range agents {
		var known bool
		err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM durable_agents WHERE agent_id = ? AND agent_id != 'operator')`, agent).Scan(&known)
		if err != nil || !known {
			return false, err
		}
	}
	return len(agents) > 0, nil
}

// heldRow is a request about to be held, with its canonical targets.
type heldRow struct {
	r       newRow
	targets []protocol.ConsentTarget
}

// size is what a held request charges: its text and its review request,
// in bytes as stored.
func (h heldRow) size() int {
	return len(h.r.m.Text) + len(reviewAskJSON(h.r.m.Review).String)
}

// holdRequest charges the bounds and the rate, and holds the request.
func holdRequest(ctx context.Context, tx *sql.Tx, h heldRow) (messaging.Answer, bool, error) {
	room, err := consentRoom(ctx, tx, h)
	if err != nil || !room {
		return refused(messaging.ReasonInboxFull), false, err
	}
	token, err := takeConsentToken(ctx, tx, h.r.key.contact, h.r.now)
	if err != nil || !token {
		return refused(messaging.ReasonBusy), false, err
	}
	id := consentID(h.r.key.contact, h.r.m.ID)
	if err := insertConsent(ctx, tx, h, id); err != nil {
		return messaging.Answer{}, false, err
	}
	return heldAnswer(id), true, nil
}

func heldAnswer(id string) messaging.Answer {
	return messaging.Answer{Stored: true, Held: true, HeldID: id}
}

// heldNow are the requests waiting for this side's operator: held ones,
// and review requests staged and not yet dispatched or decided.
const heldNow = `WITH held AS (
    SELECT contact, recipients, bytes FROM consent_requests WHERE state = 'pending'
    UNION ALL
    SELECT ri.contact, ri.recipients, COALESCE(pm.text_bytes, 0) + COALESCE(LENGTH(CAST(pm.review_ask AS BLOB)), 0)
    FROM remote_imports ri LEFT JOIN peer_messages pm ON pm.contact = ri.contact AND pm.direction = 'in' AND pm.message_id = ri.remote_id
    WHERE ri.kind = 'review' AND ri.state IN ('pending', 'awaiting'))
`

// heldUse is how much is held: for the contact, in all, and the bytes of each.
type heldUse struct {
	contact, total           int
	contactBytes, totalBytes int
}

// within: one more request of size fits the count and byte bounds.
func (u heldUse) within(size int) bool {
	if u.contact >= ConsentPerContact || u.total >= ConsentTotal {
		return false
	}
	return u.contactBytes+size <= ConsentContactSize && u.totalBytes+size <= ConsentTotalSize
}

func readHeldUse(ctx context.Context, tx *sql.Tx, contact string) (heldUse, error) {
	var u heldUse
	err := tx.QueryRowContext(ctx, heldNow+`SELECT COALESCE(SUM(contact = ?1), 0), COUNT(*),
    COALESCE(SUM(CASE WHEN contact = ?1 THEN bytes END), 0), COALESCE(SUM(bytes), 0) FROM held`, contact).
		Scan(&u.contact, &u.total, &u.contactBytes, &u.totalBytes)
	return u, err
}

// heldForAgent is how many of the contact's held requests name agent.
func heldForAgent(ctx context.Context, tx *sql.Tx, contact, agent string) (int, error) {
	var n int
	err := tx.QueryRowContext(ctx, heldNow+`SELECT COUNT(*) FROM held
WHERE contact = ? AND EXISTS (SELECT 1 FROM json_each(held.recipients) j WHERE j.value = ?)`, contact, agent).Scan(&n)
	return n, err
}

// consentRoom: the request fits every bound, each of its agents included.
func consentRoom(ctx context.Context, tx *sql.Tx, h heldRow) (bool, error) {
	use, err := readHeldUse(ctx, tx, h.r.key.contact)
	if err != nil || !use.within(h.size()) {
		return false, err
	}
	for _, t := range h.targets {
		n, err := heldForAgent(ctx, tx, h.r.key.contact, t.Agent)
		if err != nil || n >= ConsentPerAgent {
			return false, err
		}
	}
	return true, nil
}

// takeConsentToken spends one of the contact's arrival tokens: ConsentBurst
// at most, one more each ConsentRefill.
func takeConsentToken(ctx context.Context, tx *sql.Tx, contact string, now time.Time) (bool, error) {
	b := rateBucket{tokens: ConsentBurst, at: now}
	var at string
	err := tx.QueryRowContext(ctx, `SELECT tokens, refreshed_at FROM consent_rate WHERE contact = ?`, contact).Scan(&b.tokens, &at)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return false, err
	default:
		if b.at, err = time.Parse(time.RFC3339Nano, at); err != nil {
			return false, err
		}
	}
	b = b.refilled(now)
	if b.tokens < 1 {
		return false, nil
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO consent_rate (contact, tokens, refreshed_at) VALUES (?, ?, ?)
ON CONFLICT (contact) DO UPDATE SET tokens = excluded.tokens, refreshed_at = excluded.refreshed_at`, contact, b.tokens-1, msgStamp(b.at))
	return true, err
}

// rateBucket is a contact's tokens as of a time.
type rateBucket struct {
	tokens int
	at     time.Time
}

// refilled is the bucket at now: a token for each whole ConsentRefill
// since, up to ConsentBurst; a full bucket starts counting again at now.
func (b rateBucket) refilled(now time.Time) rateBucket {
	if now.After(b.at) {
		earned := int(now.Sub(b.at) / ConsentRefill)
		b.tokens += earned
		b.at = b.at.Add(time.Duration(earned) * ConsentRefill)
	}
	if b.tokens >= ConsentBurst {
		b.tokens, b.at = ConsentBurst, laterOf(b.at, now)
	}
	return b
}

func laterOf(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// consentID is a held request's ID: derived from the contact and the
// message's ID, so the peer can't choose it.
func consentID(contact, messageID string) string {
	sum := sha256.Sum256([]byte("consent\x00" + contact + "\x00" + messageID))
	return "consent:" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:]))[:52]
}

// consentDigest is what a decision names: the authenticated contact, the
// request's identity and claimed sender, its canonical targets, its full
// text and its review request's pins and policy.
func consentDigest(contact string, m messaging.Message, targets []protocol.ConsentTarget) string {
	encoded, _ := json.Marshal(targets) // strings always encode
	return hex.EncodeToString(operationFingerprint("consent", contact, m.ID, m.From, string(encoded), m.Text, reviewAskJSON(m.Review).String))
}

func insertConsent(ctx context.Context, tx *sql.Tx, h heldRow, id string) error {
	r, m := h.r, h.r.m
	targets, _ := json.Marshal(h.targets) // strings always encode
	recipients, _ := json.Marshal(m.To)
	hash := m.Hash()
	_, err := tx.ExecContext(ctx, `INSERT INTO consent_requests
(id, contact, message_id, n, created_at, sender, recipients, reply_id, reply_author, text, review_ask, hash,
    targets, digest, bytes, state, revision, received_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', 1, ?, ?)`,
		id, r.key.contact, m.ID, int64(m.N), msgStamp(m.CreatedAt), m.From, string(recipients), m.ReplyTo.ID, int(m.ReplyTo.Author),
		m.Text, reviewAskJSON(m.Review), hash[:], string(targets), consentDigest(r.key.contact, m, h.targets), h.size(),
		msgStamp(r.now), msgStamp(r.now.Add(ConsentExpiry)))
	return err
}

// consentReplay answers a message held before under its ID or sequence:
// the same message as its request stands, anything else a conflict.
func consentReplay(ctx context.Context, tx *sql.Tx, r newRow) (messaging.Answer, bool, error) {
	held, found, err := consentByMessage(ctx, tx, r.key.contact, r.m.ID)
	if err != nil || found {
		return replayedHold(held, r), found, err
	}
	var other bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM consent_requests WHERE contact = ? AND n = ?)`,
		r.key.contact, int64(r.m.N)).Scan(&other)
	if err != nil || !other {
		return messaging.Answer{}, false, err
	}
	return refused(messaging.ReasonConflict), true, nil
}

// replayedHold answers the same request again: held while pending and
// unexpired, not shared once it ended. A dispatched one has its message.
func replayedHold(held consentRow, r newRow) messaging.Answer {
	hash := r.m.Hash()
	switch {
	case string(held.hash) != string(hash[:]):
		return refused(messaging.ReasonConflict)
	case held.State == consentPending && msgStamp(r.now) < held.ExpiresAt:
		return heldAnswer(held.ID)
	case held.State == consentAllowed:
		return stored
	}
	return refused(messaging.ReasonNotShared)
}

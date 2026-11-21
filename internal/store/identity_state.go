package store

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/pairtask/pairtask/internal/identity"
)

// Identity state (#392, #421 slice 1): what this workspace holds about
// other people's identities, kept as the inputs that produced it, never as
// a snapshot. identity.Held holds statement interfaces and proof context
// that only the library's own parsers rebuild, so the store keeps every
// signed statement verbatim and every local trust decision in the order
// they happened, and rebuilds Held by replaying them through Apply,
// Approve and Resolve. Replay reads nothing current: the workspace keys
// and the local key a resolution was checked with are inputs too.

const identitySchema = `
CREATE TABLE IF NOT EXISTS identity_lineages (
    lineage    TEXT PRIMARY KEY,
    anchor     BLOB NOT NULL,
    created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS identity_inputs (
    seq         INTEGER PRIMARY KEY AUTOINCREMENT,
    lineage     TEXT NOT NULL,
    kind        TEXT NOT NULL,
    body        BLOB NOT NULL,
    digest      TEXT NOT NULL DEFAULT '',
    received_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS identity_inputs_lineage ON identity_inputs(lineage, seq);
CREATE UNIQUE INDEX IF NOT EXISTS identity_inputs_statement ON identity_inputs(lineage, digest) WHERE kind = 'statement';
CREATE TABLE IF NOT EXISTS identity_keys (
    key_id  TEXT PRIMARY KEY,
    lineage TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS identity_contacts (
    lineage    TEXT NOT NULL,
    contact    TEXT NOT NULL,
    first_seen TEXT NOT NULL,
    PRIMARY KEY (lineage, contact)
);
CREATE TABLE IF NOT EXISTS identity_clock (
    id   INTEGER PRIMARY KEY CHECK (id = 1),
    mark TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS identity_outbox (
    contact       TEXT NOT NULL,
    pairing_epoch INTEGER NOT NULL,
    digest        TEXT NOT NULL,
    body          BLOB NOT NULL,
    queued_at     TEXT NOT NULL,
    effect        TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (contact, pairing_epoch, digest)
);
CREATE TABLE IF NOT EXISTS identity_admissions (
    contact       TEXT NOT NULL,
    pairing_epoch INTEGER NOT NULL,
    message_id    TEXT NOT NULL,
    lineage       TEXT NOT NULL,
    admitted_at   TEXT NOT NULL,
    state         TEXT NOT NULL,
    epoch         INTEGER NOT NULL,
    device        TEXT NOT NULL,
    record_digest TEXT NOT NULL,
    PRIMARY KEY (contact, pairing_epoch, message_id)
);
`

// Input kinds. A workspace key is known to every lineage, so it is kept
// once under the empty lineage and replayed, in its place in the global
// order, into each lineage's state.
const (
	inputStatement = "statement"
	inputApprove   = "approve"
	inputResolve   = "resolve"
	inputWorkspace = "workspace"
	inputLegacy    = "legacy"
	// inputFull marks that the lineage reached MaxIdentityInputs: replay
	// freezes it from that point on, so nothing from it verifies, across
	// restarts, until the operator resolves it.
	inputFull     = "full"
	globalLineage = ""
)

// MaxIdentityInputs bounds what a lineage may accumulate. Reaching it
// freezes the lineage here, so nothing from it verifies until the
// operator acts: a revocation can't be lost to the bound.
const MaxIdentityInputs = 4096

// ErrIdentityFull: the lineage reached MaxIdentityInputs and is frozen.
var ErrIdentityFull = errors.New("identity history full; the lineage is frozen until resolved")

// errHistoryFull is the reason a full lineage reports.
const errHistoryFull = "history full"

func lineageKey(l identity.Lineage) string { return hex.EncodeToString(l[:]) }

// statementLineage is the lineage a statement belongs to.
func statementLineage(st identity.Statement) (identity.Lineage, bool) {
	switch s := st.(type) {
	case identity.Anchor:
		return s.Lineage(), true
	case identity.Record:
		return s.Lineage, true
	case identity.Revocation:
		return s.Lineage, true
	case identity.Notice:
		return s.Lineage, true
	case identity.Succession:
		return s.Lineage, true
	case identity.Handover:
		return s.Lineage, true
	}
	return identity.Lineage{}, false
}

// input is one stored input, as replay reads it.
type input struct {
	kind string
	body []byte
	at   time.Time
}

// identityHeld rebuilds a lineage's state by replaying its inputs and the global
// workspace keys in their stored order. found is false for a lineage never
// stored.
func identityHeld(ctx context.Context, q rowsQuerier, l identity.Lineage) (identity.Held, bool, error) {
	key := lineageKey(l)
	inputs, err := lineageInputs(ctx, q, key)
	if err != nil || len(inputs) == 0 {
		return identity.Held{}, false, err
	}
	h := identity.Held{Workspaces: map[string]ed25519.PublicKey{}}
	started := false
	for _, in := range inputs {
		if h, started, err = replayInput(h, started, in); err != nil {
			return identity.Held{}, false, fmt.Errorf("replay %s: %w", in.kind, err)
		}
	}
	return h, started, nil
}

func lineageInputs(ctx context.Context, q rowsQuerier, key string) ([]input, error) {
	rows, err := q.QueryContext(ctx, `SELECT kind, body, received_at FROM identity_inputs WHERE lineage IN (?, '') ORDER BY seq`, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []input
	for rows.Next() {
		var in input
		var at string
		if err := rows.Scan(&in.kind, &in.body, &at); err != nil {
			return nil, err
		}
		if in.at, err = time.Parse(time.RFC3339Nano, at); err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

// replayInput applies one stored input. Workspace keys seen before the
// lineage's anchor still count: genesis carries the known keys forward.
func replayInput(h identity.Held, started bool, in input) (identity.Held, bool, error) {
	switch in.kind {
	case inputWorkspace:
		pub, id := ed25519.PublicKey(in.body), identity.KeyID(ed25519.PublicKey(in.body))
		ws := cloneKeys(h.Workspaces)
		ws[id] = pub
		h.Workspaces = ws
		return h, started, nil
	case inputStatement:
		st, err := identity.Parse(in.body)
		if err != nil {
			return h, started, err
		}
		next, _, err := h.Apply(st, in.at)
		return next, true, err
	case inputApprove:
		next, err := h.Approve()
		return next, started, err
	case inputResolve:
		return replayResolve(h, started, in.body)
	case inputLegacy:
		h.Legacy = append(append([]string(nil), h.Legacy...), string(in.body))
		return h, started, nil
	case inputFull:
		h.Trust = identity.Frozen
		return h, started, nil
	}
	return h, started, fmt.Errorf("unknown identity input %q", in.kind)
}

// resolveBody is a Resolution and the local key it was checked with: 32
// key bytes, then the marshalled resolution.
func resolveBody(r identity.Resolution, local ed25519.PublicKey) []byte {
	return append(append([]byte(nil), local...), r.Marshal()...)
}

func replayResolve(h identity.Held, started bool, body []byte) (identity.Held, bool, error) {
	if len(body) < ed25519.PublicKeySize {
		return h, started, identity.ErrMalformed
	}
	r, err := identity.ParseResolution(body[ed25519.PublicKeySize:])
	if err != nil {
		return h, started, err
	}
	next, err := identity.Resolve(h, r, ed25519.PublicKey(body[:ed25519.PublicKeySize]))
	return next, started, err
}

func cloneKeys(m map[string]ed25519.PublicKey) map[string]ed25519.PublicKey {
	out := make(map[string]ed25519.PublicKey, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	return out
}

// HeldIdentity is what this workspace holds about a lineage now, rebuilt
// from its inputs.
func (s *Store) HeldIdentity(ctx context.Context, l identity.Lineage) (identity.Held, bool, error) {
	return identityHeld(ctx, s.db, l)
}

// keepEffects are the Apply outcomes whose statement changes, or may later
// change, the state: anything else (a rejection, a duplicate) had no effect
// and isn't stored, so a statement rejected for a reason that can change
// is judged afresh when it comes again.
var keepEffects = map[identity.EffectKind]bool{identity.Applied: true, identity.Parked: true, identity.FrozenEffect: true}

// applyStatement judges one statement against the stored state and keeps
// it when it had an effect, all in tx.
func applyStatement(ctx context.Context, tx *sql.Tx, s arrival) (identity.Effect, error) {
	st, err := identity.Parse(s.body)
	if err != nil {
		return identity.Effect{}, err
	}
	l, ok := statementLineage(st)
	if !ok {
		return identity.Effect{Kind: identity.Rejected, Reason: "not a statement a peer holds"}, nil
	}
	h, _, err := identityHeld(ctx, tx, l)
	if err != nil {
		return identity.Effect{}, err
	}
	// Judged first: a statement that wouldn't be kept (a rejection, a
	// duplicate, a bad signature) is a no-op at the bound as anywhere, so
	// only a statement that would change the state can freeze a full
	// lineage.
	next, eff, err := h.Apply(st, s.at)
	if err != nil || !keepEffects[eff.Kind] {
		return eff, err
	}
	if full, err := inputsFull(ctx, tx, l); err != nil || full {
		if err == nil {
			err = markFull(ctx, tx, l, s.at)
		}
		return identity.Effect{Kind: identity.Rejected, Reason: errHistoryFull}, err
	}
	return eff, keepStatement(ctx, tx, keptStatement{l, st, s, next})
}

// arrival is a statement's bytes, when it came, and from whom ("" for a
// local import).
type arrival struct {
	body    []byte
	at      time.Time
	contact string
}

// keptStatement is a statement with an effect and the state it led to.
type keptStatement struct {
	lineage identity.Lineage
	st      identity.Statement
	arrival
	next identity.Held
}

func keepStatement(ctx context.Context, tx *sql.Tx, k keptStatement) error {
	key := lineageKey(k.lineage)
	if a, ok := k.st.(identity.Anchor); ok {
		if _, err := tx.ExecContext(ctx, `INSERT INTO identity_lineages (lineage, anchor, created_at) VALUES (?, ?, ?) ON CONFLICT DO NOTHING`,
			key, a.Marshal(), msgStamp(k.at)); err != nil {
			return err
		}
	}
	if err := insertInput(ctx, tx, storedInput{key, inputStatement, k.body, identity.DigestOf(k.st.TBS()).String(), k.at}); err != nil {
		return err
	}
	if err := indexKeys(ctx, tx, key, k.next); err != nil {
		return err
	}
	return noteLineageContact(ctx, tx, k)
}

// storedInput is one row of identity_inputs.
type storedInput struct {
	lineage, kind string
	body          []byte
	digest        string
	at            time.Time
}

func insertInput(ctx context.Context, tx *sql.Tx, in storedInput) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO identity_inputs (lineage, kind, body, digest, received_at) VALUES (?, ?, ?, ?, ?)`,
		in.lineage, in.kind, in.body, in.digest, msgStamp(in.at))
	return err
}

// indexKeys records every person and recovery key the lineage has
// presented, so a seal is matched to its lineage by its root alone,
// whichever contact or protocol version it arrived through.
func indexKeys(ctx context.Context, tx *sql.Tx, key string, h identity.Held) error {
	for id := range h.Keys {
		if _, err := tx.ExecContext(ctx, `INSERT INTO identity_keys (key_id, lineage) VALUES (?, ?) ON CONFLICT DO NOTHING`, id, key); err != nil {
			return err
		}
	}
	return nil
}

func noteLineageContact(ctx context.Context, tx *sql.Tx, k keptStatement) error {
	if k.contact == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO identity_contacts (lineage, contact, first_seen) VALUES (?, ?, ?) ON CONFLICT DO NOTHING`,
		lineageKey(k.lineage), k.contact, msgStamp(k.at))
	return err
}

// inputsFull: the lineage reached the bound (its one full marker aside).
func inputsFull(ctx context.Context, tx *sql.Tx, l identity.Lineage) (bool, error) {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM identity_inputs WHERE lineage = ? AND kind != ?`, lineageKey(l), inputFull).Scan(&n)
	return n >= MaxIdentityInputs, err
}

// markFull records, once, that the lineage hit the bound: from here on its
// state replays frozen. A statement turned away for the bound might have
// been a revocation, so trusting anything from the lineage after it would
// fail open.
func markFull(ctx context.Context, tx *sql.Tx, l identity.Lineage, at time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO identity_inputs (lineage, kind, body, digest, received_at)
SELECT ?1, ?2, x'', '', ?3 WHERE NOT EXISTS (SELECT 1 FROM identity_inputs WHERE lineage = ?1 AND kind = ?2)`,
		lineageKey(l), inputFull, msgStamp(at))
	return err
}

// lineageOfKey is the held lineage a person or recovery key belongs to.
func lineageOfKey(ctx context.Context, q rowQuerier, keyID string) (identity.Lineage, bool, error) {
	var key string
	err := q.QueryRowContext(ctx, `SELECT lineage FROM identity_keys WHERE key_id = ?`, keyID).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return identity.Lineage{}, false, nil
	}
	if err != nil {
		return identity.Lineage{}, false, err
	}
	var l identity.Lineage
	b, err := hex.DecodeString(key)
	if err != nil || len(b) != len(l) {
		return l, false, fmt.Errorf("stored lineage %q is malformed", key)
	}
	copy(l[:], b)
	return l, true, nil
}

// ApplyIdentity takes one statement from the contact this store is bound
// to (peer sync), keeps it when it had an effect, and says what it did.
func (p *PeerMessages) ApplyIdentity(ctx context.Context, statement []byte, at time.Time) (identity.Effect, error) {
	contact := ""
	if p.fence != nil {
		contact = p.fence.contact
	}
	var eff identity.Effect
	err := p.inTx(ctx, contact, func(tx *sql.Tx) (err error) {
		eff, err = applyStatement(ctx, tx, arrival{statement, at.UTC(), contact})
		return err
	})
	return eff, err
}

// ApproveIdentity records the operator's approval of a lineage as
// displayed: its P, R and L together.
func (s *Store) ApproveIdentity(ctx context.Context, l identity.Lineage, at time.Time) error {
	return s.localDecision(ctx, l, func(h identity.Held) (storedInput, error) {
		if _, err := h.Approve(); err != nil {
			return storedInput{}, err
		}
		return storedInput{lineageKey(l), inputApprove, []byte{}, "", at}, nil
	})
}

// ResolveIdentity records the operator's choice in the frozen lineage the
// resolution names, checked with the local key that signed it, which is
// kept with it.
func (s *Store) ResolveIdentity(ctx context.Context, r identity.Resolution, local ed25519.PublicKey, at time.Time) error {
	return s.localDecision(ctx, r.Lineage, func(h identity.Held) (storedInput, error) {
		if _, err := identity.Resolve(h, r, local); err != nil {
			return storedInput{}, err
		}
		return storedInput{lineageKey(r.Lineage), inputResolve, resolveBody(r, local), "", at}, nil
	})
}

// localDecision checks a decision against the current state and, when it
// holds, appends it as an input, in one transaction.
func (s *Store) localDecision(ctx context.Context, l identity.Lineage, decide func(identity.Held) (storedInput, error)) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	h, found, err := identityHeld(ctx, tx, l)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%w: no such lineage", identity.ErrMalformed)
	}
	in, err := decide(h)
	if err != nil {
		return err
	}
	if err := insertInput(ctx, tx, in); err != nil {
		return err
	}
	return tx.Commit()
}

// NoteWorkspaceKey records a workspace key this side learned (from pairing
// or its own identity), once, in its place in the order bindings are
// judged.
func (s *Store) NoteWorkspaceKey(ctx context.Context, pub ed25519.PublicKey, at time.Time) error {
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: workspace key", identity.ErrMalformed)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO identity_inputs (lineage, kind, body, digest, received_at)
SELECT '', ?, ?, ?, ? WHERE NOT EXISTS (SELECT 1 FROM identity_inputs WHERE lineage = '' AND kind = ? AND digest = ?)`,
		inputWorkspace, []byte(pub), identity.KeyID(pub), msgStamp(at), inputWorkspace, identity.KeyID(pub))
	return err
}

// IdentityProofs is the retained recovery chain of a lineage from an
// epoch and counter on, as signed statements, for a peer that parked one.
func (s *Store) IdentityProofs(ctx context.Context, l identity.Lineage, fromEpoch, fromCounter uint64) ([][]byte, error) {
	h, found, err := identityHeld(ctx, s.db, l)
	if err != nil || !found {
		return nil, err
	}
	var out [][]byte
	for _, p := range h.History {
		if proofFrom(p, fromEpoch, fromCounter) {
			out = append(out, p.Statement.Marshal())
		}
	}
	return out, nil
}

// proofFrom: a retained proof at or after the requested point.
func proofFrom(p identity.Proof, fromEpoch, fromCounter uint64) bool {
	switch s := p.Statement.(type) {
	case identity.Succession:
		return s.Epoch >= fromEpoch
	case identity.Handover:
		return s.Counter >= fromCounter
	}
	return false
}

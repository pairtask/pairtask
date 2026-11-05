package store

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
)

// Session authority (#293). "Allow for this session" leaves a reusable
// grant for exactly the scopes it showed, bound to the broker incarnation
// and operator connection that decided it. One live owner row names the
// connection that owns the broker now; a session grant admits only while
// its (epoch, gen) is that row's, and never while the in-memory gate is
// suspended: a Set or End that has not committed leaves no session
// authority usable. Opening the store clears both, so a restarted broker
// revives none.

// consentOwnerSchema keeps the live owner and the session grants.
const consentOwnerSchema = `
CREATE TABLE IF NOT EXISTS consent_owner (
    id     INTEGER PRIMARY KEY CHECK (id = 1),
    epoch  TEXT NOT NULL,
    gen    INTEGER NOT NULL,
    set_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS consent_sessions (
    contact    TEXT NOT NULL,
    agent      TEXT NOT NULL,
    operation  TEXT NOT NULL,
    formal     INTEGER NOT NULL,
    epoch      TEXT NOT NULL,
    gen        INTEGER NOT NULL,
    consent_id TEXT NOT NULL,
    decided_at TEXT NOT NULL,
    PRIMARY KEY (contact, agent, operation)
);
`

// consentGate fails session admission closed: suspended from the start of
// an owner change until it commits, and after one that failed.
type consentGate struct{ suspended atomic.Bool }

// clearConsentOwner ends every session's authority as the store opens: a
// new broker incarnation has no owner until the hub sets one.
func clearConsentOwner(db *sql.DB) error {
	_, err := db.Exec(`DELETE FROM consent_owner; DELETE FROM consent_sessions;`)
	return err
}

// SetConsentOwner records the operator connection (epoch, gen) that owns
// the broker now, replacing any other; every session grant of another
// connection is dead and goes. Session admission stays suspended unless
// this commits.
func (p *PeerMessages) SetConsentOwner(ctx context.Context, epoch string, gen uint64) error {
	return p.ownerChange(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO consent_owner (id, epoch, gen, set_at) VALUES (1, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET epoch = excluded.epoch, gen = excluded.gen, set_at = excluded.set_at`,
			epoch, int64(gen), msgStamp(time.Now()))
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM consent_sessions WHERE NOT (epoch = ? AND gen = ?)`, epoch, int64(gen))
		return err
	})
}

// EndConsentOwner clears the live owner when it is still (epoch, gen),
// with that connection's session grants; another owner is left as it is.
func (p *PeerMessages) EndConsentOwner(ctx context.Context, epoch string, gen uint64) error {
	return p.ownerChange(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM consent_owner WHERE epoch = ? AND gen = ?`, epoch, int64(gen))
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM consent_sessions WHERE epoch = ? AND gen = ?`, epoch, int64(gen))
		return err
	})
}

// ownerChange suspends session admission, applies change in one
// transaction, and lifts the suspension only once it committed.
func (p *PeerMessages) ownerChange(ctx context.Context, change func(*sql.Tx) error) error {
	p.gate.suspended.Store(true)
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := change(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	p.gate.suspended.Store(false)
	return nil
}

// sessionOwner is the live owner session grants may admit under now: none
// while the gate is suspended, or with no owner row.
func sessionOwner(ctx context.Context, tx *sql.Tx, gate *consentGate) (LiveOwner, error) {
	if gate == nil || gate.suspended.Load() {
		return LiveOwner{}, nil
	}
	var o LiveOwner
	var gen int64
	err := tx.QueryRowContext(ctx, `SELECT epoch, gen FROM consent_owner WHERE id = 1`).Scan(&o.Epoch, &gen)
	if errors.Is(err, sql.ErrNoRows) {
		return LiveOwner{}, nil
	}
	o.Gen = uint64(gen)
	return o, err
}

// sessionCovers: a session grant of the live owner covers the scope, at
// the asked publication ceiling.
func sessionCovers(ctx context.Context, tx *sql.Tx, sc consentScope, q scopeQuery) (bool, error) {
	if q.owner.Gen == 0 {
		return false, nil
	}
	var covered bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM consent_sessions WHERE contact = ? AND agent = ? AND operation = ?
    AND epoch = ? AND gen = ? AND (formal OR NOT ?))`, sc.contact, sc.agent, sc.operation, q.owner.Epoch, int64(q.owner.Gen),
		askedFormal(sc, q.ask)).Scan(&covered)
	return covered, err
}

// askedFormal: the scope is publication, asked formally.
func askedFormal(sc consentScope, ask *messaging.ReviewAsk) bool {
	return sc.operation == protocol.ConsentPublish && ask != nil && ask.Formal
}

// grantSession leaves, for a session decision, a reusable grant of exactly
// each scope the request showed undecided, at its asked ceiling, bound to
// the deciding connection.
func grantSession(ctx context.Context, tx *sql.Tx, c decided) error {
	for _, sc := range c.undecided {
		_, err := tx.ExecContext(ctx, `INSERT INTO consent_sessions (contact, agent, operation, formal, epoch, gen, consent_id, decided_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (contact, agent, operation) DO UPDATE SET formal = excluded.formal,
    epoch = excluded.epoch, gen = excluded.gen, consent_id = excluded.consent_id, decided_at = excluded.decided_at`,
			sc.contact, sc.agent, sc.operation, askedFormal(sc, c.held.Review), c.d.Evidence.Epoch, int64(c.d.Evidence.Gen),
			c.held.ID, msgStamp(c.d.Now))
		if err != nil {
			return err
		}
	}
	return nil
}

// endSessions ends the session grants of a scope explicitly denied.
func endSessions(ctx context.Context, tx *sql.Tx, sc consentScope) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM consent_sessions WHERE contact = ? AND agent = ? AND operation = ?`,
		sc.contact, sc.agent, sc.operation)
	return err
}

// endContactSessions ends every session grant of a contact, as a pause does.
func endContactSessions(ctx context.Context, tx *sql.Tx, contact string) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM consent_sessions WHERE contact = ?`, contact)
	return err
}

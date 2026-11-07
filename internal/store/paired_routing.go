package store

import (
	"context"
	"database/sql"

	"github.com/pairtask/pairtask/internal/protocol"
)

// HomeRoute is who a ⇄ send also went to on this side (#327): its tagged
// agents, canonical, and whether this side's operator.
type HomeRoute struct {
	Agents   []string
	Operator bool
}

// HomeRouting is the home route of the ⇄ send id to contact in its
// current pairing (#276), as its message here was stored: none for a send
// that tagged no one here.
func (s *Store) HomeRouting(ctx context.Context, contact, id string) (HomeRoute, error) {
	return homeRoute(ctx, s.db, homeRouteQuery(currentEpochAt(2)), id, contact)
}

// HomeRoutingIn is HomeRouting in the pairing k names (#276): its epoch,
// or with 0 the newest the contact's history holds; another pairing's send
// is never borrowed. An epoch the contact never had is messaging.ErrInvalid.
func (s *Store) HomeRoutingIn(ctx context.Context, k MessageKey) (HomeRoute, error) {
	route, _, err := inHistory(ctx, s.db, k, func(tx *sql.Tx, pk pairingKey) (HomeRoute, bool, error) {
		r, err := homeRoute(ctx, tx, homeRouteQuery("?3"), k.ID, pk.contact, pk.epoch)
		return r, true, err
	})
	return route, err
}

// homeRouteQuery reads the targets of the ⇄ send ?1 to contact ?2 in the
// pairing epoch names.
func homeRouteQuery(epoch string) string {
	return `SELECT r.agent_id FROM paired_threads t JOIN message_receipts r ON r.message_id = t.local_id
WHERE t.local_id = ?1 AND t.contact = ?2 AND t.root_local_id = t.local_id AND t.pairing_epoch = ` + epoch + ` ORDER BY r.agent_id`
}

// homeRoute is the route a homeRouteQuery reads.
func homeRoute(ctx context.Context, q rowsQuerier, query string, args ...any) (HomeRoute, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return HomeRoute{}, err
	}
	defer rows.Close()
	var route HomeRoute
	for rows.Next() {
		var target string
		if err := rows.Scan(&target); err != nil {
			return HomeRoute{}, err
		}
		route.add(target)
	}
	return route, rows.Err()
}

// add counts one stored target: this side's operator, or an agent.
func (r *HomeRoute) add(target string) {
	if target == protocol.OperatorID {
		r.Operator = true
		return
	}
	r.Agents = append(r.Agents, target)
}

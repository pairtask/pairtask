package store

import (
	"context"

	"github.com/pairtask/pairtask/internal/protocol"
)

// HomeRoute is who a ⇄ send also went to on this side (#327): its tagged
// agents, canonical, and whether this side's operator.
type HomeRoute struct {
	Agents   []string
	Operator bool
}

// HomeRouting is the home route of the ⇄ send id to contact, as its
// message here was stored: none for a send that tagged no one here.
func (s *Store) HomeRouting(ctx context.Context, contact, id string) (HomeRoute, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT r.agent_id FROM paired_threads t JOIN message_receipts r ON r.message_id = t.local_id
WHERE t.local_id = ? AND t.contact = ? AND t.root_local_id = t.local_id ORDER BY r.agent_id`, id, contact)
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

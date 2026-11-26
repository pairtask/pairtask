package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/pairtask/pairtask/internal/protocol"
)

// BroadcastAudience is the frozen outcome returned to the author. Excluded
// agents have no pending copy. Retrying the message repeats this outcome.
type BroadcastAudience struct {
	Recipients []protocol.Recipient
	Excluded   []string
}

// PersistPublicBroadcast admits the operator and each agent with inbox space.
// An unavailable operator inbox or a global limit still refuses the whole post.
func (s *Store) PersistPublicBroadcast(ctx context.Context, msg protocol.Message, candidates []protocol.Recipient) (BroadcastAudience, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BroadcastAudience{}, err
	}
	defer tx.Rollback()
	audience, err := publicAudience(ctx, tx, msg, candidates)
	if err != nil {
		return BroadcastAudience{}, err
	}
	route := routedMessage{msg: msg, senderSession: msg.SessionID, recipients: audience.Recipients, excluded: audience.Excluded}
	if err := persistMessage(ctx, tx, route); err != nil {
		return BroadcastAudience{}, err
	}
	return audience, tx.Commit()
}

func publicAudience(ctx context.Context, tx *sql.Tx, msg protocol.Message, candidates []protocol.Recipient) (BroadcastAudience, error) {
	wire, err := DurableWireBytes(msg)
	if err != nil {
		return BroadcastAudience{}, err
	}
	var audience BroadcastAudience
	for _, r := range candidates {
		if r.AgentID != protocol.OperatorID {
			fits, err := recipientCapacity(ctx, tx, r.AgentID, wire)
			if err != nil {
				return BroadcastAudience{}, err
			}
			if !fits {
				audience.Excluded = append(audience.Excluded, r.AgentID)
				continue
			}
		}
		audience.Recipients = append(audience.Recipients, r)
	}
	return audience, nil
}

func recordBroadcastExclusions(ctx context.Context, tx *sql.Tx, id string, excluded sql.NullString) error {
	if !excluded.Valid {
		return nil
	}
	_, err := tx.ExecContext(ctx, "INSERT INTO broadcast_exclusions VALUES (?,?)", id, excluded.String)
	return err
}
func (s *Store) loadBroadcastExclusions(ctx context.Context, out *RetrySnapshot) error {
	if out.Message.To != protocol.Broadcast {
		return nil
	}
	var encoded string
	err := s.db.QueryRowContext(ctx, "SELECT agents FROM broadcast_exclusions WHERE message_id=?", out.Message.MessageID).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(encoded), &out.Excluded)
}

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/pairtask/pairtask/pairbook"
)

// Space maps (Pairbook v1): the last owner-signed map this workspace
// verified for each pinned space, and its mark (version and digest). The
// mark only goes up, survives restarts and outlives the map's expiry: it is
// what refuses an older or different map, and the owner renews from the
// last map. A map is written only after it verifies against the space's
// pin and the stored mark, and only if that mark is still the stored one
// (compare and swap), so two writers through separate handles can't both
// win.

const spaceMapsSchema = `
CREATE TABLE IF NOT EXISTS space_maps (
    space   TEXT PRIMARY KEY,
    map     BLOB NOT NULL,
    version TEXT NOT NULL,
    digest  BLOB NOT NULL,
    kept_at TEXT NOT NULL
);
`

// ErrNoSpacePin: a map for a space this workspace hasn't pinned.
var ErrNoSpacePin = errors.New("store: no pin for this space")

// storedMark is a space's stored mark; found is false when none is stored.
type storedMark struct {
	pairbook.Mark
	found bool
}

// KeepSpaceMap verifies m as space's map at now, against the pinned owner
// and the stored mark, and stores it when it is newer. The same map again
// changes nothing. On any error nothing is stored.
func (s *Store) KeepSpaceMap(ctx context.Context, space string, m pairbook.SignedSpaceMap, now time.Time) (pairbook.VerifiedMap, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return pairbook.VerifiedMap{}, err
	}
	defer tx.Rollback()
	v, err := keepSpaceMap(ctx, tx, space, m, now)
	if err != nil {
		return pairbook.VerifiedMap{}, err
	}
	return v, tx.Commit()
}

func keepSpaceMap(ctx context.Context, tx *sql.Tx, space string, m pairbook.SignedSpaceMap, now time.Time) (pairbook.VerifiedMap, error) {
	pin, ok, err := spacePin(ctx, tx, space)
	if err != nil {
		return pairbook.VerifiedMap{}, err
	}
	if !ok {
		return pairbook.VerifiedMap{}, ErrNoSpacePin
	}
	prev, err := spaceMark(ctx, tx, space)
	if err != nil {
		return pairbook.VerifiedMap{}, err
	}
	v, err := m.Verify(space, pin.Owner, prev.Mark, now)
	if err != nil || v.Mark == prev.Mark {
		return v, err
	}
	return v, swapSpaceMap(ctx, tx, m, prev, now)
}

// swapSpaceMap writes m in place of the map prev names, only if prev is
// still what is stored.
func swapSpaceMap(ctx context.Context, tx *sql.Tx, m pairbook.SignedSpaceMap, prev storedMark, now time.Time) error {
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	mark := m.SpaceMap.Mark()
	res, err := tx.ExecContext(ctx, `INSERT INTO space_maps (space, map, version, digest, kept_at) VALUES (?1, ?2, ?3, ?4, ?5)
ON CONFLICT (space) DO UPDATE SET map = ?2, version = ?3, digest = ?4, kept_at = ?5
WHERE space_maps.version = ?6 AND space_maps.digest = ?7`,
		m.Space, body, strconv.FormatUint(mark.Version, 10), mark.Digest[:], msgStamp(now), strconv.FormatUint(prev.Version, 10), prev.Digest[:])
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return errors.Join(pairbook.ErrRollback, err) // another writer moved the mark first
	}
	return nil
}

// spaceMark is space's stored mark.
func spaceMark(ctx context.Context, q rowQuerier, space string) (storedMark, error) {
	var version string
	var digest []byte
	err := q.QueryRowContext(ctx, `SELECT version, digest FROM space_maps WHERE space = ?`, space).Scan(&version, &digest)
	if errors.Is(err, sql.ErrNoRows) {
		return storedMark{}, nil
	}
	if err != nil {
		return storedMark{}, err
	}
	return parseMark(version, digest)
}

func parseMark(version string, digest []byte) (storedMark, error) {
	v, err := strconv.ParseUint(version, 10, 64)
	if err != nil || len(digest) != 32 {
		return storedMark{}, errors.New("store: damaged space map mark")
	}
	m := storedMark{found: true}
	m.Version = v
	copy(m.Digest[:], digest)
	return m, nil
}

// SpaceMap is space's last kept map and its mark, even if expired; found is
// false when none is kept. A stored map that no longer matches its mark is
// an error, never returned.
func (s *Store) SpaceMap(ctx context.Context, space string) (pairbook.SignedSpaceMap, pairbook.Mark, bool, error) {
	var body, digest []byte
	var version string
	err := s.db.QueryRowContext(ctx, `SELECT map, version, digest FROM space_maps WHERE space = ?`, space).Scan(&body, &version, &digest)
	if errors.Is(err, sql.ErrNoRows) {
		return pairbook.SignedSpaceMap{}, pairbook.Mark{}, false, nil
	}
	if err != nil {
		return pairbook.SignedSpaceMap{}, pairbook.Mark{}, false, err
	}
	return decodeSpaceMap(body, version, digest)
}

func decodeSpaceMap(body []byte, version string, digest []byte) (pairbook.SignedSpaceMap, pairbook.Mark, bool, error) {
	mark, err := parseMark(version, digest)
	if err != nil {
		return pairbook.SignedSpaceMap{}, pairbook.Mark{}, false, err
	}
	var m pairbook.SignedSpaceMap
	if err := json.Unmarshal(body, &m); err != nil || m.SpaceMap.Mark() != mark.Mark {
		return pairbook.SignedSpaceMap{}, pairbook.Mark{}, false, errors.New("store: damaged space map")
	}
	return m, mark.Mark, true, nil
}

// SpaceMapSpaces is every space with a kept map.
func (s *Store) SpaceMapSpaces(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT space FROM space_maps ORDER BY space`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

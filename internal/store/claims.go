package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// Claim takes ownership of a thread, or reports who has it.
//
// One statement, which is why claims live in sqlite rather than in files: no lock file and no ordering rule for
// every participant to reimplement. Measured with 20 connections racing one thread: one winner, one row, no
// retries.
//
// Re-claiming what you hold is granted and updates the note, since refining your own note is not a conflict.
func (s *Store) Claim(ctx context.Context, req ClaimRequest) (ClaimResult, error) {
	if req.Channel == "" {
		return ClaimResult{}, ErrNoChannel
	}
	if req.Thread == "" {
		return ClaimResult{}, ErrNoThread
	}
	if req.Holder == "" {
		return ClaimResult{}, ErrNoHolder
	}
	paths, err := encodePaths(req.Paths)
	if err != nil {
		return ClaimResult{}, err
	}

	var result ClaimResult
	err = s.tx(ctx, func(tx *sql.Tx) error {
		channelID, err := s.channelID(ctx, tx, req.Channel)
		if err != nil {
			return err
		}
		// A claim holder who is not in the roster is the one member a reader most wants to look up.
		if err := s.upsertMember(ctx, tx, channelID, req.Holder, req.Worktree); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `
INSERT INTO claims(channel_id, thread, holder, note, paths, created_at)
VALUES(?, ?, ?, ?, ?, ?)
ON CONFLICT(channel_id, thread) DO UPDATE SET
	note  = excluded.note,
	paths = excluded.paths
WHERE claims.holder = excluded.holder`,
			channelID, req.Thread, req.Holder, req.Note, paths, s.dbTime(),
		)
		if err != nil {
			return fmt.Errorf("claim %q in %q: %w", req.Thread, req.Channel, err)
		}
		// The conflicting insert changes nothing when the WHERE fails, so the row count is the
		// answer: 1 means taken or refreshed, 0 means somebody else holds it. created_at is left
		// alone on a refresh, so a claim keeps the age it was taken at.
		affected, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("claim %q in %q: %w", req.Thread, req.Channel, err)
		}
		result.Granted = affected > 0
		if result.Granted {
			// Taking the work is caring about the thread, so a mute the holder had on it goes, the same way
			// posting to it lifts one.
			if err := setMuted(ctx, tx, channelID, req.Holder, req.Thread, false); err != nil {
				return err
			}
		}
		// The holder is reported either way. A losing claim exists to name the holder and their
		// note, because that output is what stops duplicate work.
		result.Claim, _, err = loadClaim(ctx, tx, req.Channel, channelID, req.Thread)
		return err
	})
	if err != nil {
		return ClaimResult{}, err
	}
	return result, nil
}

// Release gives up a claim, and refuses to release someone else's unless forced.
//
// A claim held by a member that looks idle may belong to an agent waiting on its user, so releasing
// one is an explicit act rather than a cleanup.
func (s *Store) Release(ctx context.Context, req ReleaseRequest) (ReleaseResult, error) {
	if req.Channel == "" {
		return ReleaseResult{}, ErrNoChannel
	}
	if req.Thread == "" {
		return ReleaseResult{}, ErrNoThread
	}
	if req.Holder == "" && !req.Force {
		return ReleaseResult{}, ErrNoHolder
	}

	var result ReleaseResult
	err := s.tx(ctx, func(tx *sql.Tx) error {
		channelID, ok, err := lookupChannelID(ctx, tx, req.Channel)
		if err != nil || !ok {
			return err
		}
		if req.Holder != "" {
			if err := s.upsertMember(ctx, tx, channelID, req.Holder, req.Worktree); err != nil {
				return err
			}
		}
		claim, found, err := loadClaim(ctx, tx, req.Channel, channelID, req.Thread)
		if err != nil || !found {
			return err
		}
		result.Claim = claim
		if claim.Holder != req.Holder && !req.Force {
			return nil
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM claims WHERE channel_id = ? AND thread = ?`, channelID, req.Thread,
		); err != nil {
			return fmt.Errorf("release %q in %q: %w", req.Thread, req.Channel, err)
		}
		result.Released = true
		return nil
	})
	if err != nil {
		return ReleaseResult{}, err
	}
	return result, nil
}

// Claims lists a channel's claims, ordered by thread.
func (s *Store) Claims(ctx context.Context, channel string) ([]Claim, error) {
	if channel == "" {
		return nil, ErrNoChannel
	}
	channelID, ok, err := s.lookupChannelID(ctx, channel)
	if err != nil || !ok {
		return nil, err
	}
	return claimRows(ctx, s.db, channel, channelID)
}

// claimRows loads a channel's claims through whatever handle it is given, so a caller already inside a
// transaction stays inside it: what a deletion reports has to be what the deletion took.
func claimRows(ctx context.Context, q queryer, channel string, channelID int64) ([]Claim, error) {
	rows, err := q.QueryContext(ctx, `
SELECT thread, holder, note, paths, created_at
FROM claims WHERE channel_id = ? ORDER BY thread`, channelID)
	if err != nil {
		return nil, fmt.Errorf("list claims in %q: %w", channel, err)
	}
	defer rows.Close()

	var claims []Claim
	for rows.Next() {
		c, err := scanClaim(rows, channel)
		if err != nil {
			return nil, fmt.Errorf("list claims in %q: %w", channel, err)
		}
		claims = append(claims, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list claims in %q: %w", channel, err)
	}
	return claims, nil
}

func loadClaim(ctx context.Context, q queryer, channel string, channelID int64, thread string) (Claim, bool, error) {
	row := q.QueryRowContext(ctx, `
SELECT thread, holder, note, paths, created_at
FROM claims WHERE channel_id = ? AND thread = ?`, channelID, thread)
	c, err := scanClaim(row, channel)
	if errors.Is(err, sql.ErrNoRows) {
		return Claim{}, false, nil
	}
	if err != nil {
		return Claim{}, false, fmt.Errorf("read claim %q in %q: %w", thread, channel, err)
	}
	return c, true, nil
}

func scanClaim(sc scanner, channel string) (Claim, error) {
	c := Claim{Channel: channel}
	var (
		paths   string
		created int64
	)
	if err := sc.Scan(&c.Thread, &c.Holder, &c.Note, &paths, &created); err != nil {
		return Claim{}, err
	}
	var err error
	if c.Paths, err = decodePaths(paths); err != nil {
		return Claim{}, err
	}
	c.CreatedAt = fromDBTime(created)
	return c, nil
}

// Paths are stored as a JSON array rather than a delimited string, because a glob can contain most
// delimiters and none of them are worth escaping by hand.
func encodePaths(paths []string) (string, error) {
	if len(paths) == 0 {
		return "[]", nil
	}
	encoded, err := json.Marshal(paths)
	if err != nil {
		return "", fmt.Errorf("encode paths %v: %w", paths, err)
	}
	return string(encoded), nil
}

func decodePaths(encoded string) ([]string, error) {
	var paths []string
	if err := json.Unmarshal([]byte(encoded), &paths); err != nil {
		return nil, fmt.Errorf("decode paths %q: %w", encoded, err)
	}
	// An empty list comes back as nil, so a claim with no paths compares equal however it was
	// written.
	if len(paths) == 0 {
		return nil, nil
	}
	return paths, nil
}

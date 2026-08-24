package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Notice answers which of these claims a member has not been told about yet, and records that it now has.
//
// The guard is what calls it. As a permission decision the guard had nothing to remember, because it refused or
// asked every time an edit matched; as a notice it has to, or the same three lines arrive on every edit under one
// claim and the layer that was too loud stays too loud.
//
// A claim speaks once more when the holder changes, or when it was released and taken again, which is what
// comparing the recorded claim's age catches. Everything else is silence, and silence is the point.
func (s *Store) Notice(ctx context.Context, req NoticeRequest) (NoticeResult, error) {
	if req.Channel == "" {
		return NoticeResult{}, ErrNoChannel
	}
	if req.Member == "" {
		return NoticeResult{}, ErrNoMember
	}
	result := NoticeResult{Channel: req.Channel, Member: req.Member, New: []Claim{}}
	if len(req.Claims) == 0 {
		return result, nil
	}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		// Looked up rather than created: a hook in the path of every edit must not be what brings a channel
		// into existence, and a channel that does not exist has nothing claimed in it.
		channelID, ok, err := lookupChannelID(ctx, tx, req.Channel)
		if err != nil || !ok {
			return err
		}
		for _, claim := range req.Claims {
			told, err := alreadyTold(ctx, tx, channelID, req.Member, claim)
			if err != nil {
				return err
			}
			if told {
				continue
			}
			result.New = append(result.New, claim)
			if req.DryRun {
				continue
			}
			if _, err := tx.ExecContext(ctx, `
INSERT INTO notices(channel_id, member, thread, holder, claimed_at, told_at)
VALUES(?, ?, ?, ?, ?, ?)
ON CONFLICT(channel_id, member, thread) DO UPDATE SET
	holder     = excluded.holder,
	claimed_at = excluded.claimed_at,
	told_at    = excluded.told_at`,
				channelID, req.Member, claim.Thread, claim.Holder,
				claim.CreatedAt.UnixNano(), s.dbTime(),
			); err != nil {
				return fmt.Errorf("record telling %q about %q: %w", req.Member, claim.Thread, err)
			}
		}
		return nil
	})
	if err != nil {
		return NoticeResult{}, err
	}
	return result, nil
}

// alreadyTold is whether this member has heard about this claim as it now stands.
func alreadyTold(ctx context.Context, q queryer, channelID int64, member string, claim Claim) (bool, error) {
	var (
		holder    string
		claimedAt int64
	)
	err := q.QueryRowContext(ctx,
		`SELECT holder, claimed_at FROM notices WHERE channel_id = ? AND member = ? AND thread = ?`,
		channelID, member, claim.Thread,
	).Scan(&holder, &claimedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read what %q was told about %q: %w", member, claim.Thread, err)
	}
	// A newer claim on the same thread is a different piece of work, whoever holds it.
	return holder == claim.Holder && !claim.CreatedAt.After(fromDBTime(claimedAt)), nil
}

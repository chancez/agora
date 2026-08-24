package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Mute records that a member no longer wants to hear about a thread, or takes that back.
//
// It is the sticky half of triage, and the reason it exists is that the other half is not: Ack says "read to
// here" and the next message in the thread undoes it, so a member that had judged a thread irrelevant was told
// about it again every time it moved. The injected briefing was already promising the opposite.
//
// The cursor is left where it is. A muted thread keeps counting unread, because the briefing line naming
// "docs-rewrite (4)" is what stops a mute from becoming a thread nobody remembers exists, and because unmuting
// should hand back what was missed rather than a clean slate.
func (s *Store) Mute(ctx context.Context, req MuteRequest) (MuteResult, error) {
	if req.Channel == "" {
		return MuteResult{}, ErrNoChannel
	}
	if req.Member == "" {
		return MuteResult{}, ErrNoMember
	}
	if req.Thread == "" && !req.All {
		return MuteResult{}, ErrNoThread
	}
	result := MuteResult{
		Channel: req.Channel,
		Member:  req.Member,
		Muted:   !req.Unmute,
		Threads: []string{},
	}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		channelID, err := s.channelID(ctx, tx, req.Channel)
		if err != nil {
			return err
		}
		if err := s.upsertMember(ctx, tx, channelID, req.Member, req.Worktree); err != nil {
			return err
		}
		// Only threads not already in the state being asked for, so Threads is what this call changed and an
		// empty one means it was already that way. That holds for one thread and for --all alike.
		query := `
SELECT n.thread
FROM (SELECT thread FROM messages WHERE channel_id = ?
      UNION
      SELECT thread FROM claims WHERE channel_id = ?) n
LEFT JOIN cursors c ON c.channel_id = ? AND c.member = ? AND c.thread = n.thread
WHERE coalesce(c.muted, 0) != ?`
		args := []any{channelID, channelID, channelID, req.Member, boolToInt(result.Muted)}
		if req.Thread != "" {
			query += ` AND n.thread = ?`
			args = append(args, req.Thread)
		}
		query += ` ORDER BY n.thread`

		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("list threads to mute in %q: %w", req.Channel, err)
		}
		defer rows.Close()
		for rows.Next() {
			var thread string
			if err := rows.Scan(&thread); err != nil {
				return fmt.Errorf("list threads to mute in %q: %w", req.Channel, err)
			}
			result.Threads = append(result.Threads, thread)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("list threads to mute in %q: %w", req.Channel, err)
		}
		for _, thread := range result.Threads {
			if err := setMuted(ctx, tx, channelID, req.Member, thread, result.Muted); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return MuteResult{}, err
	}
	return result, nil
}

// setMuted writes one member's standing on one thread.
//
// Muting inserts, with cursor 0, because muting is not reading: a thread muted before it was ever read keeps
// every message in it unread. Unmuting only ever updates, because a row that is not there cannot be muted, and
// an upsert here would leave a cursor row behind every post and every claim, which is a row for nothing and a
// count that says a member has read threads it has not.
func setMuted(ctx context.Context, tx *sql.Tx, channelID int64, member, thread string, muted bool) error {
	stmt := `UPDATE cursors SET muted = 0 WHERE channel_id = ? AND member = ? AND thread = ? AND muted = 1`
	if muted {
		stmt = `
INSERT INTO cursors(channel_id, member, thread, cursor, muted)
VALUES(?, ?, ?, 0, 1)
ON CONFLICT(channel_id, member, thread) DO UPDATE SET muted = 1`
	}
	if _, err := tx.ExecContext(ctx, stmt, channelID, member, thread); err != nil {
		return fmt.Errorf("mute %q for %q: %w", thread, member, err)
	}
	return nil
}

// liftMutes is how a muted thread comes back, and it runs on every post: the author's own mute goes, since
// speaking in a thread is caring about it again, and so does the mute of anybody the message names.
//
// Being named is the only path through a mute that does not need the muted member to do something, which is
// what makes a mute safe to use. Without it, "muted" and "unreachable" are the same word.
func liftMutes(ctx context.Context, tx *sql.Tx, channelID int64, thread, author, body string) error {
	if err := setMuted(ctx, tx, channelID, author, thread, false); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT member FROM cursors WHERE channel_id = ? AND thread = ? AND muted = 1`, channelID, thread)
	if err != nil {
		return fmt.Errorf("find who muted %q: %w", thread, err)
	}
	defer rows.Close()
	var named []string
	for rows.Next() {
		var member string
		if err := rows.Scan(&member); err != nil {
			return fmt.Errorf("find who muted %q: %w", thread, err)
		}
		if mentions(body, member) {
			named = append(named, member)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("find who muted %q: %w", thread, err)
	}
	for _, member := range named {
		if err := setMuted(ctx, tx, channelID, member, thread, false); err != nil {
			return err
		}
	}
	return nil
}

// mentions reports whether body names this member. Matched on a word boundary rather than as a substring,
// because a member name is often a prefix of another: "claude" must not read as naming "claude-3fb7e7b0", and
// a name is written with an @ in front of it as often as not.
//
// Case insensitive, since a member writing about "Alice" means alice.
func mentions(body, member string) bool {
	if member == "" {
		return false
	}
	lowerBody, lowerMember := strings.ToLower(body), strings.ToLower(member)
	for at := 0; ; {
		i := strings.Index(lowerBody[at:], lowerMember)
		if i < 0 {
			return false
		}
		start := at + i
		end := start + len(lowerMember)
		if !namePart(lowerBody, start-1) && !namePart(lowerBody, end) {
			return true
		}
		at = start + 1
	}
}

// namePart reports whether the byte at i could be part of a member name, so a match with one on either side is
// a longer name rather than this one. The dash is in the set because every agent name has one.
func namePart(s string, i int) bool {
	if i < 0 || i >= len(s) {
		return false
	}
	c := s[i]
	return c == '-' || c == '_' || c == '.' ||
		('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || ('0' <= c && c <= '9')
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

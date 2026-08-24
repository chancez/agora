package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Doorbell is what should wake this member, and it records that they were woken.
//
// The gap it closes: everything else agora puts in front of an agent rides on that agent doing something,
// so a session sitting at a prompt reads nothing and a thread addressed to it waits for its user to type.
//
// Addressed rather than unread, because a wake costs a turn. A message rings only if it names this member,
// or lands in a thread this member has posted in or holds the claim on. Waking everybody on every post
// would spend ten turns on one finding, which is a chat room rather than a ledger.
//
// The seq it records is a watermark, not a cursor: it says what this member has been woken for, never what
// it has read. Reading is the agent's own decision, and a doorbell that consumed unread would answer a
// message by hiding it.
func (s *Store) Doorbell(ctx context.Context, req DoorbellRequest) (DoorbellResult, error) {
	if req.Channel == "" {
		return DoorbellResult{}, ErrNoChannel
	}
	if req.Member == "" {
		return DoorbellResult{}, ErrNoMember
	}
	result := DoorbellResult{Channel: req.Channel, Member: req.Member, Messages: []Message{}, Waiter: req.Waiter}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		// Looked up rather than created. This runs at the end of every turn, and a channel brought into
		// existence by a doorbell is a channel nobody chose to have.
		channelID, ok, err := lookupChannelID(ctx, tx, req.Channel)
		if err != nil || !ok {
			return err
		}
		rang, waiter, err := doorbellState(ctx, tx, channelID, req.Member)
		if err != nil {
			return err
		}
		result.Rang = rang

		if req.Waiter != "" && waiter > req.Waiter {
			// A doorbell started later is waiting on this member already, so this one is the leftover of an
			// earlier turn and says nothing: the newer one covers the same messages, and both ringing would
			// wake one session twice. Reported rather than silently ignored, since the caller's move is to
			// stop.
			result.Waiter = waiter
			return nil
		}
		// Taking the wait is a write, and the write is the point: data_version is what tells the doorbell
		// above that it has been taken over, so a wait nobody can see is a wait nobody can hand on.
		//
		// Only when it changes, so a doorbell rechecking every commit is not also writing every commit.
		// Measured: sqlite does not move data_version for an UPDATE that stores the value already there,
		// so the write storm this looks like it is avoiding was never possible either way.
		if req.Waiter != "" && req.Waiter != waiter && !req.DryRun {
			if err := setWaiter(ctx, tx, channelID, req.Member, req.Waiter); err != nil {
				return err
			}
		}

		addressed, err := addressedThreads(ctx, tx, channelID, req.Member)
		if err != nil {
			return err
		}
		candidates, err := unreadAfter(ctx, tx, req.Channel, channelID, req.Member, rang)
		if err != nil {
			return err
		}
		for _, msg := range candidates {
			// Named, or in a thread this member is already part of. Both, rather than a mention alone: an
			// answer in the thread you opened is addressed to you whether or not it spells your name, and
			// that is the case the doorbell exists for.
			if addressed[msg.Thread] || mentions(msg.Body, req.Member) {
				result.Messages = append(result.Messages, msg)
			}
		}
		if req.Limit > 0 && len(result.Messages) > req.Limit {
			result.Remaining = len(result.Messages) - req.Limit
			result.Messages = result.Messages[:req.Limit]
		}
		if req.DryRun || len(result.Messages) == 0 {
			return nil
		}
		// To the last message this call is handing over, never past it, so a ring bounded by Limit leaves
		// the rest to ring again. Messages that were not addressed to this member and sit below that seq
		// never ring: joining their thread later does not make an old message news.
		result.Rang = result.Messages[len(result.Messages)-1].Seq
		return setRang(ctx, tx, channelID, req.Member, result.Rang, s.dbTime())
	})
	if err != nil {
		return DoorbellResult{}, err
	}
	return result, nil
}

// doorbellState is how far this member has been woken, and who is waiting on its doorbell now.
func doorbellState(ctx context.Context, q queryer, channelID int64, member string) (int64, string, error) {
	var (
		seq    int64
		waiter string
	)
	err := q.QueryRowContext(ctx,
		`SELECT seq, waiter FROM doorbells WHERE channel_id = ? AND member = ?`, channelID, member,
	).Scan(&seq, &waiter)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", nil
	}
	if err != nil {
		return 0, "", fmt.Errorf("read the doorbell for %q: %w", member, err)
	}
	return seq, waiter, nil
}

func setWaiter(ctx context.Context, tx *sql.Tx, channelID int64, member, waiter string) error {
	if _, err := tx.ExecContext(ctx, `
INSERT INTO doorbells(channel_id, member, waiter) VALUES(?, ?, ?)
ON CONFLICT(channel_id, member) DO UPDATE SET waiter = excluded.waiter`,
		channelID, member, waiter,
	); err != nil {
		return fmt.Errorf("wait on the doorbell for %q: %w", member, err)
	}
	return nil
}

// setRang moves the watermark, and only ever forward: two doorbells racing one message both read the
// watermark before either writes, and the loser must not pull it back to where the winner already rang.
func setRang(ctx context.Context, tx *sql.Tx, channelID int64, member string, seq, at int64) error {
	if _, err := tx.ExecContext(ctx, `
INSERT INTO doorbells(channel_id, member, seq, rang_at) VALUES(?, ?, ?, ?)
ON CONFLICT(channel_id, member) DO UPDATE SET
	seq     = max(doorbells.seq, excluded.seq),
	rang_at = excluded.rang_at`,
		channelID, member, seq, at,
	); err != nil {
		return fmt.Errorf("record ringing %q: %w", member, err)
	}
	return nil
}

// addressedThreads is every thread this member is part of: one it has posted in, or one it holds the claim
// on. A claim counts because taking work and then waiting for an answer about it is the state a doorbell is
// for, and the answer can arrive before the first post.
func addressedThreads(ctx context.Context, q queryer, channelID int64, member string) (map[string]bool, error) {
	rows, err := q.QueryContext(ctx, `
SELECT thread FROM messages WHERE channel_id = ? AND author = ?
UNION
SELECT thread FROM claims WHERE channel_id = ? AND holder = ?`,
		channelID, member, channelID, member)
	if err != nil {
		return nil, fmt.Errorf("find the threads %q is part of: %w", member, err)
	}
	defer rows.Close()
	threads := map[string]bool{}
	for rows.Next() {
		var thread string
		if err := rows.Scan(&thread); err != nil {
			return nil, fmt.Errorf("find the threads %q is part of: %w", member, err)
		}
		threads[thread] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("find the threads %q is part of: %w", member, err)
	}
	return threads, nil
}

// unreadAfter is what this member has not read that is also newer than its watermark, oldest first.
//
// Unread as well as un-rung, so a message the agent has already gone and read never wakes it, and muted as
// everywhere else: a thread this member dismissed does not get to interrupt it. A message that names the
// member lifts its own mute when it is posted, so being named still arrives.
func unreadAfter(ctx context.Context, q queryer, channel string, channelID int64, member string, rang int64) ([]Message, error) {
	rows, err := q.QueryContext(ctx, `
SELECT `+messageColumns+`
FROM messages m
LEFT JOIN cursors c ON c.channel_id = m.channel_id AND c.member = ? AND c.thread = m.thread
WHERE m.channel_id = ? AND m.author != ? AND m.seq > ?
  AND m.seq > coalesce(c.cursor, 0) AND coalesce(c.muted, 0) = 0
ORDER BY m.seq`,
		member, channelID, member, rang)
	if err != nil {
		return nil, fmt.Errorf("read what would wake %q in %q: %w", member, channel, err)
	}
	defer rows.Close()

	var messages []Message
	for rows.Next() {
		msg := Message{Channel: channel}
		var created int64
		if err := rows.Scan(&msg.Seq, &msg.Thread, &msg.Author, &msg.Body, &created, &msg.Number); err != nil {
			return nil, fmt.Errorf("read what would wake %q in %q: %w", member, channel, err)
		}
		msg.CreatedAt = fromDBTime(created)
		messages = append(messages, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read what would wake %q in %q: %w", member, channel, err)
	}
	return messages, nil
}

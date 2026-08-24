package store

import (
	"context"
	"database/sql"
	"fmt"
)

// unreadWhere is the definition of unread, written once so nothing can hold a second opinion: a message in
// this channel, from somebody else, newer than this member's cursor on the thread it is in, in a thread it has
// not muted. A thread with no cursor row has never been read, which is why the join is left and both the cursor
// and the mute coalesce to zero.
//
// Muted threads are excluded here rather than only where a briefing is printed, because these numbers are what
// says whether somebody is behind, and a member cannot be behind on what it has dismissed.
const unreadWhere = `
FROM messages msg
LEFT JOIN cursors c
  ON c.channel_id = msg.channel_id AND c.member = members.name AND c.thread = msg.thread
WHERE msg.channel_id = members.channel_id
  AND msg.author != members.name
  AND msg.seq > coalesce(c.cursor, 0)
  AND coalesce(c.muted, 0) = 0`

// memberColumns is shared by every member query. The two unread counts answer different questions: how many
// decisions are waiting is the thread count, and how much reading they add up to is the message count. Posts
// is the other direction, what this member has put into the channel rather than what it owes.
const memberColumns = `name, description, notify, worktree, joined_at, seen_at,
	(SELECT count(*) ` + unreadWhere + `),
	(SELECT count(DISTINCT msg.thread) ` + unreadWhere + `),
	(SELECT count(*) FROM messages WHERE channel_id = members.channel_id AND author = members.name)`

// queryer is the read half shared by *sql.DB and *sql.Tx, so a row loader can serve a plain query and
// a transaction without being written twice.
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Join adds a member to a channel, or updates one already there. It is idempotent: rejoining keeps every
// cursor, so an agent that re-runs join does not re-read the channel from the beginning.
func (s *Store) Join(ctx context.Context, req JoinRequest) (Member, error) {
	if req.Channel == "" {
		return Member{}, ErrNoChannel
	}
	if req.Member == "" {
		return Member{}, ErrNoMember
	}
	var m Member
	err := s.tx(ctx, func(tx *sql.Tx) error {
		channelID, err := s.channelID(ctx, tx, req.Channel)
		if err != nil {
			return err
		}
		now := s.dbTime()
		// COALESCE against the bound pointer is what makes the update partial: a nil Notify leaves the
		// recorded nudge command alone, where excluded.notify would overwrite it with the empty string
		// that the insert half of this statement had to supply.
		if _, err := tx.ExecContext(ctx, `
INSERT INTO members(channel_id, name, description, notify, worktree, joined_at, seen_at)
VALUES(?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(channel_id, name) DO UPDATE SET
	description = COALESCE(?, description),
	notify      = COALESCE(?, notify),
	worktree    = COALESCE(?, worktree),
	seen_at     = excluded.seen_at`,
			channelID, req.Member, orEmpty(req.Description), orEmpty(req.Notify), orEmpty(req.Worktree), now, now,
			req.Description, req.Notify, req.Worktree,
		); err != nil {
			return fmt.Errorf("join %q as %q: %w", req.Channel, req.Member, err)
		}
		m, err = loadMember(ctx, tx, req.Channel, channelID, req.Member)
		return err
	})
	if err != nil {
		return Member{}, err
	}
	return m, nil
}

// Members lists a channel's participants, ordered by name. An unused channel has none, which is not an
// error.
func (s *Store) Members(ctx context.Context, channel string) ([]Member, error) {
	if channel == "" {
		return nil, ErrNoChannel
	}
	channelID, ok, err := s.lookupChannelID(ctx, channel)
	if err != nil || !ok {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+memberColumns+` FROM members WHERE channel_id = ? ORDER BY name`, channelID)
	if err != nil {
		return nil, fmt.Errorf("list members of %q: %w", channel, err)
	}
	defer rows.Close()

	var members []Member
	for rows.Next() {
		m, err := scanMember(rows, channel)
		if err != nil {
			return nil, fmt.Errorf("list members of %q: %w", channel, err)
		}
		members = append(members, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list members of %q: %w", channel, err)
	}
	return members, nil
}

// Leave takes a member off a roster, and with Force releases its claims. The counterpart to upsertMember: a
// member that is gone but still listed reads as one falling behind, and its claim as work in progress.
//
// Cursors stay. Deleting them cost a real channel four messages read and answered eleven hours earlier, because
// SessionEnd fires with reason "resume" when a session is only switched away from. The reason for deleting them
// was wrong anyway: a name is a session id, so no other session can reuse it, and the names that are reused (a
// person's $USER, an $AGORA_MEMBER role) want the read state carried over.
func (s *Store) Leave(ctx context.Context, req LeaveRequest) (LeaveResult, error) {
	if req.Channel == "" {
		return LeaveResult{}, ErrNoChannel
	}
	if req.Member == "" {
		return LeaveResult{}, ErrNoMember
	}
	result := LeaveResult{Channel: req.Channel, Member: req.Member, Claims: []string{}}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		channelID, ok, err := lookupChannelID(ctx, tx, req.Channel)
		if err != nil || !ok {
			return err
		}
		var present int64
		if err := tx.QueryRowContext(ctx,
			`SELECT count(*) FROM members WHERE channel_id = ? AND name = ?`, channelID, req.Member,
		).Scan(&present); err != nil {
			return fmt.Errorf("look for member %q: %w", req.Member, err)
		}
		if err := tx.QueryRowContext(ctx,
			`SELECT count(*) FROM cursors WHERE channel_id = ? AND member = ?`, channelID, req.Member,
		).Scan(&result.Cursors); err != nil {
			return fmt.Errorf("count %q's cursors: %w", req.Member, err)
		}
		threads, err := heldThreads(ctx, tx, channelID, req.Member)
		if err != nil {
			return err
		}
		result.Claims = threads
		// A member with no row can still hold a claim, from a name that was cleaned up before its work was.
		if present == 0 && len(threads) == 0 {
			return nil
		}
		if req.DryRun {
			return nil
		}
		if len(threads) > 0 && !req.Force {
			return nil
		}
		for _, stmt := range []string{
			`DELETE FROM claims WHERE channel_id = ? AND holder = ?`,
			`DELETE FROM members WHERE channel_id = ? AND name = ?`,
		} {
			if _, err := tx.ExecContext(ctx, stmt, channelID, req.Member); err != nil {
				return fmt.Errorf("remove member %q: %w", req.Member, err)
			}
		}
		result.Left = true
		return nil
	})
	if err != nil {
		return LeaveResult{}, err
	}
	return result, nil
}

// heldThreads is every thread this member holds a claim on.
func heldThreads(ctx context.Context, q queryer, channelID int64, member string) ([]string, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT thread FROM claims WHERE channel_id = ? AND holder = ? ORDER BY thread`, channelID, member)
	if err != nil {
		return nil, fmt.Errorf("list %q's claims: %w", member, err)
	}
	defer rows.Close()
	threads := []string{}
	for rows.Next() {
		var thread string
		if err := rows.Scan(&thread); err != nil {
			return nil, fmt.Errorf("list %q's claims: %w", member, err)
		}
		threads = append(threads, thread)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list %q's claims: %w", member, err)
	}
	return threads, nil
}

// Channels lists every channel in the database with the numbers a reader picks between them by. This is
// what a view showing several projects at once needs, and it is the only method that crosses channels.
func (s *Store) Channels(ctx context.Context, member string) ([]Channel, error) {
	if member == "" {
		return nil, ErrNoMember
	}
	// The thread count comes from messages and claims both, for the same reason the thread list does: a
	// claimed thread with nothing posted in it yet is still a thread somebody needs to know about.
	rows, err := s.db.QueryContext(ctx, `
SELECT ch.id, ch.key, ch.name, ch.created_at,
       (SELECT count(*) FROM (SELECT thread FROM messages WHERE channel_id = ch.id
                              UNION
                              SELECT thread FROM claims WHERE channel_id = ch.id)),
       (SELECT count(DISTINCT msg.thread)
          FROM messages msg
          LEFT JOIN cursors c ON c.channel_id = msg.channel_id AND c.member = ? AND c.thread = msg.thread
         WHERE msg.channel_id = ch.id AND msg.author != ? AND msg.seq > coalesce(c.cursor, 0)
           AND coalesce(c.muted, 0) = 0)
FROM channels ch
ORDER BY ch.key`, member, member)
	if err != nil {
		return nil, fmt.Errorf("list channels: %w", err)
	}
	defer rows.Close()

	var channels []Channel
	for rows.Next() {
		var (
			ch      Channel
			created int64
		)
		if err := rows.Scan(&ch.ID, &ch.Key, &ch.Name, &created, &ch.Threads, &ch.UnreadThreads); err != nil {
			return nil, fmt.Errorf("list channels: %w", err)
		}
		ch.CreatedAt = fromDBTime(created)
		channels = append(channels, ch)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list channels: %w", err)
	}
	return channels, nil
}

// upsertMember records a member without disturbing an existing one, so acting in a channel is what joins it.
// Requiring join first would mean a roster missing whoever forgot, and an agent's first contact is usually a
// hook reading the channel, where a failure goes into the model's context rather than onto a screen.
//
// It doubles as the heartbeat: an action is the best evidence a participant is alive.
func (s *Store) upsertMember(ctx context.Context, tx *sql.Tx, channelID int64, name string, worktree *string) error {
	now := s.dbTime()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO members(channel_id, name, worktree, joined_at, seen_at)
VALUES(?, ?, ?, ?, ?)
ON CONFLICT(channel_id, name) DO UPDATE SET
	worktree = COALESCE(?, worktree),
	seen_at  = excluded.seen_at`,
		channelID, name, orEmpty(worktree), now, now, worktree,
	); err != nil {
		return fmt.Errorf("record member %q: %w", name, err)
	}
	return nil
}

// setCursor moves a member's cursor on one thread.
//
// It never moves backwards. Two calls can race, and the loser carrying an older id would otherwise hand the
// member messages they have already read, which teaches an agent that acknowledging does not work.
func (s *Store) setCursor(ctx context.Context, tx *sql.Tx, channelID int64, member, thread string, cursor int64) error {
	if _, err := tx.ExecContext(ctx, `
INSERT INTO cursors(channel_id, member, thread, cursor)
VALUES(?, ?, ?, ?)
ON CONFLICT(channel_id, member, thread) DO UPDATE SET
	cursor = max(excluded.cursor, cursors.cursor)`,
		channelID, member, thread, cursor,
	); err != nil {
		return fmt.Errorf("move %q's cursor on %q: %w", member, thread, err)
	}
	return nil
}

// cursorFor is where a member's cursor stands on one thread, zero meaning they have read none of it.
func cursorFor(ctx context.Context, q queryer, channelID int64, member, thread string) (int64, error) {
	var cursor int64
	err := q.QueryRowContext(ctx,
		`SELECT coalesce(max(cursor), 0) FROM cursors WHERE channel_id = ? AND member = ? AND thread = ?`,
		channelID, member, thread,
	).Scan(&cursor)
	if err != nil {
		return 0, fmt.Errorf("read %q's cursor on %q: %w", member, thread, err)
	}
	return cursor, nil
}

func loadMember(ctx context.Context, q queryer, channel string, channelID int64, name string) (Member, error) {
	row := q.QueryRowContext(ctx,
		`SELECT `+memberColumns+` FROM members WHERE channel_id = ? AND name = ?`, channelID, name)
	m, err := scanMember(row, channel)
	if err != nil {
		return Member{}, fmt.Errorf("read member %q of %q: %w", name, channel, err)
	}
	return m, nil
}

// scanner is the shared surface of *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanMember(sc scanner, channel string) (Member, error) {
	m := Member{Channel: channel}
	var joined, seen int64
	if err := sc.Scan(&m.Name, &m.Description, &m.Notify, &m.Worktree, &joined, &seen,
		&m.Unread, &m.UnreadThreads, &m.Posts); err != nil {
		return Member{}, err
	}
	m.JoinedAt = fromDBTime(joined)
	m.SeenAt = fromDBTime(seen)
	return m, nil
}

func orEmpty(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// readCursors collects every thread this member has a cursor on, so a caller can see where it stands
// without having to ask thread by thread.
func readCursors(ctx context.Context, q queryer, channelID int64, member string, into map[string]int64) error {
	rows, err := q.QueryContext(ctx,
		`SELECT thread, cursor FROM cursors WHERE channel_id = ? AND member = ?`, channelID, member)
	if err != nil {
		return fmt.Errorf("read %q's cursors: %w", member, err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			thread string
			cursor int64
		)
		if err := rows.Scan(&thread, &cursor); err != nil {
			return fmt.Errorf("read %q's cursors: %w", member, err)
		}
		into[thread] = cursor
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read %q's cursors: %w", member, err)
	}
	return nil
}

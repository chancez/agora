package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// messageColumns is the select list for every message query, so none can return a message without both its
// numbers. The place in the thread is counted per row rather than as a window over the table, which is what
// lets it survive any filter, order, or limit.
const messageColumns = `m.seq, m.thread, m.author, m.body, m.created_at,
	(SELECT count(*) FROM messages e
	  WHERE e.channel_id = m.channel_id AND e.thread = m.thread AND e.seq <= m.seq)`

// Post writes one message and returns it with the id the database assigned.
func (s *Store) Post(ctx context.Context, req PostRequest) (Message, error) {
	if req.Channel == "" {
		return Message{}, ErrNoChannel
	}
	if req.Thread == "" {
		return Message{}, ErrNoThread
	}
	if req.Author == "" {
		return Message{}, ErrNoAuthor
	}
	if req.Body == "" {
		return Message{}, ErrNoBody
	}
	msg := Message{
		Channel:   req.Channel,
		Thread:    req.Thread,
		Author:    req.Author,
		Body:      req.Body,
		CreatedAt: s.now().UTC(),
	}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		channelID, err := s.channelID(ctx, tx, req.Channel)
		if err != nil {
			return err
		}
		// The next number in the channel, read and written under the write lock every transaction here takes,
		// so two posts racing cannot land on one.
		if err := tx.QueryRowContext(ctx,
			`SELECT coalesce(max(seq), 0) + 1 FROM messages WHERE channel_id = ?`, channelID,
		).Scan(&msg.Seq); err != nil {
			return fmt.Errorf("post to %q in %q: %w", req.Thread, req.Channel, err)
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO messages(channel_id, seq, thread, author, body, created_at)
VALUES(?, ?, ?, ?, ?, ?)`,
			channelID, msg.Seq, req.Thread, req.Author, req.Body, msg.CreatedAt.UnixNano(),
		); err != nil {
			return fmt.Errorf("post to %q in %q: %w", req.Thread, req.Channel, err)
		}
		// The newest in its thread, so its place there is the thread's length.
		if err := tx.QueryRowContext(ctx,
			`SELECT count(*) FROM messages WHERE channel_id = ? AND thread = ?`, channelID, req.Thread,
		).Scan(&msg.Number); err != nil {
			return fmt.Errorf("post to %q in %q: %w", req.Thread, req.Channel, err)
		}
		// A post is how a muted thread comes back: for the author, who is speaking in it, and for anybody the
		// message names, who is being spoken to.
		if err := liftMutes(ctx, tx, channelID, req.Thread, req.Author, req.Body); err != nil {
			return err
		}
		// Posting puts you in the roster, so a message never has an author nobody can look up.
		return s.upsertMember(ctx, tx, channelID, req.Author, req.Worktree)
	})
	if err != nil {
		return Message{}, err
	}
	return msg, nil
}

// Threads is the index a notification carries: per thread, how much is unread and the oldest unread message,
// which is enough to triage without reading anything.
//
// Detail can be hidden; the *existence* of a thread cannot. An agent that cannot see a discussion is how the
// duplicate work comes back.
func (s *Store) Threads(ctx context.Context, req ThreadsRequest) ([]Thread, error) {
	if req.Channel == "" {
		return nil, ErrNoChannel
	}
	if req.Member == "" {
		return nil, ErrNoMember
	}

	var threads []Thread
	err := s.tx(ctx, func(tx *sql.Tx) error {
		channelID, err := s.channelID(ctx, tx, req.Channel)
		if err != nil {
			return err
		}
		if !req.Observe {
			if err := s.upsertMember(ctx, tx, channelID, req.Member, req.Worktree); err != nil {
				return err
			}
		}
		threads, err = threadRows(ctx, tx, req, channelID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return threads, nil
}

// threadRows is one query for the list, plus one per thread for its first unread message.
//
// Thread names come from messages and claims both, because claiming a thread and posting to it are two acts
// and the gap between them is exactly when somebody else needs to know the work is taken.
func threadRows(ctx context.Context, q queryer, req ThreadsRequest, channelID int64) ([]Thread, error) {
	query := `
SELECT n.thread,
       count(m.seq),
       coalesce(max(m.created_at), max(cl.created_at), 0),
       coalesce(sum(CASE WHEN m.author != ? AND m.seq > coalesce(c.cursor, 0) THEN 1 ELSE 0 END), 0),
       coalesce(min(CASE WHEN m.author != ? AND m.seq > coalesce(c.cursor, 0) THEN m.seq END), 0),
       coalesce(max(cl.holder), ''),
       coalesce(max(c.muted), 0)
FROM (SELECT thread FROM messages WHERE channel_id = ?
      UNION
      SELECT thread FROM claims WHERE channel_id = ?) n
LEFT JOIN messages m ON m.channel_id = ? AND m.thread = n.thread
LEFT JOIN cursors c ON c.channel_id = ? AND c.member = ? AND c.thread = n.thread
LEFT JOIN claims cl ON cl.channel_id = ? AND cl.thread = n.thread
GROUP BY n.thread`
	// Bound in the order the placeholders appear in the text above, which is why they are listed out
	// rather than named: the select list binds before the joins do.
	args := []any{req.Member, req.Member, channelID, channelID, channelID, channelID, req.Member, channelID}
	// Unread is counted the same whatever the filter, muted or not, so a muted thread still reports how much
	// has piled up in it. What the filter decides is which threads come back at all.
	//
	// Collected rather than appended to the query in place: Author combines with either filter, and two
	// HAVING keywords is a syntax error. Placeholders still bind in the order the clauses are written.
	var having []string
	switch req.Filter {
	case UnreadThreads:
		having = append(having, `sum(CASE WHEN m.author != ? AND m.seq > coalesce(c.cursor, 0) THEN 1 ELSE 0 END) > 0
   AND coalesce(max(c.muted), 0) = 0`)
		args = append(args, req.Member)
	case MutedThreads:
		having = append(having, `coalesce(max(c.muted), 0) = 1`)
	}
	if req.Author != "" {
		having = append(having, `sum(CASE WHEN m.author = ? THEN 1 ELSE 0 END) > 0`)
		args = append(args, req.Author)
	}
	if len(having) > 0 {
		query += "\nHAVING " + strings.Join(having, "\n   AND ")
	}
	// Most recently active first, by the third column, since that is the order attention goes in.
	query += `
ORDER BY 3 DESC, n.thread`
	if req.Limit > 0 {
		query += `
LIMIT ?`
		args = append(args, req.Limit)
	}

	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list threads in %q: %w", req.Channel, err)
	}
	defer rows.Close()

	var (
		threads   []Thread
		firstUnre []int64
	)
	for rows.Next() {
		thread := Thread{Channel: req.Channel}
		var lastAt, first int64
		if err := rows.Scan(&thread.Name, &thread.Messages, &lastAt, &thread.Unread, &first, &thread.Claim,
			&thread.Muted); err != nil {
			return nil, fmt.Errorf("list threads in %q: %w", req.Channel, err)
		}
		thread.LastAt = fromDBTime(lastAt)
		threads = append(threads, thread)
		firstUnre = append(firstUnre, first)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list threads in %q: %w", req.Channel, err)
	}

	for i := range threads {
		if firstUnre[i] == 0 {
			continue
		}
		msg, err := messageBySeq(ctx, q, req.Channel, channelID, firstUnre[i])
		if err != nil {
			return nil, err
		}
		threads[i].First = &msg
	}
	return threads, nil
}

func messageBySeq(ctx context.Context, q queryer, channel string, channelID, seq int64) (Message, error) {
	msg := Message{Channel: channel}
	var created int64
	err := q.QueryRowContext(ctx,
		`SELECT `+messageColumns+` FROM messages m WHERE m.channel_id = ? AND m.seq = ?`, channelID, seq,
	).Scan(&msg.Seq, &msg.Thread, &msg.Author, &msg.Body, &created, &msg.Number)
	if err != nil {
		return Message{}, fmt.Errorf("read message %d of %q: %w", seq, channel, err)
	}
	msg.CreatedAt = fromDBTime(created)
	return msg, nil
}

// Read returns a member's unread messages, and advances their cursors only if asked.
//
// The select and the advance share one transaction, so a message posted between them is left unread rather
// than skipped. Cursors are per thread, so reading one thread leaves every other thread's unread alone.
func (s *Store) Read(ctx context.Context, req ReadRequest) (ReadResult, error) {
	if req.Channel == "" {
		return ReadResult{}, ErrNoChannel
	}
	if req.Member == "" {
		return ReadResult{}, ErrNoMember
	}
	result := ReadResult{
		Channel: req.Channel,
		Member:  req.Member,
		Thread:  req.Thread,
		Cursors: map[string]int64{},
	}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		channelID, err := s.channelID(ctx, tx, req.Channel)
		if err != nil {
			return err
		}
		if err := s.upsertMember(ctx, tx, channelID, req.Member, req.Worktree); err != nil {
			return err
		}
		if result.Messages, err = unreadMessages(ctx, tx, req, channelID, req.Limit); err != nil {
			return err
		}
		// Which threads this call concerns: the ones it delivered from, plus a named one even when it
		// turned out empty, so a caller reading one thread always learns where its cursor stands.
		last := map[string]int64{}
		for _, msg := range result.Messages {
			last[msg.Thread] = msg.Seq
		}
		if _, ok := last[req.Thread]; req.Thread != "" && !ok {
			last[req.Thread] = 0
		}
		// Counted before anything advances. Counting afterwards reports nothing left behind, because the
		// advance has already moved past the messages this call is asking about.
		if req.Limit > 0 && len(result.Messages) == req.Limit {
			total, err := unreadCount(ctx, tx, req, channelID)
			if err != nil {
				return err
			}
			if remaining := total - len(result.Messages); remaining > 0 {
				result.Remaining = remaining
			}
		}
		if req.Advance {
			for thread, id := range last {
				if id == 0 {
					continue
				}
				// To the last message actually handed over, never past it, so a truncated read leaves
				// the rest unread.
				if err := s.setCursor(ctx, tx, channelID, req.Member, thread, id); err != nil {
					return err
				}
			}
		}
		// Every thread this member stands anywhere on, not only the ones this call delivered from. A read
		// that finds nothing new would otherwise report no cursors at all, which reads as "you have read
		// nothing" rather than "there is nothing new".
		if err := readCursors(ctx, tx, channelID, req.Member, result.Cursors); err != nil {
			return err
		}
		for thread := range last {
			cursor, err := cursorFor(ctx, tx, channelID, req.Member, thread)
			if err != nil {
				return err
			}
			result.Cursors[thread] = cursor
		}
		return nil
	})
	if err != nil {
		return ReadResult{}, err
	}
	return result, nil
}

// Ack marks threads read without reading them. Deciding a thread is not yours is a decision, and it has to
// be recorded somewhere, or the same index arrives every turn until somebody reads what they already judged
// irrelevant.
func (s *Store) Ack(ctx context.Context, req AckRequest) (AckResult, error) {
	if req.Channel == "" {
		return AckResult{}, ErrNoChannel
	}
	if req.Member == "" {
		return AckResult{}, ErrNoMember
	}
	result := AckResult{Channel: req.Channel, Member: req.Member, Cursors: map[string]int64{}}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		channelID, err := s.channelID(ctx, tx, req.Channel)
		if err != nil {
			return err
		}
		if err := s.upsertMember(ctx, tx, channelID, req.Member, req.Worktree); err != nil {
			return err
		}
		// Only threads with something unread, so acknowledging reports what it changed rather than every
		// thread that has ever existed.
		// A muted thread is not in the inbox, so acknowledging everything leaves it alone: the count piling up
		// in it is what the briefing line reports, and clearing it would make a mute look like a deletion.
		// Naming one still acknowledges it, the same way reading one does, which is why that case asks for the
		// whole index and checks the unread itself.
		filter := ThreadsRequest{Channel: req.Channel, Member: req.Member, Filter: UnreadThreads}
		if req.Thread != "" {
			filter.Filter = AllThreads
		}
		threads, err := threadRows(ctx, tx, filter, channelID)
		if err != nil {
			return err
		}
		for _, thread := range threads {
			if req.Thread != "" && thread.Name != req.Thread {
				continue
			}
			if thread.Unread == 0 {
				continue
			}
			newest, err := newestInThread(ctx, tx, channelID, thread.Name)
			if err != nil {
				return err
			}
			if err := s.setCursor(ctx, tx, channelID, req.Member, thread.Name, newest); err != nil {
				return err
			}
			result.Threads = append(result.Threads, thread.Name)
			result.Cursors[thread.Name] = newest
		}
		// Acknowledging a thread with nothing waiting is not an error, and reporting where its cursor
		// stands beats saying nothing.
		if req.Thread != "" && len(result.Threads) == 0 {
			cursor, err := cursorFor(ctx, tx, channelID, req.Member, req.Thread)
			if err != nil {
				return err
			}
			result.Cursors[req.Thread] = cursor
		}
		return nil
	})
	if err != nil {
		return AckResult{}, err
	}
	return result, nil
}

// unreadMessages is what a member has not read, oldest first, because unread is read as a narrative.
func unreadMessages(ctx context.Context, q queryer, req ReadRequest, channelID int64, limit int) ([]Message, error) {
	query := `
SELECT ` + messageColumns + `
FROM messages m
LEFT JOIN cursors c ON c.channel_id = m.channel_id AND c.member = ? AND c.thread = m.thread
WHERE m.channel_id = ? AND m.author != ? AND m.seq > coalesce(c.cursor, 0)`
	args := []any{req.Member, channelID, req.Member}
	if req.Thread != "" {
		query += ` AND m.thread = ?`
		args = append(args, req.Thread)
	} else {
		// Naming a thread beats the filter: reading a muted thread is how you look at one on purpose. Without
		// a name this is the inbox, and a muted thread is not in it.
		query += ` AND coalesce(c.muted, 0) = 0`
	}
	query += ` ORDER BY m.seq`
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("read unread in %q: %w", req.Channel, err)
	}
	defer rows.Close()

	var messages []Message
	for rows.Next() {
		msg := Message{Channel: req.Channel}
		var created int64
		if err := rows.Scan(&msg.Seq, &msg.Thread, &msg.Author, &msg.Body, &created, &msg.Number); err != nil {
			return nil, fmt.Errorf("read unread in %q: %w", req.Channel, err)
		}
		msg.CreatedAt = fromDBTime(created)
		messages = append(messages, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read unread in %q: %w", req.Channel, err)
	}
	return messages, nil
}

func unreadCount(ctx context.Context, q queryer, req ReadRequest, channelID int64) (int, error) {
	query := `
SELECT count(*)
FROM messages m
LEFT JOIN cursors c ON c.channel_id = m.channel_id AND c.member = ? AND c.thread = m.thread
WHERE m.channel_id = ? AND m.author != ? AND m.seq > coalesce(c.cursor, 0)`
	args := []any{req.Member, channelID, req.Member}
	if req.Thread != "" {
		query += ` AND m.thread = ?`
		args = append(args, req.Thread)
	} else {
		// The same rule unreadMessages follows, or a truncated read reports messages left behind that it was
		// never going to deliver.
		query += ` AND coalesce(c.muted, 0) = 0`
	}
	var count int
	if err := q.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("count unread in %q: %w", req.Channel, err)
	}
	return count, nil
}

// newestInThread is the thread's last message as a cursor can hold it, which is its place in the channel. The
// rowid is not interchangeable with it: rowids are global, so in a database with a second channel this returned
// a number past the end of the thread and acknowledging marked the next message read before it existed.
func newestInThread(ctx context.Context, q queryer, channelID int64, thread string) (int64, error) {
	var newest int64
	if err := q.QueryRowContext(ctx,
		`SELECT coalesce(max(seq), 0) FROM messages WHERE channel_id = ? AND thread = ?`, channelID, thread,
	).Scan(&newest); err != nil {
		return 0, fmt.Errorf("find the newest message in %q: %w", thread, err)
	}
	return newest, nil
}

// Messages reads a channel's history directly, ignoring every cursor. This is what agora dump shows, and it
// never moves anything.
func (s *Store) Messages(ctx context.Context, req MessagesRequest) ([]Message, error) {
	if req.Channel == "" {
		return nil, ErrNoChannel
	}
	channelID, ok, err := s.lookupChannelID(ctx, req.Channel)
	if err != nil || !ok {
		return nil, err
	}
	return queryMessages(ctx, s.db, req.Channel, channelID, messageFilter{
		thread: req.Thread,
		after:  req.After,
		limit:  req.Limit,
		// A truncated history is more useful from the recent end, so the limit keeps the newest.
		newestFirst: true,
	})
}

type messageFilter struct {
	thread string
	after  int64
	limit  int
	// excludeAuthor drops one member's own messages. Unread means "posted by somebody else since I last
	// looked": an author has read what they wrote, so counting it would inject a member's own findings back
	// at them and report them as behind on their own work.
	excludeAuthor string
	newestFirst   bool
}

// queryMessages always returns messages oldest first. newestFirst decides which end a limit keeps, not the
// order they come back in, because a caller that has to reverse a slice will sometimes forget.
func queryMessages(ctx context.Context, q queryer, channel string, channelID int64, f messageFilter) ([]Message, error) {
	query := `SELECT ` + messageColumns + ` FROM messages m WHERE m.channel_id = ? AND m.seq > ?`
	args := []any{channelID, f.after}
	if f.thread != "" {
		query += ` AND m.thread = ?`
		args = append(args, f.thread)
	}
	if f.excludeAuthor != "" {
		query += ` AND m.author != ?`
		args = append(args, f.excludeAuthor)
	}
	if f.newestFirst {
		query += ` ORDER BY m.seq DESC`
	} else {
		query += ` ORDER BY m.seq`
	}
	if f.limit > 0 {
		query += ` LIMIT ?`
		args = append(args, f.limit)
	}

	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("read messages from %q: %w", channel, err)
	}
	defer rows.Close()

	var messages []Message
	for rows.Next() {
		msg := Message{Channel: channel}
		var created int64
		if err := rows.Scan(&msg.Seq, &msg.Thread, &msg.Author, &msg.Body, &created, &msg.Number); err != nil {
			return nil, fmt.Errorf("read messages from %q: %w", channel, err)
		}
		msg.CreatedAt = fromDBTime(created)
		messages = append(messages, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read messages from %q: %w", channel, err)
	}
	if f.newestFirst {
		reverse(messages)
	}
	return messages, nil
}

func reverse(messages []Message) {
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}
}

// Delete removes a thread and everything about it: the only operation that shortens the record. It exists
// because a mistaken finding misleads whoever reads it next.
//
// Another member's thread is refused unless forced. A release they can see and argue with; a deletion leaves
// them nothing to argue about.
func (s *Store) Delete(ctx context.Context, req DeleteRequest) (DeleteResult, error) {
	if req.Channel == "" {
		return DeleteResult{}, ErrNoChannel
	}
	if req.Thread == "" {
		return DeleteResult{}, ErrNoThread
	}
	if req.Member == "" {
		return DeleteResult{}, ErrNoMember
	}
	result := DeleteResult{Channel: req.Channel, Thread: req.Thread}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		channelID, ok, err := lookupChannelID(ctx, tx, req.Channel)
		if err != nil || !ok {
			return err
		}
		if err := tx.QueryRowContext(ctx,
			`SELECT count(*) FROM messages WHERE channel_id = ? AND thread = ?`, channelID, req.Thread,
		).Scan(&result.Messages); err != nil {
			return fmt.Errorf("count %q: %w", req.Thread, err)
		}
		if err := tx.QueryRowContext(ctx,
			`SELECT count(*) FROM cursors WHERE channel_id = ? AND thread = ?`, channelID, req.Thread,
		).Scan(&result.Cursors); err != nil {
			return fmt.Errorf("count cursors on %q: %w", req.Thread, err)
		}
		claim, held, err := loadClaim(ctx, tx, req.Channel, channelID, req.Thread)
		if err != nil {
			return err
		}
		if held {
			result.Claim = claim.Holder
		}
		if req.DryRun {
			return nil
		}
		if held && claim.Holder != req.Member && !req.Force {
			return nil
		}
		for _, stmt := range []string{
			`DELETE FROM messages WHERE channel_id = ? AND thread = ?`,
			`DELETE FROM cursors WHERE channel_id = ? AND thread = ?`,
			`DELETE FROM claims WHERE channel_id = ? AND thread = ?`,
		} {
			if _, err := tx.ExecContext(ctx, stmt, channelID, req.Thread); err != nil {
				return fmt.Errorf("delete %q: %w", req.Thread, err)
			}
		}
		result.Deleted = true
		// Recorded as an action like any other, so a roster still shows who was here. Deleting is not a way
		// to leave without trace.
		return s.upsertMember(ctx, tx, channelID, req.Member, req.Worktree)
	})
	if err != nil {
		return DeleteResult{}, err
	}
	return result, nil
}

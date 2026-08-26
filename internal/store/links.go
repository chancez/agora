package store

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
)

// One thread naming another is a link. Nothing new has to be typed for it: agents already write the pointer in
// prose once the standing rule asks them to, measured 3 of 3 as "tracking that separate fix in lex-empty-input",
// and the matcher that finds a member's name in a body finds a thread's the same way.
//
// Both directions are recorded, which is not symmetry for its own sake. In every measured run the pointer was
// posted *before* the thread it named existed, so a scan of only the threads present at post time would have
// found none of them.

// references reports whether body points at this thread.
//
// A bare name counts only when it carries a separator, because a one-word thread like "general" or "docs" turns
// up in ordinary prose, and a false link costs a reader a thread that has nothing to do with theirs. Every
// thread name an agent has been observed choosing is a slug: parser-empty-input, lex-empty-input,
// kitty-sandbox-mise. A leading # always counts, which is how a one-word thread gets referenced on purpose, and
// it works for a slug too since # is not part of a name.
func references(body, thread string) bool {
	if strings.ContainsAny(thread, "-_") {
		return named(body, thread, threadPart)
	}
	return named(body, "#"+thread, threadPart)
}

// threadPart is the boundary rule for a thread name, and it differs from a member's in one place: a full stop
// after a thread name ends a sentence rather than continuing the name.
//
// Found end to end rather than by a test, against the exact sentence agents were measured writing: "tracking that
// separate fix in lex-empty-input." matched nothing, because the member rule treats '.' as part of a name so that
// alice.dev is one name. Every reference in every measured run ended a sentence, so the rule that missed them
// missed all of them.
//
// A stop still continues a name when a name character follows it, which is what keeps lex-empty-input.go reading
// as a filename rather than as a pointer to the thread.
func threadPart(s string, i int) bool {
	if i < 0 || i >= len(s) {
		return false
	}
	if s[i] == '.' {
		return namePart(s, i+1)
	}
	return namePart(s, i)
}

// recordLinks writes the links a message creates, in both directions. opened says this message is the first in
// its thread, which is the only time the second scan is worth doing: a reference written after a thread exists is
// caught by the first scan when it is written, and one written before is caught here when the thread appears.
func recordLinks(ctx context.Context, tx *sql.Tx, channelID int64, thread, body string, seq int64, opened bool) error {
	names, err := threadNames(ctx, tx, channelID)
	if err != nil {
		return err
	}
	for _, name := range names {
		if name == thread || !references(body, name) {
			continue
		}
		if err := addLink(ctx, tx, channelID, thread, name, seq); err != nil {
			return err
		}
	}
	if !opened {
		return nil
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT seq, thread, body FROM messages WHERE channel_id = ? AND thread != ?`, channelID, thread)
	if err != nil {
		return fmt.Errorf("find what already named %q: %w", thread, err)
	}
	defer rows.Close()
	type ref struct {
		seq    int64
		thread string
	}
	var earlier []ref
	for rows.Next() {
		var (
			at   int64
			from string
			text string
		)
		if err := rows.Scan(&at, &from, &text); err != nil {
			return fmt.Errorf("find what already named %q: %w", thread, err)
		}
		if references(text, thread) {
			earlier = append(earlier, ref{seq: at, thread: from})
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("find what already named %q: %w", thread, err)
	}
	for _, r := range earlier {
		if err := addLink(ctx, tx, channelID, r.thread, thread, r.seq); err != nil {
			return err
		}
	}
	return nil
}

// addLink keeps the first reference between a pair rather than the newest, since what a reader wants is where
// the two threads were joined.
func addLink(ctx context.Context, tx *sql.Tx, channelID int64, from, to string, seq int64) error {
	if _, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO links(channel_id, from_thread, to_thread, seq) VALUES(?, ?, ?, ?)`,
		channelID, from, to, seq); err != nil {
		return fmt.Errorf("link %q to %q: %w", from, to, err)
	}
	return nil
}

// threadNames is every thread in the channel, from messages and claims both, for the same reason threadRows
// unions them: claiming a thread and posting to it are two acts, and a claim with no message in it is still a
// thread somebody can point at.
func threadNames(ctx context.Context, q queryer, channelID int64) ([]string, error) {
	rows, err := q.QueryContext(ctx, `
SELECT thread FROM messages WHERE channel_id = ?
UNION
SELECT thread FROM claims WHERE channel_id = ?`, channelID, channelID)
	if err != nil {
		return nil, fmt.Errorf("list thread names: %w", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("list thread names: %w", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list thread names: %w", err)
	}
	return names, nil
}

// linksByThread is every link in the channel as an undirected map, one query rather than one per thread.
// Undirected because finding the other half is the whole point: a thread that was named by another is as
// related to it as the one that did the naming.
func linksByThread(ctx context.Context, q queryer, channelID int64) (map[string][]string, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT from_thread, to_thread FROM links WHERE channel_id = ? ORDER BY seq`, channelID)
	if err != nil {
		return nil, fmt.Errorf("list links: %w", err)
	}
	defer rows.Close()
	related := map[string][]string{}
	add := func(a, b string) {
		for _, seen := range related[a] {
			if seen == b {
				return
			}
		}
		related[a] = append(related[a], b)
	}
	for rows.Next() {
		var from, to string
		if err := rows.Scan(&from, &to); err != nil {
			return nil, fmt.Errorf("list links: %w", err)
		}
		add(from, to)
		add(to, from)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list links: %w", err)
	}
	return related, nil
}

// relatedThreads is the other half of a split piece of work, as a reader of one thread sees it: the threads it
// names or is named by, with what is unread in each. Enough to decide whether to follow the pointer, which is
// the same standard the index itself is held to.
func relatedThreads(ctx context.Context, q queryer, req ReadRequest, channelID int64) ([]RelatedRead, error) {
	links, err := linksByThread(ctx, q, channelID)
	if err != nil {
		return nil, err
	}
	names := links[req.Thread]
	if len(names) == 0 {
		return nil, nil
	}
	all, err := threadRows(ctx, q, ThreadsRequest{
		Channel: req.Channel,
		Member:  req.Member,
		Filter:  AllThreads,
	}, channelID)
	if err != nil {
		return nil, err
	}
	byName := make(map[string]Thread, len(all))
	for _, thread := range all {
		byName[thread.Name] = thread
	}
	// In the order the references were made rather than by activity, since that is the order somebody reading
	// this thread met them.
	related := make([]RelatedRead, 0, len(names))
	for _, name := range names {
		thread, ok := byName[name]
		if !ok {
			continue
		}
		// The oldest unread message of a *related* thread is not what this call is delivering, and a briefing
		// that carried it twice over would spend the budget on the thread nobody asked for.
		thread.First = nil
		entry := RelatedRead{Thread: thread}
		if req.Related {
			msgs, omitted, err := lastInThread(ctx, q, req.Channel, channelID, name, relatedWindow)
			if err != nil {
				return nil, err
			}
			entry.Messages, entry.Omitted = msgs, omitted
		}
		related = append(related, entry)
	}
	return related, nil
}

// relatedWindow bounds what one related thread contributes. Bounded for the reason everything here is: hook
// output over 10000 characters is replaced by a preview, and a thread somebody has worked all day has no natural
// length. Newest kept, since a truncated history is more useful from the recent end.
const relatedWindow = 5

// lastInThread is the newest messages in a thread whatever this member has read, returned oldest first, with how
// many older ones were left out. Read state is deliberately not consulted: the case a link exists for is a thread
// this member has already read, and filtering to unread there returns nothing at all.
func lastInThread(ctx context.Context, q queryer, channel string, channelID int64, thread string, window int) ([]Message, int, error) {
	var total int
	if err := q.QueryRowContext(ctx,
		`SELECT count(*) FROM messages WHERE channel_id = ? AND thread = ?`, channelID, thread,
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count %q: %w", thread, err)
	}
	rows, err := q.QueryContext(ctx, `
SELECT `+messageColumns+`
FROM messages m WHERE m.channel_id = ? AND m.thread = ?
ORDER BY m.seq DESC
LIMIT ?`, channelID, thread, window)
	if err != nil {
		return nil, 0, fmt.Errorf("read %q: %w", thread, err)
	}
	defer rows.Close()
	var msgs []Message
	for rows.Next() {
		msg := Message{Channel: channel}
		var created int64
		if err := rows.Scan(&msg.Seq, &msg.Thread, &msg.Author, &msg.Body, &created, &msg.Number); err != nil {
			return nil, 0, fmt.Errorf("read %q: %w", thread, err)
		}
		msg.CreatedAt = fromDBTime(created)
		msgs = append(msgs, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("read %q: %w", thread, err)
	}
	// Newest first out of the database so the window keeps the recent end, oldest first for a reader.
	slices.Reverse(msgs)
	omitted := total - len(msgs)
	if omitted < 0 {
		omitted = 0
	}
	return msgs, omitted, nil
}

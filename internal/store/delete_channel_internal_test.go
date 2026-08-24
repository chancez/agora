package store

import (
	"path/filepath"
	"testing"
	"time"
)

// TestDeleteChannelLeavesNoRowsBehind is internal because the failure it guards against is invisible from
// outside: every read narrows by channel id, so a message whose channel is gone reads as no message at all.
//
// It matters anyway, because channels.id is a rowid and sqlite hands the next channel created an id a deleted
// one had. Rows left pointing at it would come back in an unrelated repository's channel, which is the worst
// shape a bug in agora can take: a finding turning up in a channel it was never posted to.
func TestDeleteChannelLeavesNoRowsBehind(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	s, err := Open(filepath.Join(t.TempDir(), "agora.db"), WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatalf("Open(): %v", err)
	}
	defer s.Close()
	ctx := t.Context()

	if _, err := s.Post(ctx, PostRequest{Channel: "repo", Thread: "parser-panic", Author: "alice", Body: "the token loop"}); err != nil {
		t.Fatalf("Post(): %v", err)
	}
	if _, err := s.Claim(ctx, ClaimRequest{Channel: "repo", Thread: "parser-panic", Holder: "alice"}); err != nil {
		t.Fatalf("Claim(): %v", err)
	}
	if _, err := s.Read(ctx, ReadRequest{Channel: "repo", Member: "bob", Advance: true}); err != nil {
		t.Fatalf("Read(): %v", err)
	}

	if _, err := s.DeleteChannel(ctx, DeleteChannelRequest{Channel: "repo", Force: true}); err != nil {
		t.Fatalf("DeleteChannel(): %v", err)
	}

	// Every table, not only the ones this method names, since it names none: the cascade is the mechanism.
	for _, table := range []string{"channels", "messages", "members", "cursors", "claims"} {
		var rows int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&rows); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if rows != 0 {
			t.Errorf("%s has %d rows after deleting the only channel, want none", table, rows)
		}
	}
}

package bus

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// migrations[v] upgrades a database at user_version v to v+1. Each step runs
// inside one immediate write transaction with foreign keys off (a table
// rebuild would otherwise cascade-delete embeddings), re-reads user_version
// under the write lock so two processes starting together apply it once,
// and stamps the new version before committing. The regular schema DDL
// (CREATE ... IF NOT EXISTS) runs after migrate and recreates whatever a
// rebuild dropped (indexes, triggers). An older binary opening the upgraded
// file refuses it (ADR 0003 A6); every running old-binary session must be
// restarted.
var migrations = map[int]func(tx *sql.Tx) error{
	1:  dropMessagesBytes,
	2:  addTables,                               // message_tags (ADR 0009)
	3:  addTables,                               // tag_subscriptions (ADR 0009)
	4:  splitTagSets,                            // tag_subscription_tags (#13)
	5:  addSubject,                              // messages.subject, FTS over subject and content (ADR 0010)
	6:  addMemoryAccessAndDropTaskSubscriptions, // memory_access; drop task-list subscriptions (ADR 0013)
	7:  addSessionHarness,                       // sessions.harness, sessions.harness_version (ADR 0015)
	8:  addSessionHarnessProcess,                // sessions.harness_pid, sessions.harness_start (ADR 0017)
	9:  addTagRanges,                            // tag_subscription_tags.lo, .hi (#20, ADR 0009 amendment 2026-10-06)
	10: foldRefs,                                // fold messages.refs into content and drop the column (ADR 0019)
}

// addTables is the step for a version that only adds tables: the schema DDL
// (CREATE ... IF NOT EXISTS) runs right after migrate and creates them, so
// the step itself has nothing to do beyond stamping the version.
func addTables(*sql.Tx) error { return nil }

// migrate brings db from user_version from up to schemaVersion.
func migrate(db *sql.DB, from int) error {
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
		return err
	}
	defer func() { _, _ = conn.ExecContext(ctx, "PRAGMA foreign_keys = ON") }()
	for v := from; v < schemaVersion; v++ {
		step, ok := migrations[v]
		if !ok {
			return fmt.Errorf("no migration from schema version %d", v)
		}
		if err := runMigration(ctx, conn, v, step); err != nil {
			return fmt.Errorf("migrate schema version %d to %d: %w", v, v+1, err)
		}
	}
	return nil
}

func runMigration(ctx context.Context, conn *sql.Conn, v int, step func(*sql.Tx) error) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var cur int
	if err := tx.QueryRow("PRAGMA user_version").Scan(&cur); err != nil {
		return err
	}
	if cur != v {
		return nil // another process got here first
	}
	if err := step(tx); err != nil {
		return err
	}
	var bad int
	if err := tx.QueryRow("SELECT count(*) FROM pragma_foreign_key_check").Scan(&bad); err != nil {
		return err
	}
	if bad != 0 {
		return fmt.Errorf("%d foreign key violation(s) after rebuild", bad)
	}
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", v+1)); err != nil {
		return err
	}
	return tx.Commit()
}

// splitTagSets (schema 4 -> 5, #13) creates tag_subscription_tags and
// backfills one row per tag from each existing tag_subscriptions.tags_key,
// so matching can join from these rows into message_tags(tag, seq) instead
// of scanning messages. message_tags_tag is dropped here: it can't
// range-scan by seq, and message_tags_tag_seq (created by the schema DDL
// that runs right after migrate) replaces it.
func splitTagSets(tx *sql.Tx) error {
	if _, err := tx.Exec(tagSubscriptionTagsDDL); err != nil {
		return err
	}
	// A real v4 database always has tag_subscriptions (schema v4's own DDL
	// created it). It's only absent here when a much older database jumps
	// straight to v5 in one Open() call, skipping v4 (or a test simulates
	// that); in that case there is nothing to back-fill, and the trailing
	// schema DDL creates the table fresh right after migrate returns, same
	// as it always has for a table dropped between opens.
	var exists int
	if err := tx.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='tag_subscriptions'").Scan(&exists); err != nil {
		return err
	}
	if exists == 1 {
		if _, err := tx.Exec(`WITH RECURSIVE split(sender, tags_key, tag, rest) AS (
			SELECT sender, tags_key, '', tags_key || ',' FROM tag_subscriptions
			UNION ALL
			SELECT sender, tags_key, substr(rest, 1, instr(rest, ',') - 1), substr(rest, instr(rest, ',') + 1) FROM split WHERE rest <> '')
			INSERT OR IGNORE INTO tag_subscription_tags(sender, tags_key, tag) SELECT sender, tags_key, tag FROM split WHERE tag <> ''`); err != nil {
			return err
		}
	}
	_, err := tx.Exec("DROP INDEX IF EXISTS message_tags_tag")
	return err
}

// addSubject (schema 5 -> 6, ADR 0010) adds messages.subject, backfills it
// on task-list rows from the task document's subject (the same channel
// predicate as IsTaskChannel), and rebuilds messages_fts and its triggers
// over subject and content; the rebuild indexes the backfilled subjects.
// The column check keeps the step safe on a file that already has the
// column (a test shaping an older version from a fresh file). json_valid
// guards the backfill: json_extract raises on malformed text, and an error
// here would leave the bus unopenable.
func addSubject(tx *sql.Tx) error {
	var has int
	if err := tx.QueryRow("SELECT count(*) FROM pragma_table_info('messages') WHERE name='subject'").Scan(&has); err != nil {
		return err
	}
	if has == 0 {
		if _, err := tx.Exec("ALTER TABLE messages ADD COLUMN subject TEXT NOT NULL DEFAULT ''"); err != nil {
			return err
		}
	}
	for _, s := range []string{
		`UPDATE messages SET subject = COALESCE(json_extract(content, '$.subject'), '')
		  WHERE (channel = 'tasks' OR channel LIKE 'tasks/%') AND json_valid(content)`,
		"DROP TRIGGER IF EXISTS messages_ai",
		"DROP TRIGGER IF EXISTS messages_ad",
		"DROP TABLE IF EXISTS messages_fts",
		messagesFTSDDL,
		"INSERT INTO messages_fts(messages_fts) VALUES('rebuild')",
	} {
		if _, err := tx.Exec(s); err != nil {
			return err
		}
	}
	return nil
}

// addMemoryAccessAndDropTaskSubscriptions (schema 6 -> 7, ADR 0013) creates
// memory_access, backfills one row per live memory (a message with a
// memory_id, not tombstoned, on a channel that is not a task list) at the
// migration time, deletes every subscription to a task list for every
// sender (task lists stop being subscribed by default, and the ones agents
// already followed must not survive the upgrade, human decision 3), and
// adds channels.created_at, the floor a task channel's last activity never
// goes below (design doc section 1). No earlier per-channel timestamp
// exists to backfill from, so every existing channel gets the migration
// time, the same reasoning as memory_access's own backfill: neither a
// task channel nor a memory built before this upgrade should look stale
// the moment it lands. The timestamp is time.Now, not the bus's
// overridable clock: migrate runs before the Bus (and Now) exists.
func addMemoryAccessAndDropTaskSubscriptions(tx *sql.Tx) error {
	if _, err := tx.Exec(memoryAccessDDL); err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	if _, err := tx.Exec(`INSERT OR IGNORE INTO memory_access(memory_id, accessed_at)
	  SELECT DISTINCT memory_id, ? FROM messages
	  WHERE memory_id IS NOT NULL AND tombstone=0 AND channel<>'tasks' AND channel NOT LIKE 'tasks/%'`,
		now); err != nil {
		return err
	}
	var hasCreatedAt int
	if err := tx.QueryRow("SELECT count(*) FROM pragma_table_info('channels') WHERE name='created_at'").Scan(&hasCreatedAt); err != nil {
		return err
	}
	if hasCreatedAt == 0 {
		if _, err := tx.Exec("ALTER TABLE channels ADD COLUMN created_at INTEGER NOT NULL DEFAULT 0"); err != nil {
			return err
		}
		if _, err := tx.Exec("UPDATE channels SET created_at=?", now); err != nil {
			return err
		}
	}
	_, err := tx.Exec(`DELETE FROM subscriptions WHERE channel='tasks' OR channel LIKE 'tasks/%'`)
	return err
}

// addSessionHarness (schema 7 -> 8, ADR 0015) adds the sessions columns that
// hold the harness name and version from the MCP initialize handshake's
// clientInfo. Existing rows get an empty string (unknown) until their next register. The
// column checks keep the step safe on a file that already has them (a test
// shaping an older version from a fresh file).
func addSessionHarness(tx *sql.Tx) error {
	for _, col := range []string{"harness", "harness_version"} {
		var has int
		if err := tx.QueryRow("SELECT count(*) FROM pragma_table_info('sessions') WHERE name=?", col).Scan(&has); err != nil {
			return err
		}
		if has == 0 {
			if _, err := tx.Exec("ALTER TABLE sessions ADD COLUMN " + col + " TEXT NOT NULL DEFAULT ''"); err != nil {
				return err
			}
		}
	}
	return nil
}

// addSessionHarnessProcess (schema 8 -> 9, ADR 0017) adds the sessions
// columns that hold the pid and start time of the harness process that
// registered the session. Existing rows get 0 (unknown) until their next
// register. Like addSessionHarness, it skips a column the file already has.
func addSessionHarnessProcess(tx *sql.Tx) error {
	for _, col := range []string{"harness_pid", "harness_start"} {
		var has int
		if err := tx.QueryRow("SELECT count(*) FROM pragma_table_info('sessions') WHERE name=?", col).Scan(&has); err != nil {
			return err
		}
		if has == 0 {
			if _, err := tx.Exec("ALTER TABLE sessions ADD COLUMN " + col + " INTEGER NOT NULL DEFAULT 0"); err != nil {
				return err
			}
		}
	}
	return nil
}

// addTagRanges (schema 9 -> 10, #20) adds the lo and hi columns that hold a
// subscribed pattern's byte range (tagRange) and backfills every existing
// row as an exact tag (lo = hi = tag): before patterns, every subscribed
// tag was one. SQLite needs a default to add a NOT NULL column. Like
// addSessionHarness, it skips a column the file already has: a v4 file
// migrating straight through gets both from tagSubscriptionTagsDDL in
// splitTagSets, with an empty string in every backfilled row, so the UPDATE always runs.
// A file with no table at all (an older file whose chain skips v5, or a
// test shaping one) has nothing to alter or backfill; the schema DDL that
// runs after migrate creates it.
func addTagRanges(tx *sql.Tx) error {
	var exists int
	if err := tx.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='tag_subscription_tags'").Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return nil
	}
	for _, col := range []string{"lo", "hi"} {
		var has int
		if err := tx.QueryRow("SELECT count(*) FROM pragma_table_info('tag_subscription_tags') WHERE name=?", col).Scan(&has); err != nil {
			return err
		}
		if has == 0 {
			if _, err := tx.Exec("ALTER TABLE tag_subscription_tags ADD COLUMN " + col + " TEXT NOT NULL DEFAULT ''"); err != nil {
				return err
			}
		}
	}
	_, err := tx.Exec("UPDATE tag_subscription_tags SET lo=tag, hi=tag")
	return err
}

// dropMessagesBytes (schema 1 -> 2, ADR 0006 item 4) rebuilds messages
// without the bytes column, which undercounted the envelope and was never
// read. The triggers are dropped first so the rebuild never touches
// messages_fts (an external-content table that keys on messages by name);
// the schema DDL recreates them and the indexes. seq's AUTOINCREMENT
// counter is carried over explicitly: DROP TABLE removes its
// sqlite_sequence row, and the copy alone would reset it to the surviving
// maximum, breaking the monotonic-seq guarantee Reset relies on.
func dropMessagesBytes(tx *sql.Tx) error {
	var counter int64
	if err := tx.QueryRow("SELECT coalesce((SELECT seq FROM sqlite_sequence WHERE name='messages'), 0)").Scan(&counter); err != nil {
		return err
	}
	stmts := []string{
		"DROP TRIGGER IF EXISTS messages_ai",
		"DROP TRIGGER IF EXISTS messages_ad",
		`CREATE TABLE messages_v2 (
  seq INTEGER PRIMARY KEY AUTOINCREMENT,
  channel TEXT NOT NULL,
  sender TEXT NOT NULL,
  context TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  type TEXT NOT NULL DEFAULT '',
  content TEXT NOT NULL,
  reply_to INTEGER,
  metadata TEXT,
  refs TEXT,
  memory_id INTEGER,
  revision INTEGER,
  tombstone INTEGER NOT NULL DEFAULT 0,
  tombstone_at INTEGER
)`,
		`INSERT INTO messages_v2 SELECT seq,channel,sender,context,created_at,type,content,reply_to,metadata,refs,memory_id,revision,tombstone,tombstone_at FROM messages`,
		"DROP TABLE messages",
		"ALTER TABLE messages_v2 RENAME TO messages",
	}
	for _, s := range stmts {
		if _, err := tx.Exec(s); err != nil {
			return err
		}
	}
	// sqlite_sequence has no unique key, so INSERT OR REPLACE would add a
	// second 'messages' row and SQLite would keep reading the first.
	if _, err := tx.Exec("UPDATE sqlite_sequence SET seq=max(seq, ?) WHERE name='messages'", counter); err != nil {
		return err
	}
	_, err := tx.Exec("INSERT INTO sqlite_sequence(name, seq) SELECT 'messages', ? WHERE NOT EXISTS (SELECT 1 FROM sqlite_sequence WHERE name='messages')", counter)
	return err
}

// refsBlock renders a messages.refs value as the text foldRefs appends to
// the row's content, or "" when there is nothing to fold (ADR 0019). A JSON
// array of {kind, value} entries becomes a "Refs:" line and one "- value"
// line per entry, values verbatim (no decoding, normalization or URI
// conversion; the kind is not written); an empty or null array folds
// nothing. Anything else (malformed JSON, an entry without a value) is
// appended as "Refs: <raw>" rather than failing: a failed step would leave
// the bus unable to open.
func refsBlock(raw string) string {
	var refs []struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal([]byte(raw), &refs); err != nil {
		return "\n\nRefs: " + refLine(raw)
	}
	if len(refs) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n\nRefs:")
	for _, r := range refs {
		if r.Value == "" {
			return "\n\nRefs: " + refLine(raw)
		}
		sb.WriteString("\n- " + refLine(r.Value))
	}
	return sb.String()
}

// refLine keeps one ref on one line: a value containing a control
// character (newline, tab, and so on) is written in strconv.Quote form.
// Every other value, quotes and backslashes included, is returned as is.
func refLine(v string) string {
	for _, r := range v {
		if unicode.IsControl(r) {
			return strconv.Quote(v)
		}
	}
	return v
}

// foldRefs (schema 10 -> 11, ADR 0019) moves each live row's refs into its
// content as a trailing "Refs:" block (refsBlock) and drops the column.
// messages_fts has insert and delete triggers only, so the index entry is
// replaced by hand around the UPDATE, and the row's embedding is deleted so
// the next pass recomputes it from the new text. Tombstoned rows are not
// folded: nothing reads their content again. The rows are read in full
// before the first write so no Exec runs against the transaction while a
// result set is open on it. The column check keeps the step safe on a file
// that already lacks the column (a test shaping an older version from a
// fresh file, whose DDL no longer has it).
func foldRefs(tx *sql.Tx) error {
	var has int
	if err := tx.QueryRow("SELECT count(*) FROM pragma_table_info('messages') WHERE name='refs'").Scan(&has); err != nil {
		return err
	}
	if has == 0 {
		return nil
	}
	// embed_failures is created only by the schema DDL, which runs after
	// migrate, so a file last written by a binary through v1.12.0 (schema
	// 8) lacks it and has no failures to clear.
	var hasFailures int
	if err := tx.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='embed_failures'").Scan(&hasFailures); err != nil {
		return err
	}
	type fold struct {
		seq                     int64
		subject, content, block string // block is the text appended to content
	}
	rows, err := tx.Query("SELECT seq, subject, content, refs FROM messages WHERE refs IS NOT NULL AND tombstone=0 ORDER BY seq")
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	var folds []fold
	for rows.Next() {
		var f fold
		var refs string
		if err := rows.Scan(&f.seq, &f.subject, &f.content, &refs); err != nil {
			return err
		}
		if f.block = refsBlock(refs); f.block != "" {
			folds = append(folds, f)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_ = rows.Close()
	for _, f := range folds {
		next := f.content + f.block
		for _, s := range []struct {
			q    string
			args []any
		}{
			{"INSERT INTO messages_fts(messages_fts, rowid, subject, content) VALUES('delete', ?, ?, ?)", []any{f.seq, f.subject, f.content}},
			{"UPDATE messages SET content=? WHERE seq=?", []any{next, f.seq}},
			{"INSERT INTO messages_fts(rowid, subject, content) VALUES(?, ?, ?)", []any{f.seq, f.subject, next}},
			{"DELETE FROM embeddings WHERE seq=?", []any{f.seq}},
		} {
			if _, err := tx.Exec(s.q, s.args...); err != nil {
				return err
			}
		}
		if hasFailures > 0 {
			if _, err := tx.Exec("DELETE FROM embed_failures WHERE seq=?", f.seq); err != nil {
				return err
			}
		}
	}
	// SQLite 3.35+; modernc v1.58.0 bundles a newer one. No index, trigger
	// or view names the column, so the drop is a plain table rewrite.
	_, err = tx.Exec("ALTER TABLE messages DROP COLUMN refs")
	return err
}

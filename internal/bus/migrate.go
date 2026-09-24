package bus

import (
	"context"
	"database/sql"
	"fmt"
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
	1: dropMessagesBytes,
	2: addTables,    // message_tags (ADR 0009)
	3: addTables,    // tag_subscriptions (ADR 0009)
	4: splitTagSets, // tag_subscription_tags (#13)
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

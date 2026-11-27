package bus

const schemaVersion = 14

// tagSubscriptionTagsDDL is shared by schema.go (fresh databases) and
// migrate.go's splitTagSets step (v4 -> v5, #13): one row per tag of each
// AND set, so matching drives from these few rows into message_tags(tag,
// seq) instead of scanning messages. ON DELETE CASCADE means every existing
// DELETE FROM tag_subscriptions (receive.go, sessions.go, UnsubscribeTags)
// cleans this table up without code changes. lo and hi (v10, #20) hold the
// pattern's byte range (tagRange): lo = hi for an exact tag, [p, p~] for a
// prefix p*. They carry a default so addTagRanges can ALTER them onto an
// older file; a v4 file migrating straight through gets them from this DDL
// in splitTagSets, which is why addTagRanges checks before adding.
const tagSubscriptionTagsDDL = `
CREATE TABLE IF NOT EXISTS tag_subscription_tags (
  sender TEXT NOT NULL,
  tags_key TEXT NOT NULL,
  tag TEXT NOT NULL,
  lo TEXT NOT NULL DEFAULT '',
  hi TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (sender, tags_key, tag),
  FOREIGN KEY (sender, tags_key) REFERENCES tag_subscriptions(sender, tags_key) ON DELETE CASCADE
);
`

// messagesFTSDDLv6 is the frozen v6..v13 form of the full-text index (ADR
// 0010): external-content on messages over subject and raw content, kept in
// step by two triggers. migrate.go's addSubject step (v5 -> v6) uses it so
// that historical step keeps its meaning; reindexTaskText (v13 -> v14)
// replaces it. No update trigger: a message row is never rewritten after
// insert (an edit inserts a new revision row).
const messagesFTSDDLv6 = `
CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(subject, content, content='messages', content_rowid='seq');
CREATE TRIGGER IF NOT EXISTS messages_ai AFTER INSERT ON messages BEGIN
  INSERT INTO messages_fts(rowid, subject, content) VALUES (new.seq, new.subject, new.content);
END;
CREATE TRIGGER IF NOT EXISTS messages_ad AFTER DELETE ON messages BEGIN
  INSERT INTO messages_fts(messages_fts, rowid, subject, content) VALUES ('delete', old.seq, old.subject, old.content);
END;
`

// ftsContentExpr is the SQL expression for the text indexed as a message's
// FTS content column, with alias naming the row ("new", "old" or a table
// alias). A task-channel row (the IsTaskChannel predicate) holds task JSON
// in content; indexing it raw would make every task match words like
// "status" or "pending", so only the description is indexed (subject has its
// own column). json_valid guards json_extract, which raises on malformed
// text; invalid JSON or a missing description indexes as the empty string. Every other row
// indexes its content. Both triggers and reindexTaskText use this one
// expression, so the delete trigger passes exactly what was inserted
// (external-content FTS5 requires that).
func ftsContentExpr(alias string) string {
	return "CASE WHEN (" + alias + ".channel = 'tasks' OR " + alias + ".channel LIKE 'tasks/%') THEN " +
		"CASE WHEN json_valid(" + alias + ".content) THEN COALESCE(json_extract(" + alias + ".content, '$.description'), '') ELSE '' END " +
		"ELSE " + alias + ".content END"
}

// messagesFTSDDL is shared by schema.go (fresh databases) and migrate.go's
// reindexTaskText step (v13 -> v14, #52): the full-text index over subject
// and ftsContentExpr, external-content on messages (the table is read only
// by rowid join, never for column values), kept in step by two triggers.
// FTS5 'rebuild' reads raw content, so it must never repopulate this table
// except over an empty messages table; reindexTaskText inserts explicitly.
var messagesFTSDDL = `
CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(subject, content, content='messages', content_rowid='seq');
CREATE TRIGGER IF NOT EXISTS messages_ai AFTER INSERT ON messages BEGIN
  INSERT INTO messages_fts(rowid, subject, content) VALUES (new.seq, new.subject, ` + ftsContentExpr("new") + `);
END;
CREATE TRIGGER IF NOT EXISTS messages_ad AFTER DELETE ON messages BEGIN
  INSERT INTO messages_fts(messages_fts, rowid, subject, content) VALUES ('delete', old.seq, old.subject, ` + ftsContentExpr("old") + `);
END;
`

// memoryAccessDDL is shared by schema.go (fresh databases) and migrate.go's
// addMemoryAccess step (v6 -> v7, ADR 0013): one row per live memory,
// updated on create, edit, get, and search hit, apart from messages so
// revisions need not carry it. Task rows never get one; the task-channel
// expiry rule governs them instead.
const memoryAccessDDL = `
CREATE TABLE IF NOT EXISTS memory_access (
  memory_id INTEGER PRIMARY KEY,
  accessed_at INTEGER NOT NULL
);
`

var schema = `
CREATE TABLE IF NOT EXISTS channels (
  name TEXT PRIMARY KEY,
  kind TEXT NOT NULL CHECK (kind IN ('ordinary','memory')),
  created_seq INTEGER NOT NULL,
  evicted_before_seq INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS sessions (
  sender TEXT PRIMARY KEY,
  context TEXT NOT NULL,
  owner TEXT NOT NULL,
  heartbeat INTEGER NOT NULL,
  registered_at INTEGER NOT NULL,
  harness TEXT NOT NULL DEFAULT '',
  harness_version TEXT NOT NULL DEFAULT '',
  harness_pid INTEGER NOT NULL DEFAULT 0,
  harness_start INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS sessions_heartbeat ON sessions(heartbeat);
CREATE TABLE IF NOT EXISTS subscriptions (
  sender TEXT NOT NULL,
  channel TEXT NOT NULL,
  cursor_seq INTEGER NOT NULL,
  last_activity INTEGER NOT NULL,
  pending_token TEXT NOT NULL DEFAULT '',
  pending_end_seq INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (sender, channel)
);
CREATE TABLE IF NOT EXISTS messages (
  seq INTEGER PRIMARY KEY AUTOINCREMENT,
  channel TEXT NOT NULL,
  sender TEXT NOT NULL,
  context TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  type TEXT NOT NULL DEFAULT '',
  content TEXT NOT NULL,
  reply_to INTEGER,
  metadata TEXT,
  memory_id INTEGER,
  revision INTEGER,
  tombstone INTEGER NOT NULL DEFAULT 0,
  tombstone_at INTEGER,
  subject TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS messages_channel_seq ON messages(channel, seq);
CREATE INDEX IF NOT EXISTS messages_memory ON messages(memory_id, tombstone);
CREATE INDEX IF NOT EXISTS messages_tombstone_at ON messages(tombstone_at);
CREATE INDEX IF NOT EXISTS messages_created_at ON messages(created_at);
` + messagesFTSDDL + `
CREATE TABLE IF NOT EXISTS embeddings (
  seq INTEGER PRIMARY KEY REFERENCES messages(seq) ON DELETE CASCADE,
  model TEXT NOT NULL,
  vector BLOB NOT NULL
);
CREATE TABLE IF NOT EXISTS embed_failures (
  seq INTEGER NOT NULL REFERENCES messages(seq) ON DELETE CASCADE,
  model TEXT NOT NULL,
  error TEXT NOT NULL,
  failed_at INTEGER NOT NULL,
  PRIMARY KEY (seq, model)
);
CREATE TABLE IF NOT EXISTS message_tags (
  seq INTEGER NOT NULL REFERENCES messages(seq) ON DELETE CASCADE,
  tag TEXT NOT NULL,
  PRIMARY KEY (seq, tag)
);
CREATE INDEX IF NOT EXISTS message_tags_tag_seq ON message_tags(tag, seq);
CREATE TABLE IF NOT EXISTS tag_subscriptions (
  sender TEXT NOT NULL,
  tags_key TEXT NOT NULL,
  created_seq INTEGER NOT NULL,
  PRIMARY KEY (sender, tags_key)
);
` + tagSubscriptionTagsDDL + `
CREATE TABLE IF NOT EXISTS receipts (
  sender TEXT NOT NULL,
  key TEXT NOT NULL,
  fingerprint TEXT NOT NULL,
  result TEXT NOT NULL,
  expires_at INTEGER NOT NULL,
  PRIMARY KEY (sender, key)
);
CREATE INDEX IF NOT EXISTS receipts_expires ON receipts(expires_at);
CREATE TABLE IF NOT EXISTS leases (
  name TEXT PRIMARY KEY,
  owner TEXT NOT NULL,
  expires_at INTEGER NOT NULL
);
INSERT OR IGNORE INTO leases(name, owner, expires_at) VALUES ('maintenance', '', 0);
CREATE TABLE IF NOT EXISTS meta (
  key TEXT PRIMARY KEY,
  value INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS notices (
  kind TEXT PRIMARY KEY,
  message TEXT NOT NULL,
  set_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS statuses (
  identity TEXT NOT NULL,
  revision INTEGER NOT NULL,
  tag      TEXT NOT NULL CHECK (tag IN ('idle','planning','implementing','reviewing','waiting','blocked','needs-human')),
  text     TEXT NOT NULL,
  problem  TEXT NOT NULL DEFAULT '',
  set_at   INTEGER NOT NULL,
  PRIMARY KEY (identity, revision)
);
` + memoryAccessDDL + `
`

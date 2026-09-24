package bus

const schemaVersion = 5

// tagSubscriptionTagsDDL is shared by schema.go (fresh databases) and
// migrate.go's splitTagSets step (v4 -> v5, #13): one row per tag of each
// AND set, so matching drives from these few rows into message_tags(tag,
// seq) instead of scanning messages. ON DELETE CASCADE means every existing
// DELETE FROM tag_subscriptions (receive.go, sessions.go, UnsubscribeTags)
// cleans this table up without code changes.
const tagSubscriptionTagsDDL = `
CREATE TABLE IF NOT EXISTS tag_subscription_tags (
  sender TEXT NOT NULL,
  tags_key TEXT NOT NULL,
  tag TEXT NOT NULL,
  PRIMARY KEY (sender, tags_key, tag),
  FOREIGN KEY (sender, tags_key) REFERENCES tag_subscriptions(sender, tags_key) ON DELETE CASCADE
);
`

const schema = `
CREATE TABLE IF NOT EXISTS channels (
  name TEXT PRIMARY KEY,
  kind TEXT NOT NULL CHECK (kind IN ('ordinary','memory')),
  created_seq INTEGER NOT NULL,
  evicted_before_seq INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS sessions (
  sender TEXT PRIMARY KEY,
  context TEXT NOT NULL,
  owner TEXT NOT NULL,
  heartbeat INTEGER NOT NULL,
  registered_at INTEGER NOT NULL
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
  refs TEXT,
  memory_id INTEGER,
  revision INTEGER,
  tombstone INTEGER NOT NULL DEFAULT 0,
  tombstone_at INTEGER
);
CREATE INDEX IF NOT EXISTS messages_channel_seq ON messages(channel, seq);
CREATE INDEX IF NOT EXISTS messages_memory ON messages(memory_id, tombstone);
CREATE INDEX IF NOT EXISTS messages_tombstone_at ON messages(tombstone_at);
CREATE INDEX IF NOT EXISTS messages_created_at ON messages(created_at);
CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(content, content='messages', content_rowid='seq');
CREATE TRIGGER IF NOT EXISTS messages_ai AFTER INSERT ON messages BEGIN
  INSERT INTO messages_fts(rowid, content) VALUES (new.seq, new.content);
END;
CREATE TRIGGER IF NOT EXISTS messages_ad AFTER DELETE ON messages BEGIN
  INSERT INTO messages_fts(messages_fts, rowid, content) VALUES ('delete', old.seq, old.content);
END;
CREATE TABLE IF NOT EXISTS embeddings (
  seq INTEGER PRIMARY KEY REFERENCES messages(seq) ON DELETE CASCADE,
  model TEXT NOT NULL,
  vector BLOB NOT NULL
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
CREATE TABLE IF NOT EXISTS notices (
  kind TEXT PRIMARY KEY,
  message TEXT NOT NULL,
  set_at INTEGER NOT NULL
);
`

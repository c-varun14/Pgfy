-- What alerting currently sees and what the receiver last heard. sent_state is
-- '' (nothing sent), 'firing' or 'resolved'.
CREATE TABLE alert_conditions (
 key TEXT PRIMARY KEY,
 kind TEXT NOT NULL,
 source TEXT NOT NULL,
 summary TEXT NOT NULL,
 detail TEXT NOT NULL DEFAULT '',
 active INTEGER NOT NULL,
 first_seen_at INTEGER NOT NULL,
 last_seen_at INTEGER NOT NULL,
 clear_since INTEGER,
 last_fired_at INTEGER,
 sent_state TEXT NOT NULL DEFAULT ''
);
-- One-shot events, recorded in the transaction of the change they report.
CREATE TABLE alert_events (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 kind TEXT NOT NULL,
 key TEXT NOT NULL,
 summary TEXT NOT NULL,
 detail TEXT NOT NULL DEFAULT '',
 created_at INTEGER NOT NULL,
 sent_at INTEGER,
 attempts INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX alert_events_unsent ON alert_events(sent_at, created_at);

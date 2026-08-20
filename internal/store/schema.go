// Package store is the SQLite persistence layer for the lease service. It owns
// the schema and the low-level transaction primitives; the lease manager layers
// business rules (fencing-token auth, renew window, idempotent release,
// transfer) on top.
//
// All write operations are wrapped in BEGIN IMMEDIATE transactions so
// concurrent writers (a holder releasing while the sweeper runs) serialize at
// the database level and exactly one transition wins.
package store

// schema is applied on every Open; SQLite's IF NOT EXISTS makes it idempotent
// so a fresh file, a reused file, and a post-crash file all converge to the
// same shape.
const schema = `
CREATE TABLE IF NOT EXISTS resources (
	name             TEXT    PRIMARY KEY,
	fencing_token    INTEGER NOT NULL DEFAULT 0,
	current_lease_id TEXT    NOT NULL DEFAULT '',
	locked           INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS leases (
	lease_id       TEXT    PRIMARY KEY,
	resource       TEXT    NOT NULL,
	holder         TEXT    NOT NULL,
	fencing_token  INTEGER NOT NULL,
	acquired_at    INTEGER NOT NULL,
	ttl_seconds    INTEGER NOT NULL,
	expires_at     INTEGER NOT NULL,
	last_heartbeat INTEGER NOT NULL,
	status         TEXT    NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_leases_resource ON leases(resource);
CREATE INDEX IF NOT EXISTS idx_leases_expires  ON leases(expires_at);
CREATE INDEX IF NOT EXISTS idx_leases_status   ON leases(status);
CREATE INDEX IF NOT EXISTS idx_leases_holder   ON leases(holder);

CREATE TABLE IF NOT EXISTS audit (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	lease_id      TEXT    NOT NULL DEFAULT '',
	resource      TEXT    NOT NULL DEFAULT '',
	action        TEXT    NOT NULL,
	actor         TEXT    NOT NULL DEFAULT '',
	fencing_token INTEGER NOT NULL DEFAULT 0,
	at            INTEGER NOT NULL,
	detail        TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_audit_lease   ON audit(lease_id);
CREATE INDEX IF NOT EXISTS idx_audit_resource ON audit(resource);
CREATE INDEX IF NOT EXISTS idx_audit_action  ON audit(action);
CREATE INDEX IF NOT EXISTS idx_audit_at      ON audit(at);
`

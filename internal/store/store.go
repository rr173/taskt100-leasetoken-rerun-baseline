package store

import (
	"database/sql"
	"fmt"
	"strings"

	"task100-leasetoken/internal/model"

	_ "modernc.org/sqlite" // pure-Go SQLite driver; CGO_ENABLED=0 compatible
)

// Store wraps a *sql.DB connection to the lease database. It is safe for
// concurrent use: database/sql pools connections and modernc.org/sqlite
// serializes writes via its own mutex plus BEGIN IMMEDIATE in the callers.
type Store struct {
	db *sql.DB
}

// Open creates or opens the SQLite file at path, applies the schema, and tunes
// pragmatic options for durability and concurrency:
//   - journal_mode=WAL: readers don't block a single writer;
//   - busy_timeout: a concurrent writer waits rather than failing fast;
//   - _txlock=immediate: every BEGIN is BEGIN IMMEDIATE, serializing writers.
func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_txlock=immediate",
		path,
	)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	// SQLite effectively serializes writes; one connection is enough and avoids
	// "database is locked" from interleaved write transactions on extra conns.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// BeginTx starts an IMMEDIATE transaction. Callers must Commit or Rollback.
func (s *Store) BeginTx() (*sql.Tx, error) { return s.db.Begin() }

// --- resources ---

// GetResource loads a resource row by name. found is false when absent.
func (s *Store) GetResource(tx *sql.Tx, name string) (r model.Resource, found bool, err error) {
	row := tx.QueryRow(
		`SELECT name, fencing_token, current_lease_id, locked FROM resources WHERE name=?`,
		name,
	)
	err = row.Scan(&r.Name, &r.FencingToken, &r.CurrentLeaseID, &r.Locked)
	if err == sql.ErrNoRows {
		return model.Resource{}, false, nil
	}
	if err != nil {
		return model.Resource{}, false, err
	}
	return r, true, nil
}

// UpsertResourceFencing increments fencing_token for name (creating the row at
// the new value if missing) and sets current_lease_id in one statement.
func (s *Store) UpsertResourceFencing(tx *sql.Tx, name string, newToken int64, currentLeaseID string) error {
	_, err := tx.Exec(
		`INSERT INTO resources(name, fencing_token, current_lease_id, locked)
		 VALUES(?,?,?,0)
		 ON CONFLICT(name) DO UPDATE SET fencing_token=excluded.fencing_token, current_lease_id=excluded.current_lease_id`,
		name, newToken, currentLeaseID,
	)
	return err
}

// SetResourceLease updates only current_lease_id (fencing_token unchanged).
func (s *Store) SetResourceLease(tx *sql.Tx, name, currentLeaseID string) error {
	_, err := tx.Exec(`UPDATE resources SET current_lease_id=? WHERE name=?`, currentLeaseID, name)
	return err
}

// SetResourceLocked flips the locked flag for a resource. locked=1 means
// acquire is refused with ErrResourceLocked.
func (s *Store) SetResourceLocked(tx *sql.Tx, name string, locked bool) error {
	v := 0
	if locked {
		v = 1
	}
	// Create the row if missing so lock-before-acquire works.
	_, err := tx.Exec(
		`INSERT INTO resources(name, fencing_token, current_lease_id, locked) VALUES(?,0,'',?)
		 ON CONFLICT(name) DO UPDATE SET locked=excluded.locked`,
		name, v,
	)
	return err
}

// ListResources returns every resource row.
func (s *Store) ListResources(tx *sql.Tx) ([]model.Resource, error) {
	rows, err := tx.Query(
		`SELECT name, fencing_token, current_lease_id, locked FROM resources ORDER BY name`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Resource
	for rows.Next() {
		var r model.Resource
		var locked int
		if err := rows.Scan(&r.Name, &r.FencingToken, &r.CurrentLeaseID, &locked); err != nil {
			return nil, err
		}
		r.Locked = locked != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// --- leases ---

// InsertLease persists a new lease row. The caller must have already bumped the
// resource fencing_token within the same transaction.
func (s *Store) InsertLease(tx *sql.Tx, l model.Lease) error {
	_, err := tx.Exec(
		`INSERT INTO leases(lease_id, resource, holder, fencing_token, acquired_at, ttl_seconds, expires_at, last_heartbeat, status)
		 VALUES(?,?,?,?,?,?,?,?,?)`,
		l.LeaseID, l.Resource, l.Holder, l.FencingToken,
		l.AcquiredAt, l.TTLSeconds, l.ExpiresAt, l.LastHeartbeat, l.Status,
	)
	return err
}

// GetLease loads a lease by id. found is false when absent.
func (s *Store) GetLease(tx *sql.Tx, leaseID string) (l model.Lease, found bool, err error) {
	row := tx.QueryRow(
		`SELECT lease_id, resource, holder, fencing_token, acquired_at, ttl_seconds, expires_at, last_heartbeat, status
		 FROM leases WHERE lease_id=?`, leaseID,
	)
	err = scanLease(row, &l)
	if err == sql.ErrNoRows {
		return model.Lease{}, false, nil
	}
	if err != nil {
		return model.Lease{}, false, err
	}
	return l, true, nil
}

// GetActiveLeaseByResource returns the active lease for a resource, if any.
// "Active" is by status column; the manager re-checks expires_at vs now.
func (s *Store) GetActiveLeaseByResource(tx *sql.Tx, resource string) (l model.Lease, found bool, err error) {
	row := tx.QueryRow(
		`SELECT lease_id, resource, holder, fencing_token, acquired_at, ttl_seconds, expires_at, last_heartbeat, status
		 FROM leases WHERE resource=? AND status=? LIMIT 1`, resource, model.StatusActive,
	)
	err = scanLease(row, &l)
	if err == sql.ErrNoRows {
		return model.Lease{}, false, nil
	}
	if err != nil {
		return model.Lease{}, false, err
	}
	return l, true, nil
}

// UpdateLeaseExpiry sets expires_at, ttl_seconds and last_heartbeat (used by
// Renew and TTLUpdate).
func (s *Store) UpdateLeaseExpiry(tx *sql.Tx, leaseID string, ttlSeconds, expiresAt, lastHeartbeat int64) error {
	_, err := tx.Exec(
		`UPDATE leases SET ttl_seconds=?, expires_at=?, last_heartbeat=? WHERE lease_id=?`,
		ttlSeconds, expiresAt, lastHeartbeat, leaseID,
	)
	return err
}

// UpdateLeaseHeartbeat sets only last_heartbeat.
func (s *Store) UpdateLeaseHeartbeat(tx *sql.Tx, leaseID string, lastHeartbeat int64) error {
	_, err := tx.Exec(`UPDATE leases SET last_heartbeat=? WHERE lease_id=?`, lastHeartbeat, leaseID)
	return err
}

// SetLeaseStatus transitions a lease to newStatus, guarded by expectedOld so
// only one concurrent writer flips active→released/expired. rowsAffected==0
// means the guard didn't match (another writer won the race).
func (s *Store) SetLeaseStatus(tx *sql.Tx, leaseID, expectedOld, newStatus string) (rowsAffected int64, err error) {
	res, err := tx.Exec(
		`UPDATE leases SET status=? WHERE lease_id=? AND status=?`, newStatus, leaseID, expectedOld,
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ListExpiredActive returns all leases whose status is still active but whose
// expires_at is at or before cutoff. Used by Sweep and by startup recovery.
func (s *Store) ListExpiredActive(tx *sql.Tx, cutoff int64) ([]model.Lease, error) {
	rows, err := tx.Query(
		`SELECT lease_id, resource, holder, fencing_token, acquired_at, ttl_seconds, expires_at, last_heartbeat, status
		 FROM leases WHERE status=? AND expires_at<=? ORDER BY expires_at`,
		model.StatusActive, cutoff,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectLeases(rows)
}

// ListLeases returns leases matching the optional filter. Empty filter fields
// are ignored; an empty Limit means "no limit".
func (s *Store) ListLeases(tx *sql.Tx, f model.ListLeasesFilter) ([]model.Lease, error) {
	var (
		clauses []string
		args    []any
	)
	if f.Status != "" {
		clauses = append(clauses, "status=?")
		args = append(args, f.Status)
	}
	if f.Resource != "" {
		clauses = append(clauses, "resource=?")
		args = append(args, f.Resource)
	}
	if f.Holder != "" {
		clauses = append(clauses, "holder=?")
		args = append(args, f.Holder)
	}
	q := `SELECT lease_id, resource, holder, fencing_token, acquired_at, ttl_seconds, expires_at, last_heartbeat, status FROM leases`
	if len(clauses) > 0 {
		q += " WHERE " + strings.Join(clauses, " AND ")
	}
	q += " ORDER BY acquired_at DESC"
	if f.Limit > 0 {
		q += " LIMIT ?"
		args = append(args, f.Limit)
	}
	rows, err := tx.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectLeases(rows)
}

// CountLeasesByStatus returns the count of leases in each status bucket.
func (s *Store) CountLeasesByStatus(tx *sql.Tx) (active, released, expired int, err error) {
	rows, err := tx.Query(`SELECT status, COUNT(*) FROM leases GROUP BY status`)
	if err != nil {
		return 0, 0, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var st string
		var c int
		if err := rows.Scan(&st, &c); err != nil {
			return 0, 0, 0, err
		}
		switch st {
		case model.StatusActive:
			active = c
		case model.StatusReleased:
			released = c
		case model.StatusExpired:
			expired = c
		}
	}
	return active, released, expired, rows.Err()
}

// CountHolders returns the number of distinct holders that currently hold an
// active lease.
func (s *Store) CountHolders(tx *sql.Tx, now int64) (int, error) {
	var c int
	err := tx.QueryRow(
		`SELECT COUNT(DISTINCT holder) FROM leases WHERE status=?`, model.StatusActive,
	).Scan(&c)
	return c, err
}

// CountLockedResources returns the number of resources with locked=1.
func (s *Store) CountLockedResources(tx *sql.Tx) (int, error) {
	var c int
	err := tx.QueryRow(`SELECT COUNT(*) FROM resources WHERE locked=1`).Scan(&c)
	return c, err
}

// --- audit ---

// InsertAudit appends an audit entry within the caller's transaction so the
// transition and its record commit atomically.
func (s *Store) InsertAudit(tx *sql.Tx, e model.AuditEntry) error {
	_, err := tx.Exec(
		`INSERT INTO audit(lease_id, resource, action, actor, fencing_token, at, detail)
		 VALUES(?,?,?,?,?,?,?)`,
		e.LeaseID, e.Resource, e.Action, e.Actor, e.FencingToken, e.At, e.Detail,
	)
	return err
}

// ListAudit returns audit entries matching the filter, newest first.
func (s *Store) ListAudit(tx *sql.Tx, f model.ListAuditFilter) ([]model.AuditEntry, error) {
	var (
		clauses []string
		args    []any
	)
	if f.LeaseID != "" {
		clauses = append(clauses, "lease_id=?")
		args = append(args, f.LeaseID)
	}
	if f.Resource != "" {
		clauses = append(clauses, "resource=?")
		args = append(args, f.Resource)
	}
	if f.Action != "" {
		clauses = append(clauses, "action=?")
		args = append(args, f.Action)
	}
	q := `SELECT id, lease_id, resource, action, actor, fencing_token, at, detail FROM audit`
	if len(clauses) > 0 {
		q += " WHERE " + strings.Join(clauses, " AND ")
	}
	q += " ORDER BY at DESC, id DESC"
	if f.Limit > 0 {
		q += " LIMIT ?"
		args = append(args, f.Limit)
	}
	rows, err := tx.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]model.AuditEntry, 0)
	for rows.Next() {
		var e model.AuditEntry
		if err := rows.Scan(&e.ID, &e.LeaseID, &e.Resource, &e.Action, &e.Actor, &e.FencingToken, &e.At, &e.Detail); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// --- helpers ---

// scanner abstracts *sql.Row and *sql.Rows for scanLease.
type scanner interface {
	Scan(dest ...any) error
}

func scanLease(sc scanner, l *model.Lease) error {
	return sc.Scan(
		&l.LeaseID, &l.Resource, &l.Holder, &l.FencingToken,
		&l.AcquiredAt, &l.TTLSeconds, &l.ExpiresAt, &l.LastHeartbeat, &l.Status,
	)
}

func collectLeases(rows *sql.Rows) ([]model.Lease, error) {
	out := make([]model.Lease, 0)
	for rows.Next() {
		var l model.Lease
		if err := scanLease(rows, &l); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

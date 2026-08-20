// Package lease implements the business rules on top of the store: fencing
// token auth, the renew window, idempotent release, transfer, TTL update,
// resource locking, the background sweep and startup recovery. The Manager is
// the single entry point used by the HTTP layer.
package lease

import (
	crand "crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"

	"task100-leasetoken/internal/clock"
	"task100-leasetoken/internal/model"
	"task100-leasetoken/internal/store"
)

// Sentinel errors returned to callers. The HTTP layer maps these to status
// codes and JSON error strings; tests assert on them directly.
var (
	// ErrConflict: the resource already has an active, unexpired lease.
	ErrConflict = errors.New("resource already held by an active lease")
	// ErrNotFound: no lease with that id.
	ErrNotFound = errors.New("lease not found")
	// ErrFencingMismatch: the presented fencing token does not match the lease.
	// This is the stale-holder rejection path: after a lease is swept/expired
	// and the resource re-acquired, the old holder's token no longer matches.
	ErrFencingMismatch = errors.New("fencing token does not match lease")
	// ErrLeaseExpired: the lease exists but is logically past its expires_at
	// (sweep hasn't run yet, or it's a terminal expired row).
	ErrLeaseExpired = errors.New("lease has expired")
	// ErrRenewTooEarly: renew called before the TTL half-window opens.
	ErrRenewTooEarly = errors.New("renew too early")
	// ErrTerminal: the lease is released/expired and the op (renew/heartbeat/
	// transfer/ttl) is not idempotent on terminal leases.
	ErrTerminal = errors.New("lease is terminal (released or expired)")
	// ErrResourceLocked: the resource has been administratively locked.
	ErrResourceLocked = errors.New("resource is locked")
	// ErrTimeout: acquire-wait polled past its deadline without acquiring.
	ErrTimeout = errors.New("acquire timed out")
)

// RenewWindowFraction is the fraction of remaining TTL at/under which renew is
// allowed. 0.5 means "only the last half of the TTL". It is a package constant
// so tests can reason about exact boundaries.
const RenewWindowFraction = 0.5

// SystemActor labels audit entries written by the sweeper or recovery path
// rather than a named holder.
const SystemActor = "system"

// Manager coordinates leases, fencing tokens, transfers and sweeps. It is safe
// for concurrent use because every mutating method runs inside one store
// transaction (BEGIN IMMEDIATE), which serializes writers.
type Manager struct {
	store *store.Store
	clk   clock.Clock
}

// New returns a Manager over the given store using the given clock.
func New(s *store.Store, c clock.Clock) *Manager {
	return &Manager{store: s, clk: c}
}

// now is a shorthand for the manager's current clock reading.
func (m *Manager) now() int64 { return m.clk.Now() }

// Acquire issues a new lease for resource to holder.
//
// Sequence within one transaction:
//  1. refuse if the resource is locked;
//  2. read the resource; if it has an active lease that is still unexpired,
//     refuse; if the active lease is logically expired (sweep lagged), retire
//     it in place and bump the resource token;
//  3. bump the resource fencing_token by 1 (monotonic per Acquire);
//  4. insert a new active lease carrying the new fencing_token;
//  5. set the resource's current_lease_id and write an audit entry.
func (m *Manager) Acquire(resource, holder string, ttlSeconds int64) (model.AcquireResponse, error) {
	var resp model.AcquireResponse
	if err := validateAcquire(resource, holder, ttlSeconds); err != nil {
		return resp, err
	}

	tx, err := m.store.BeginTx()
	if err != nil {
		return resp, err
	}
	defer rollback(tx)

	now := m.now()

	res, found, err := m.store.GetResource(tx, resource)
	if err != nil {
		return resp, err
	}
	if found && res.Locked {
		return resp, ErrResourceLocked
	}

	if existing, found, err := m.store.GetActiveLeaseByResource(tx, resource); err != nil {
		return resp, err
	} else if found {
		if existing.ExpiresAt > now {
			return resp, ErrConflict
		}
		if _, err := m.store.SetLeaseStatus(tx, existing.LeaseID, model.StatusActive, model.StatusExpired); err != nil {
			return resp, err
		}
		if err := m.store.InsertAudit(tx, audit(model.AuditSweep, existing, SystemActor, now, "evicted-stale-on-acquire")); err != nil {
			return resp, err
		}
	}

	newToken := res.FencingToken + 1
	id := newLeaseID()
	lease := model.Lease{
		LeaseID:       id,
		Resource:      resource,
		Holder:        holder,
		FencingToken:  newToken,
		AcquiredAt:    now,
		TTLSeconds:    ttlSeconds,
		ExpiresAt:     now + ttlSeconds,
		LastHeartbeat: now,
		Status:        model.StatusActive,
	}
	if err := m.store.InsertLease(tx, lease); err != nil {
		return resp, err
	}
	if err := m.store.UpsertResourceFencing(tx, resource, newToken, id); err != nil {
		return resp, err
	}
	if err := m.store.InsertAudit(tx, audit(model.AuditAcquire, lease, holder, now, "")); err != nil {
		return resp, err
	}
	if err := tx.Commit(); err != nil {
		return resp, err
	}
	resp.LeaseID = id
	resp.FencingToken = newToken
	resp.ExpiresAt = lease.ExpiresAt
	return resp, nil
}

// Renew extends the lease's expires_at to now+ttlSeconds, but only if:
//   - the lease exists and is active,
//   - the presented fencing_token matches the lease's,
//   - the lease is not logically expired (now <= expires_at),
//   - now is within the renew window (remaining TTL <= ttl_seconds/2).
//
// The fencing_token is unchanged across renewals.
func (m *Manager) Renew(leaseID string, fencingToken int64, ttlSeconds int64) (model.RenewResponse, error) {
	var resp model.RenewResponse
	if leaseID == "" {
		return resp, errors.New("lease_id must not be empty")
	}
	if ttlSeconds <= 0 {
		return resp, errors.New("ttl_seconds must be positive")
	}

	tx, err := m.store.BeginTx()
	if err != nil {
		return resp, err
	}
	defer rollback(tx)

	now := m.now()
	l, found, err := m.store.GetLease(tx, leaseID)
	if err != nil {
		return resp, err
	}
	if !found {
		return resp, ErrNotFound
	}
	if l.FencingToken != fencingToken {
		return resp, ErrFencingMismatch
	}
	if model.IsTerminal(l.Status) {
		return resp, ErrTerminal
	}
	if now > l.ExpiresAt {
		return resp, ErrLeaseExpired
	}
	// Renew window: allowed only when remaining TTL <= half of ttl_seconds.
	remaining := l.ExpiresAt - now
	threshold := int64(float64(l.TTLSeconds) * RenewWindowFraction)
	if remaining > threshold {
		return resp, ErrRenewTooEarly
	}

	newExpiry := now + ttlSeconds
	if err := m.store.UpdateLeaseExpiry(tx, leaseID, ttlSeconds, newExpiry, now); err != nil {
		return resp, err
	}
	if err := m.store.InsertAudit(tx, audit(model.AuditRenew, l, l.Holder, now, renewDetail(ttlSeconds, newExpiry))); err != nil {
		return resp, err
	}
	if err := tx.Commit(); err != nil {
		return resp, err
	}
	resp.FencingToken = l.FencingToken
	resp.ExpiresAt = newExpiry
	return resp, nil
}

// Release frees a lease. It is idempotent on terminal leases when the fencing
// token matches: a second release with the matching token succeeds and changes
// nothing. A token mismatch is always rejected. On the active→released
// transition the resource fencing_token is bumped by 1.
func (m *Manager) Release(leaseID string, fencingToken int64) error {
	if leaseID == "" {
		return errors.New("lease_id must not be empty")
	}
	tx, err := m.store.BeginTx()
	if err != nil {
		return err
	}
	defer rollback(tx)

	now := m.now()
	l, found, err := m.store.GetLease(tx, leaseID)
	if err != nil {
		return err
	}
	if !found {
		return ErrNotFound
	}
	if l.FencingToken != fencingToken {
		return ErrFencingMismatch
	}
	if model.IsTerminal(l.Status) {
		return tx.Commit() // idempotent, no state change
	}
	n, err := m.store.SetLeaseStatus(tx, leaseID, model.StatusActive, model.StatusReleased)
	if err != nil {
		return err
	}
	if n == 0 {
		// Lost the race to a concurrent sweeper: lease is no longer active.
		return tx.Commit()
	}
	res, _, err := m.store.GetResource(tx, l.Resource)
	if err != nil {
		return err
	}
	if err := m.store.UpsertResourceFencing(tx, l.Resource, res.FencingToken+1, ""); err != nil {
		return err
	}
	if err := m.store.InsertAudit(tx, audit(model.AuditRelease, l, l.Holder, now, "")); err != nil {
		return err
	}
	return tx.Commit()
}

// Heartbeat refreshes last_heartbeat without extending the TTL. Requires an
// active, matching-token, non-expired lease.
func (m *Manager) Heartbeat(leaseID string, fencingToken int64) (model.HeartbeatResponse, error) {
	var resp model.HeartbeatResponse
	if leaseID == "" {
		return resp, errors.New("lease_id must not be empty")
	}
	tx, err := m.store.BeginTx()
	if err != nil {
		return resp, err
	}
	defer rollback(tx)

	now := m.now()
	l, found, err := m.store.GetLease(tx, leaseID)
	if err != nil {
		return resp, err
	}
	if !found {
		return resp, ErrNotFound
	}
	if l.FencingToken != fencingToken {
		return resp, ErrFencingMismatch
	}
	if model.IsTerminal(l.Status) {
		return resp, ErrTerminal
	}
	if now > l.ExpiresAt {
		return resp, ErrLeaseExpired
	}
	if err := m.store.UpdateLeaseHeartbeat(tx, leaseID, now); err != nil {
		return resp, err
	}
	if err := m.store.InsertAudit(tx, audit(model.AuditHeartbeat, l, l.Holder, now, "")); err != nil {
		return resp, err
	}
	if err := tx.Commit(); err != nil {
		return resp, err
	}
	resp.LastHeartbeat = now
	return resp, nil
}

// Sweep retires every active-but-expired lease, bumping each resource's
// fencing_token by 1. Safe to run concurrently with Release: the
// SetLeaseStatus guard (active→expired) ensures exactly one transition wins.
func (m *Manager) Sweep() (model.SweepResponse, error) {
	var resp model.SweepResponse
	tx, err := m.store.BeginTx()
	if err != nil {
		return resp, err
	}
	defer rollback(tx)

	now := m.now()
	expired, err := m.store.ListExpiredActive(tx, now)
	if err != nil {
		return resp, err
	}
	for _, l := range expired {
		n, err := m.store.SetLeaseStatus(tx, l.LeaseID, model.StatusActive, model.StatusExpired)
		if err != nil {
			return resp, err
		}
		if n == 0 {
			continue // a concurrent Release won this row; skip the token bump
		}
		res, _, err := m.store.GetResource(tx, l.Resource)
		if err != nil {
			return resp, err
		}
		if err := m.store.UpsertResourceFencing(tx, l.Resource, res.FencingToken+1, ""); err != nil {
			return resp, err
		}
		if err := m.store.InsertAudit(tx, audit(model.AuditSweep, l, SystemActor, now, "")); err != nil {
			return resp, err
		}
		resp.Expired++
	}
	if err := tx.Commit(); err != nil {
		return resp, err
	}
	return resp, nil
}

// Recover is called once at startup. It performs the same work as Sweep so a
// freshly-restarted process never serves stale active leases that died with the
// previous process. Single transaction per the recovery contract.
func (m *Manager) Recover() (int, error) {
	resp, err := m.Sweep()
	if err != nil {
		return 0, err
	}
	// Record that a recovery pass ran (even if it expired nothing) so the audit
	// trail shows the restart boundary.
	if resp.Expired > 0 {
		_ = m.auditRecover(resp.Expired)
	}
	return resp.Expired, nil
}

// auditRecover writes a single recover audit line in its own transaction.
func (m *Manager) auditRecover(n int) error {
	tx, err := m.store.BeginTx()
	if err != nil {
		return err
	}
	defer rollback(tx)
	e := model.AuditEntry{
		Action: model.AuditRecover,
		Actor:  SystemActor,
		At:     m.now(),
		Detail: recoverDetail(n),
	}
	if err := m.store.InsertAudit(tx, e); err != nil {
		return err
	}
	return tx.Commit()
}

// GetLease is a read-only diagnostic used by the API and smoke test.
func (m *Manager) GetLease(leaseID string) (model.Lease, error) {
	tx, err := m.store.BeginTx()
	if err != nil {
		return model.Lease{}, err
	}
	defer rollback(tx)
	l, found, err := m.store.GetLease(tx, leaseID)
	if err != nil {
		return model.Lease{}, err
	}
	if !found {
		return model.Lease{}, ErrNotFound
	}
	return l, tx.Commit()
}

// GetResource returns the resource row (or a zero-value, found=false) by name.
func (m *Manager) GetResource(resource string) (model.Resource, bool, error) {
	tx, err := m.store.BeginTx()
	if err != nil {
		return model.Resource{}, false, err
	}
	defer rollback(tx)
	r, found, err := m.store.GetResource(tx, resource)
	if err != nil {
		return model.Resource{}, false, err
	}
	if !found {
		return model.Resource{}, false, tx.Commit()
	}
	return r, true, tx.Commit()
}

// GetResourceFencing returns the current fencing-token counter for a resource.
// Used by the smoke test to assert monotonicity.
func (m *Manager) GetResourceFencing(resource string) (int64, error) {
	r, found, err := m.GetResource(resource)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, nil
	}
	return r.FencingToken, nil
}

// --- helpers ---

func validateAcquire(resource, holder string, ttlSeconds int64) error {
	if resource == "" {
		return errors.New("resource must not be empty")
	}
	if holder == "" {
		return errors.New("holder must not be empty")
	}
	if ttlSeconds <= 0 {
		return errors.New("ttl_seconds must be positive")
	}
	return nil
}

// newLeaseID returns a 16-byte hex id. crypto/rand is used (not math/rand,
// which would need a seed and races under concurrency) so ids are unique across
// concurrent acquires without a global counter.
func newLeaseID() string {
	var b [16]byte
	if _, err := crand.Read(b[:]); err != nil {
		return "fallback-lease-id"
	}
	return hex.EncodeToString(b[:])
}

func rollback(tx *sql.Tx) { _ = tx.Rollback() }

// audit builds an AuditEntry from a lease for the common case where the entry
// describes an operation on that lease.
func audit(action string, l model.Lease, actor string, at int64, detail string) model.AuditEntry {
	return model.AuditEntry{
		LeaseID:      l.LeaseID,
		Resource:     l.Resource,
		Action:       action,
		Actor:        actor,
		FencingToken: l.FencingToken,
		At:           at,
		Detail:       detail,
	}
}

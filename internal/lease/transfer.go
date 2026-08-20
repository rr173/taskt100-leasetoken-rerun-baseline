package lease

import (
	"errors"
	"strconv"

	"task100-leasetoken/internal/model"
)

// Transfer moves an active lease to a new holder. Within one transaction it:
//   - verifies the lease exists, is active, matches the fencing token and is
//     not logically expired;
//   - retires the old lease (status=released);
//   - bumps the resource fencing_token by 1;
//   - inserts a new active lease for new_holder carrying the new fencing token
//     and the *same* remaining TTL window (acquired_at and expires_at preserved
//     so the lease does not gain free time by being transferred);
//   - writes audit entries for both the release-side and the acquire-side.
//
// Because the old lease's fencing token differs from the new one, a stale
// holder holding the old token cannot renew or release the new lease — the
// fencing token naturally invalidates them.
func (m *Manager) Transfer(leaseID string, fencingToken int64, newHolder string) (model.TransferResponse, error) {
	var resp model.TransferResponse
	if leaseID == "" {
		return resp, errors.New("lease_id must not be empty")
	}
	if newHolder == "" {
		return resp, errors.New("new_holder must not be empty")
	}

	tx, err := m.store.BeginTx()
	if err != nil {
		return resp, err
	}
	defer rollback(tx)

	now := m.now()
	old, found, err := m.store.GetLease(tx, leaseID)
	if err != nil {
		return resp, err
	}
	if !found {
		return resp, ErrNotFound
	}
	if old.FencingToken != fencingToken {
		return resp, ErrFencingMismatch
	}
	if model.IsTerminal(old.Status) {
		return resp, ErrTerminal
	}
	if now > old.ExpiresAt {
		return resp, ErrLeaseExpired
	}

	// Retire the old lease.
	if _, err := m.store.SetLeaseStatus(tx, old.LeaseID, model.StatusActive, model.StatusReleased); err != nil {
		return resp, err
	}
	// Bump the resource token; the new lease carries the bumped value.
	res, _, err := m.store.GetResource(tx, old.Resource)
	if err != nil {
		return resp, err
	}
	newToken := res.FencingToken + 1

	newID := newLeaseID()
	fresh := model.Lease{
		LeaseID:       newID,
		Resource:      old.Resource,
		Holder:        newHolder,
		FencingToken:  newToken,
		AcquiredAt:    old.AcquiredAt, // preserve the original acquisition time
		TTLSeconds:    old.TTLSeconds,
		ExpiresAt:     old.ExpiresAt, // preserve remaining validity window
		LastHeartbeat: now,
		Status:        model.StatusActive,
	}
	if err := m.store.InsertLease(tx, fresh); err != nil {
		return resp, err
	}
	if err := m.store.UpsertResourceFencing(tx, old.Resource, newToken, newID); err != nil {
		return resp, err
	}
	if err := m.store.InsertAudit(tx, audit(model.AuditTransfer, old, old.Holder, now, transferFromDetail(newHolder))); err != nil {
		return resp, err
	}
	if err := m.store.InsertAudit(tx, audit(model.AuditTransfer, fresh, newHolder, now, transferToDetail(old.Holder))); err != nil {
		return resp, err
	}
	if err := tx.Commit(); err != nil {
		return resp, err
	}
	resp.OldLeaseID = old.LeaseID
	resp.NewLeaseID = newID
	resp.FencingToken = newToken
	resp.ExpiresAt = old.ExpiresAt
	return resp, nil
}

// UpdateTTL changes a lease's ttl_seconds and recomputes expires_at = now +
// newTTLSeconds. Unlike Renew it is not bound by the renew window: a holder may
// lengthen (or shorten) its own lease explicitly. Still requires fencing-token
// auth and an active, non-expired lease.
func (m *Manager) UpdateTTL(leaseID string, fencingToken int64, newTTLSeconds int64) (model.RenewResponse, error) {
	var resp model.RenewResponse
	if leaseID == "" {
		return resp, errors.New("lease_id must not be empty")
	}
	if newTTLSeconds <= 0 {
		return resp, errors.New("new_ttl_seconds must be positive")
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

	newExpiry := now + newTTLSeconds
	if err := m.store.UpdateLeaseExpiry(tx, leaseID, newTTLSeconds, newExpiry, now); err != nil {
		return resp, err
	}
	if err := m.store.InsertAudit(tx, audit(model.AuditTTLUpdate, l, l.Holder, now, ttlDetail(newTTLSeconds, newExpiry))); err != nil {
		return resp, err
	}
	if err := tx.Commit(); err != nil {
		return resp, err
	}
	resp.FencingToken = l.FencingToken
	resp.ExpiresAt = newExpiry
	return resp, nil
}

// ForceRelease administratively revokes a lease regardless of who holds it. It
// bumps the resource fencing_token (so a stale holder cannot later act on the
// retired lease) and writes an audit entry attributed to the admin actor.
// Works on active, released and expired leases alike; on a terminal lease it is
// a no-op that still succeeds.
func (m *Manager) ForceRelease(leaseID, actor string) error {
	if leaseID == "" {
		return errors.New("lease_id must not be empty")
	}
	if actor == "" {
		actor = "admin"
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
	if model.IsTerminal(l.Status) {
		return tx.Commit() // idempotent
	}
	if _, err := m.store.SetLeaseStatus(tx, leaseID, model.StatusActive, model.StatusReleased); err != nil {
		return err
	}
	res, _, err := m.store.GetResource(tx, l.Resource)
	if err != nil {
		return err
	}
	if err := m.store.UpsertResourceFencing(tx, l.Resource, res.FencingToken+1, ""); err != nil {
		return err
	}
	if err := m.store.InsertAudit(tx, audit(model.AuditForceRelease, l, actor, now, "")); err != nil {
		return err
	}
	return tx.Commit()
}

// Remaining reports the seconds left on a lease and whether it is past expiry.
func (m *Manager) Remaining(leaseID string) (model.RemainingResponse, error) {
	var resp model.RemainingResponse
	l, err := m.GetLease(leaseID)
	if err != nil {
		return resp, err
	}
	now := m.now()
	resp.LeaseID = leaseID
	resp.Remaining = l.ExpiresAt - now
	resp.Expired = now > l.ExpiresAt
	return resp, nil
}

// --- detail strings (kept tiny; human-readable, not parsed) ---

func renewDetail(ttl, expiry int64) string {
	return "ttl=" + strconv.FormatInt(ttl, 10) + " expires=" + strconv.FormatInt(expiry, 10)
}

func recoverDetail(n int) string {
	return "expired=" + strconv.Itoa(n)
}

func transferFromDetail(newHolder string) string {
	return "to:" + newHolder
}

func transferToDetail(oldHolder string) string {
	return "from:" + oldHolder
}

func ttlDetail(newTTL, expiry int64) string {
	return "new_ttl=" + strconv.FormatInt(newTTL, 10) + " expires=" + strconv.FormatInt(expiry, 10)
}

// lockDetail / unlockDetail keep the audit Detail column populated.
func lockDetail(locked bool) string {
	if locked {
		return "locked"
	}
	return "unlocked"
}

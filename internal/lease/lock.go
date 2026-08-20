package lease

import (
	"errors"

	"task100-leasetoken/internal/model"
)

// LockResource forbids future Acquire on a resource. Existing active leases are
// untouched: lock is a forward gate, not a revocation. Creates the resource row
// if missing so lock-before-acquire is valid.
func (m *Manager) LockResource(resource, actor string) error {
	if resource == "" {
		return errors.New("resource must not be empty")
	}
	if actor == "" {
		actor = "admin"
	}
	tx, err := m.store.BeginTx()
	if err != nil {
		return err
	}
	defer rollback(tx)

	// Optimistic: only flip 0→1; if already locked, succeed idempotently.
	r, found, err := m.store.GetResource(tx, resource)
	if err != nil {
		return err
	}
	if found && r.Locked {
		return tx.Commit()
	}
	if err := m.store.SetResourceLocked(tx, resource, true); err != nil {
		return err
	}
	e := model.AuditEntry{
		Resource: resource,
		Action:   model.AuditLock,
		Actor:    actor,
		At:       m.now(),
		Detail:   lockDetail(true),
	}
	if err := m.store.InsertAudit(tx, e); err != nil {
		return err
	}
	return tx.Commit()
}

// UnlockResource re-enables Acquire on a previously locked resource. Idempotent.
func (m *Manager) UnlockResource(resource, actor string) error {
	if resource == "" {
		return errors.New("resource must not be empty")
	}
	if actor == "" {
		actor = "admin"
	}
	tx, err := m.store.BeginTx()
	if err != nil {
		return err
	}
	defer rollback(tx)

	r, found, err := m.store.GetResource(tx, resource)
	if err != nil {
		return err
	}
	if found && !r.Locked {
		return tx.Commit()
	}
	if err := m.store.SetResourceLocked(tx, resource, false); err != nil {
		return err
	}
	e := model.AuditEntry{
		Resource: resource,
		Action:   model.AuditUnlock,
		Actor:    actor,
		At:       m.now(),
		Detail:   lockDetail(false),
	}
	if err := m.store.InsertAudit(tx, e); err != nil {
		return err
	}
	return tx.Commit()
}

// ListLeases returns leases matching the filter.
func (m *Manager) ListLeases(f model.ListLeasesFilter) ([]model.Lease, error) {
	tx, err := m.store.BeginTx()
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	out, err := m.store.ListLeases(tx, f)
	if err != nil {
		return nil, err
	}
	return out, tx.Commit()
}

// ListResources returns every resource row.
func (m *Manager) ListResources() ([]model.Resource, error) {
	tx, err := m.store.BeginTx()
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	out, err := m.store.ListResources(tx)
	if err != nil {
		return nil, err
	}
	return out, tx.Commit()
}

// ListAudit returns audit entries matching the filter.
func (m *Manager) ListAudit(f model.ListAuditFilter) ([]model.AuditEntry, error) {
	tx, err := m.store.BeginTx()
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	out, err := m.store.ListAudit(tx, f)
	if err != nil {
		return nil, err
	}
	return out, tx.Commit()
}

// LeasesByHolder is a convenience wrapper filtering on holder with active-only
// by default (caller can pass StatusActive via ListLeasesFilter for that).
func (m *Manager) LeasesByHolder(holder string, limit int) ([]model.Lease, error) {
	if holder == "" {
		return nil, errors.New("holder must not be empty")
	}
	return m.ListLeases(model.ListLeasesFilter{Holder: holder, Limit: limit})
}

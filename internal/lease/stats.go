package lease

import (
	"task100-leasetoken/internal/model"
)

// Stats aggregates the service's current counts for GET /stats. Read-only; runs
// in a single read transaction.
func (m *Manager) Stats() (model.Stats, error) {
	var s model.Stats
	tx, err := m.store.BeginTx()
	if err != nil {
		return s, err
	}
	defer rollback(tx)

	resources, err := m.store.ListResources(tx)
	if err != nil {
		return s, err
	}
	s.Resources = len(resources)
	for _, r := range resources {
		if r.Locked {
			s.LockedResources++
		}
	}

	active, released, expired, err := m.store.CountLeasesByStatus(tx)
	if err != nil {
		return s, err
	}
	s.ActiveLeases = active
	s.ReleasedLeases = released
	s.ExpiredLeases = expired

	holders, err := m.store.CountHolders(tx, m.now())
	if err != nil {
		return s, err
	}
	s.Holders = holders

	return s, tx.Commit()
}

// Metrics is a richer snapshot than Stats, used by GET /metrics to render a
// Prometheus-style text exposition. It embeds Stats and adds the highest
// fencing token across all resources (a rough activity gauge).
type Metrics struct {
	model.Stats
	MaxFencingToken int64
	AuditEntries    int
}

// Metrics returns the current Metrics snapshot.
func (m *Manager) Metrics() (Metrics, error) {
	var me Metrics
	tx, err := m.store.BeginTx()
	if err != nil {
		return me, err
	}
	defer rollback(tx)

	resources, err := m.store.ListResources(tx)
	if err != nil {
		return me, err
	}
	me.Resources = len(resources)
	for _, r := range resources {
		if r.Locked {
			me.LockedResources++
		}
		if r.FencingToken > me.MaxFencingToken {
			me.MaxFencingToken = r.FencingToken
		}
	}

	active, released, expired, err := m.store.CountLeasesByStatus(tx)
	if err != nil {
		return me, err
	}
	me.ActiveLeases = active
	me.ReleasedLeases = released
	me.ExpiredLeases = expired

	holders, err := m.store.CountHolders(tx, m.now())
	if err != nil {
		return me, err
	}
	me.Holders = holders

	// Total audit rows (head count) for a churn indicator.
	entries, err := m.store.ListAudit(tx, model.ListAuditFilter{Limit: 0})
	if err != nil {
		return me, err
	}
	me.AuditEntries = len(entries)

	if err := tx.Commit(); err != nil {
		return me, err
	}
	return me, nil
}

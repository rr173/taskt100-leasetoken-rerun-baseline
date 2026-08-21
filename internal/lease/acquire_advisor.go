package lease

// AcquireAdvice reports whether an acquire on resource would succeed right now,
// without mutating state. It mirrors the conflict rules of Acquire: a resource
// held by an active, unexpired lease is reported as a conflict (not allowed);
// an administratively locked resource is reported as locked; an absent resource,
// or one whose only lease is logically expired (sweep lagged), is reported as
// acquirable — Acquire would create or reclaim it.
type AcquireAdvice struct {
	Resource     string `json:"resource"`
	Allowed      bool   `json:"allowed"`
	Locked       bool   `json:"locked"`
	CurrentToken int64  `json:"current_token"`
	Reason       string `json:"reason"`
}

func (m *Manager) AcquireAdvice(resource string) (AcquireAdvice, error) {
	tx, err := m.store.BeginTx()
	if err != nil {
		return AcquireAdvice{}, err
	}
	defer rollback(tx)

	now := m.now()

	row, found, err := m.store.GetResource(tx, resource)
	if err != nil {
		return AcquireAdvice{}, err
	}
	if !found {
		return AcquireAdvice{Resource: resource, Allowed: true, Reason: "resource will be created"}, tx.Commit()
	}
	if row.Locked {
		return AcquireAdvice{Resource: resource, Locked: true, CurrentToken: row.FencingToken, Reason: ErrResourceLocked.Error()}, tx.Commit()
	}

	// Mirror Acquire's conflict check: an active lease that has not yet passed
	// its expires_at blocks acquisition. A logically expired active lease (sweep
	// lagged) does not, because Acquire retires it in place.
	existing, found, err := m.store.GetActiveLeaseByResource(tx, resource)
	if err != nil {
		return AcquireAdvice{}, err
	}
	if found && existing.ExpiresAt > now {
		return AcquireAdvice{Resource: resource, Allowed: false, CurrentToken: row.FencingToken, Reason: ErrConflict.Error()}, tx.Commit()
	}
	return AcquireAdvice{Resource: resource, Allowed: true, CurrentToken: row.FencingToken, Reason: "resource is available for an acquisition attempt"}, tx.Commit()
}

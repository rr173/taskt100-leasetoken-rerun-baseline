package lease

import "task100-leasetoken/internal/model"

// LeaseHealth explains the state a caller should expect before mutating a
// lease, including remaining lifetime and the current fencing generation.
type LeaseHealth struct {
	LeaseID       string `json:"lease_id"`
	Resource      string `json:"resource"`
	Status        string `json:"status"`
	RemainingSecs int64  `json:"remaining_secs"`
	FencingToken  int64  `json:"fencing_token"`
	CanRenew      bool   `json:"can_renew"`
}

func (m *Manager) LeaseHealth(id string) (LeaseHealth, error) {
	l, err := m.GetLease(id)
	if err != nil {
		return LeaseHealth{}, err
	}
	remaining := l.ExpiresAt - m.now()
	return LeaseHealth{LeaseID: l.LeaseID, Resource: l.Resource, Status: l.Status, RemainingSecs: remaining, FencingToken: l.FencingToken, CanRenew: !model.IsTerminal(l.Status) && remaining >= 0}, nil
}

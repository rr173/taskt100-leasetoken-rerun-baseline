package lease

type RenewAdvice struct {
	LeaseID       string `json:"lease_id"`
	RemainingSecs int64  `json:"remaining_secs"`
	WindowOpen    bool   `json:"window_open"`
	Reason        string `json:"reason"`
}

// RenewAdvice mirrors the manager's renew-window rule without mutating state;
// clients can decide whether to retry now or defer without probing by trial.
func (m *Manager) RenewAdvice(id string) (RenewAdvice, error) {
	l, err := m.GetLease(id)
	if err != nil {
		return RenewAdvice{}, err
	}
	remaining := l.ExpiresAt - m.now()
	threshold := int64(float64(l.TTLSeconds) * RenewWindowFraction)
	open := !isTerminalStatus(l.Status) && remaining >= 0 && remaining <= threshold
	reason := "renew window is closed"
	if open {
		reason = "renew window is open"
	}
	return RenewAdvice{LeaseID: id, RemainingSecs: remaining, WindowOpen: open, Reason: reason}, nil
}

func isTerminalStatus(status string) bool { return status == "released" || status == "expired" }

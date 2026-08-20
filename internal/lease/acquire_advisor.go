package lease

type AcquireAdvice struct {
	Resource     string `json:"resource"`
	Allowed      bool   `json:"allowed"`
	Locked       bool   `json:"locked"`
	CurrentToken int64  `json:"current_token"`
	Reason       string `json:"reason"`
}

func (m *Manager) AcquireAdvice(resource string) (AcquireAdvice, error) {
	row, found, err := m.GetResource(resource)
	if err != nil {
		return AcquireAdvice{}, err
	}
	if !found {
		return AcquireAdvice{Resource: resource, Allowed: true, Reason: "resource will be created"}, nil
	}
	if row.Locked {
		return AcquireAdvice{Resource: resource, Locked: true, CurrentToken: row.FencingToken, Reason: ErrResourceLocked.Error()}, nil
	}
	return AcquireAdvice{Resource: resource, Allowed: true, CurrentToken: row.FencingToken, Reason: "resource is available for an acquisition attempt"}, nil
}

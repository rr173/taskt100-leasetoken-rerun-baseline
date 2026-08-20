package lease

import "task100-leasetoken/internal/model"

type HolderReport struct {
	Holder       string `json:"holder"`
	Active       int    `json:"active"`
	Terminal     int    `json:"terminal"`
	LatestExpiry int64  `json:"latest_expiry"`
}

func (m *Manager) HolderReport(holder string) (HolderReport, error) {
	leases, err := m.ListLeases(model.ListLeasesFilter{Holder: holder, Limit: 0})
	if err != nil {
		return HolderReport{}, err
	}
	report := HolderReport{Holder: holder}
	for _, item := range leases {
		if item.Status == model.StatusActive && item.ExpiresAt > m.now() {
			report.Active++
		} else {
			report.Terminal++
		}
		if item.ExpiresAt > report.LatestExpiry {
			report.LatestExpiry = item.ExpiresAt
		}
	}
	return report, nil
}

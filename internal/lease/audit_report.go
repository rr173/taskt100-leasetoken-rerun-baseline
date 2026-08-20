package lease

import "task100-leasetoken/internal/model"

type AuditReport struct {
	TotalByAction map[string]int `json:"total_by_action"`
	LastSequence  int64          `json:"last_sequence"`
}

func (m *Manager) AuditReport(resource string) (AuditReport, error) {
	entries, err := m.ListAudit(model.ListAuditFilter{Resource: resource, Limit: 0})
	if err != nil {
		return AuditReport{}, err
	}
	report := AuditReport{TotalByAction: map[string]int{}}
	for _, entry := range entries {
		report.TotalByAction[entry.Action]++
		if entry.ID > report.LastSequence {
			report.LastSequence = entry.ID
		}
	}
	return report, nil
}

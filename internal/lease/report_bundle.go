package lease

// ReportBundle is the read-only operational snapshot returned by the report
// endpoint. Each component is produced by the same Manager/store instance.
type ReportBundle struct {
	Resources ResourceReport `json:"resources"`
	Audit     AuditReport    `json:"audit"`
}

func (m *Manager) ReportBundle(resource string) (ReportBundle, error) {
	resources, err := m.ResourceReport()
	if err != nil {
		return ReportBundle{}, err
	}
	audit, err := m.AuditReport(resource)
	if err != nil {
		return ReportBundle{}, err
	}
	return ReportBundle{Resources: resources, Audit: audit}, nil
}

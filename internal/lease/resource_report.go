package lease

type ResourceReport struct {
	Total  int `json:"total"`
	Locked int `json:"locked"`
	Open   int `json:"open"`
}

func (m *Manager) ResourceReport() (ResourceReport, error) {
	resources, err := m.ListResources()
	if err != nil {
		return ResourceReport{}, err
	}
	report := ResourceReport{Total: len(resources)}
	for _, resource := range resources {
		if resource.Locked {
			report.Locked++
		} else {
			report.Open++
		}
	}
	return report, nil
}

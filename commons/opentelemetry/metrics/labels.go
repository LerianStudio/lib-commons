package metrics

// WithOrganizationLabels generates a map of labels with the organization ID
func (f *MetricsFactory) WithOrganizationLabels(organizationID string) map[string]string {
	return map[string]string{
		"organization_id": organizationID,
	}
}

// WithLedgerLabels generates a map of labels with the organization ID and ledger ID
func (f *MetricsFactory) WithLedgerLabels(organizationID, ledgerID string) map[string]string {
	labels := f.WithOrganizationLabels(organizationID)
	labels["ledger_id"] = ledgerID

	return labels
}

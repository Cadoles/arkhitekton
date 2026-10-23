package mse

// The actions the plugin records in the dashboard's audit log. Their values are those the core
// wrote before the plugin had its own repository: a log is read across versions.
const (
	AuditReleasePromote = "release.promote"
	AuditSignoffSet     = "release.signoff"
	AuditSignoffClear   = "release.signoff.clear"
)

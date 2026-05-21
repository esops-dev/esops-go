package types

// OnErrorPolicy is the operator-facing --on-error value. Multi-item
// destructive commands carry one of these in their result so a
// captured JSON record reads "skip" or "rollback" rather than the bare
// per-item statuses, which would lose intent.
type OnErrorPolicy string

const (
	// OnErrorAbort is the default — stop on the first failure.
	OnErrorAbort OnErrorPolicy = "abort"
	// OnErrorSkip records each failure and continues with the next item.
	OnErrorSkip OnErrorPolicy = "skip"
	// OnErrorRollback undoes already-applied items when a later item
	// fails. Only valid for reversible operations; irreversible
	// commands refuse the flag with a usage error before dispatch.
	OnErrorRollback OnErrorPolicy = "rollback"
)

// OnErrorItemStatus is the per-item outcome on a multi-item run.
type OnErrorItemStatus string

const (
	OnErrorStatusSucceeded      OnErrorItemStatus = "succeeded"
	OnErrorStatusFailed         OnErrorItemStatus = "failed"
	OnErrorStatusRolledBack     OnErrorItemStatus = "rolled-back"
	OnErrorStatusRollbackFailed OnErrorItemStatus = "rollback-failed"
)

// OnErrorItemResult is one item's recorded outcome inside an
// OnErrorSummary.
type OnErrorItemResult struct {
	Name   string            `json:"name"             yaml:"name"`
	Status OnErrorItemStatus `json:"status"           yaml:"status"`
	Error  string            `json:"error,omitempty"  yaml:"error,omitempty"`
}

// OnErrorSummary aggregates per-item outcomes for a multi-item
// command run. Commands embed *OnErrorSummary (pointer, omitempty) so
// the field disappears from JSON / YAML output when the operator did
// not pass --on-error.
type OnErrorSummary struct {
	Policy         OnErrorPolicy       `json:"policy"                     yaml:"policy"`
	Items          []OnErrorItemResult `json:"items"                      yaml:"items"`
	Succeeded      int                 `json:"succeeded"                  yaml:"succeeded"`
	Failed         int                 `json:"failed"                     yaml:"failed"`
	RolledBack     int                 `json:"rolled_back,omitempty"      yaml:"rolled_back,omitempty"`
	RollbackFailed int                 `json:"rollback_failed,omitempty"  yaml:"rollback_failed,omitempty"`
}

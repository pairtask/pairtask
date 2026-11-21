package store

import "time"

// SetMigrationHook is test-only; callers restore it and never run in parallel.
func SetMigrationHook(h func(step string) error) (restore func()) {
	old := migrationHook
	migrationHook = h
	return func() { migrationHook = old }
}

const EpochSchemaStep = epochSchemaStep

var MigrationRebuildTables = migrationRebuildTables

func MigrationSteps() []string {
	steps := []string{"begin", "shape"}
	for _, table := range migrationRebuildTables {
		steps = append(steps, "rebuild:"+table)
	}
	return append(steps, "indexes", "columns", "ledger", "foreign_key_check", "marker", "commit")
}

// SetIdentityClock is test-only: the local clock identity admission reads.
func SetIdentityClock(now func() time.Time) (restore func()) {
	old := identityNow
	identityNow = now
	return func() { identityNow = old }
}

// SetAdmissionHook is test-only: runs at "before-message", "after-message",
// "after-evidence" and "before-commit"; an error aborts the admission.
func SetAdmissionHook(h func(stage string) error) (restore func()) {
	old := admissionHook
	admissionHook = h
	return func() { admissionHook = old }
}

// SetIdentityExchange is test-only: turns the identity phase (peer version
// 13) on or off for stores in this process.
func SetIdentityExchange(on bool) (restore func()) {
	old := identityExchange
	identityExchange = on
	return func() { identityExchange = old }
}

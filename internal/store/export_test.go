package store

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

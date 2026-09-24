//go:build updatetest

package store

import "embed"

//go:embed migrations_updatetest/*.sql
var updateTestMigrations embed.FS

func init() {
	migrationSources = append(migrationSources, mustSub(updateTestMigrations, "migrations_updatetest"))
}

package database

import (
	"errors"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // registers the "pgx5" scheme
	_ "github.com/golang-migrate/migrate/v4/source/file"     // registers the "file" source
)

// RunMigrations applies all up-migrations found in migrationsDir (an absolute
// filesystem path) to the database at databaseURL. It is idempotent: a
// no-change run is treated as success, so it is safe to call on every startup.
func RunMigrations(databaseURL, migrationsDir string) error {
	// golang-migrate's pgx/v5 driver is registered under the "pgx5" scheme.
	pgxURL := databaseURL
	for _, prefix := range []string{"postgres://", "postgresql://"} {
		if strings.HasPrefix(pgxURL, prefix) {
			pgxURL = "pgx5://" + strings.TrimPrefix(pgxURL, prefix)
			break
		}
	}

	m, err := migrate.New("file://"+migrationsDir, pgxURL)
	if err != nil {
		return err
	}
	defer m.Close()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

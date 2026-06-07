package database

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // registers the "pgx5" scheme
	_ "github.com/golang-migrate/migrate/v4/source/file"     // registers the "file" source
)

// pgxURL rewrites a postgres:// / postgresql:// DSN to the "pgx5" scheme that
// golang-migrate's pgx/v5 driver is registered under.
func pgxURL(databaseURL string) string {
	for _, prefix := range []string{"postgres://", "postgresql://"} {
		if rest, ok := strings.CutPrefix(databaseURL, prefix); ok {
			return "pgx5://" + rest
		}
	}
	return databaseURL
}

// RunMigrations applies all up-migrations found in migrationsDir (an absolute
// filesystem path) to the database at databaseURL. It is idempotent: a
// no-change run is treated as success, so it is safe to call repeatedly. This is
// the *applier* — run it from the dedicated migrate Job/command, not from the
// app's normal startup path (app pods use EnsureMigrated instead).
func RunMigrations(databaseURL, migrationsDir string) error {
	m, err := migrate.New("file://"+migrationsDir, pgxURL(databaseURL))
	if err != nil {
		return err
	}
	defer func() { _, _ = m.Close() }()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

// EnsureMigrated is a read-only guard for the app's startup path: it verifies the
// database schema is at (or beyond) the latest migration bundled in migrationsDir
// WITHOUT applying any DDL. It returns an error if the schema is dirty or behind,
// so an app pod refuses to run against an un-migrated database. Migrations are
// applied separately (RunMigrations, via the migrate Job).
func EnsureMigrated(databaseURL, migrationsDir string) error {
	latest, err := latestMigrationVersion(migrationsDir)
	if err != nil {
		return err
	}

	m, err := migrate.New("file://"+migrationsDir, pgxURL(databaseURL))
	if err != nil {
		return err
	}
	defer func() { _, _ = m.Close() }()

	version, dirty, err := m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		return fmt.Errorf("database has no migrations applied; expected version %d (run the migrate job)", latest)
	}
	if err != nil {
		return err
	}
	if dirty {
		return fmt.Errorf("database schema is dirty at version %d; a previous migration failed and needs repair", version)
	}
	if version < latest {
		return fmt.Errorf("database schema is behind: at version %d, need %d (run the migrate job before deploying this build)", version, latest)
	}
	return nil
}

// latestMigrationVersion returns the highest NNNN version among the *.up.sql
// files in dir.
func latestMigrationVersion(dir string) (uint, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("read migrations dir %q: %w", dir, err)
	}
	re := regexp.MustCompile(`^(\d+)_.*\.up\.sql$`)
	var max uint
	found := false
	for _, e := range entries {
		mch := re.FindStringSubmatch(filepath.Base(e.Name()))
		if mch == nil {
			continue
		}
		n, err := strconv.ParseUint(mch[1], 10, 64)
		if err != nil {
			continue
		}
		found = true
		if uint(n) > max {
			max = uint(n)
		}
	}
	if !found {
		return 0, fmt.Errorf("no *.up.sql migrations found in %q", dir)
	}
	return max, nil
}

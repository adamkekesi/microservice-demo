//go:build integration

// Package integration holds full-stack tests for the Shipment service. Inventory
// is mocked with an httptest.Server so the suite is self-contained (plan §8.3).
//
// By default a throwaway Postgres is started via Testcontainers (needs Docker).
// If TEST_DATABASE_URL is set, that database is used instead.
package integration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/adamkekesi/microservice-demo/platform/database"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"gorm.io/gorm"
)

var testDB *gorm.DB

func TestMain(m *testing.M) {
	ctx := context.Background()
	dsn := os.Getenv("TEST_DATABASE_URL")
	cleanup := func() {}

	if dsn == "" {
		container, err := postgres.Run(ctx, "postgres:16-alpine",
			postgres.WithDatabase("shipment_db"),
			postgres.WithUsername("test"),
			postgres.WithPassword("test"),
			testcontainers.WithWaitStrategy(
				wait.ForLog("database system is ready to accept connections").
					WithOccurrence(2).WithStartupTimeout(90*time.Second),
			),
		)
		if err != nil {
			fmt.Println("failed to start postgres container:", err)
			os.Exit(1)
		}
		dsn, err = container.ConnectionString(ctx, "sslmode=disable")
		if err != nil {
			fmt.Println("failed to get connection string:", err)
			os.Exit(1)
		}
		cleanup = func() { _ = container.Terminate(ctx) }
	}

	migrationsDir, _ := filepath.Abs("../../migrations")
	if err := database.RunMigrations(dsn, migrationsDir); err != nil {
		fmt.Println("failed to run migrations:", err)
		cleanup()
		os.Exit(1)
	}

	var err error
	if testDB, err = database.Connect("shipment-test", dsn); err != nil {
		fmt.Println("failed to connect gorm:", err)
		cleanup()
		os.Exit(1)
	}

	code := m.Run()
	cleanup()
	os.Exit(code)
}

func truncate(t *testing.T, tables ...string) {
	t.Helper()
	for _, table := range tables {
		if res := testDB.Exec("TRUNCATE TABLE " + table + " RESTART IDENTITY CASCADE"); res.Error != nil {
			t.Fatalf("truncate %s: %v", table, res.Error)
		}
	}
}

// Package database provides the traced GORM connection shared by all services
// and a programmatic migration runner. See technical plan §5.3.
package database

import (
	"errors"
	"sync"
	"time"

	sqltrace "github.com/DataDog/dd-trace-go/contrib/database/sql/v2"
	gormtrace "github.com/DataDog/dd-trace-go/contrib/gorm.io/gorm.v1/v2"
	"github.com/jackc/pgx/v5/stdlib"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// pgx's stdlib package registers the bare "pgx" driver in its init(); the
// dd-trace wrapper must register under a distinct name to avoid a duplicate
// database/sql registration panic.
const tracedDriverName = "pgx-dd"

var registerOnce sync.Once

// Connect opens a traced GORM connection. The pgx driver is wrapped by the
// dd-trace sql integration, GORM is opened over the traced *sql.DB, and the
// GORM trace plugin emits a span per query (record-not-found is not an error).
// Repositories MUST call db.WithContext(ctx) so query spans attach to the
// inbound request span — without it the showcase distributed trace breaks.
func Connect(serviceName, dsn string) (*gorm.DB, error) {
	registerOnce.Do(func() {
		sqltrace.Register(tracedDriverName, &stdlib.Driver{}, sqltrace.WithService(serviceName+"-db"))
	})

	sqlDB, err := sqltrace.Open(tracedDriverName, dsn)
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(10)
	sqlDB.SetMaxIdleConns(5)
	sqlDB.SetConnMaxLifetime(time.Hour)

	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{
		TranslateError: true, // surfaces gorm.ErrDuplicatedKey for unique violations
		Logger:         gormlogger.Default.LogMode(gormlogger.Warn),
		NowFunc:        func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		return nil, err
	}

	if err := db.Use(gormtrace.NewTracePlugin(
		gormtrace.WithErrorCheck(func(err error) bool {
			return !errors.Is(err, gorm.ErrRecordNotFound)
		}),
	)); err != nil {
		return nil, err
	}
	return db, nil
}

// Ping verifies connectivity for readiness probes.
func Ping(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Ping()
}

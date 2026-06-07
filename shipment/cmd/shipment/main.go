// Command shipment runs the Shipment service: the saga orchestrator that calls
// Inventory over HTTP. See entrypoint sequence in technical plan §3.
package main

import (
	"context"
	"os"
	"path/filepath"

	"github.com/adamkekesi/microservice-demo/platform/authn"
	"github.com/adamkekesi/microservice-demo/platform/config"
	"github.com/adamkekesi/microservice-demo/platform/database"
	"github.com/adamkekesi/microservice-demo/platform/httpserver"
	"github.com/adamkekesi/microservice-demo/platform/observability"
	"github.com/adamkekesi/microservice-demo/shipment/internal/client"
	shiphttp "github.com/adamkekesi/microservice-demo/shipment/internal/http"
	"github.com/adamkekesi/microservice-demo/shipment/internal/repository"
	"github.com/adamkekesi/microservice-demo/shipment/internal/service"
	"go.uber.org/zap"
)

func main() {
	dsn := config.MustString("DATABASE_URL")

	logger := observability.NewLogger()
	defer func() { _ = logger.Sync() }()

	// `shipment migrate` applies pending migrations and exits — run from the
	// dedicated migrate Job, decoupled from the app rollout. App pods never apply
	// DDL; they only verify the schema via EnsureMigrated below. Kept above the
	// other MustString reads so the migrate Job needs only DATABASE_URL.
	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		if err := database.RunMigrations(dsn, migrationsDir()); err != nil {
			logger.Fatal("run migrations", zap.Error(err))
		}
		logger.Info("migrations applied")
		return
	}

	port := config.String("PORT", "8003")
	jwksURL := config.MustString("AUTH_JWKS_URL")
	issuer := config.String("JWT_ISSUER", "auth-service")
	jwksTTL := config.Seconds("JWKS_CACHE_TTL_SECONDS", 600)
	inventoryBaseURL := config.MustString("INVENTORY_BASE_URL")
	inventoryTimeout := config.Millis("INVENTORY_TIMEOUT_MS", 5000)
	serviceName := config.String("DD_SERVICE", "shipment-service")

	observability.InitTracer()
	defer observability.StopTracer()
	defer observability.InitProfiler()()

	metrics, err := observability.NewMetrics()
	if err != nil {
		logger.Warn("metrics client init failed (continuing without metrics)", zap.Error(err))
	}
	defer metrics.Close()

	db, err := database.Connect(serviceName, dsn)
	if err != nil {
		logger.Fatal("connect database", zap.Error(err))
	}
	if err := database.EnsureMigrated(dsn, migrationsDir()); err != nil {
		logger.Fatal("database schema not migrated", zap.Error(err))
	}

	verifier := authn.NewVerifier(jwksURL, issuer, jwksTTL)
	if err := verifier.Prime(context.Background()); err != nil {
		logger.Warn("initial JWKS fetch failed (will retry lazily)", zap.Error(err))
	}

	inventoryClient := client.NewHTTPClient(inventoryBaseURL, inventoryTimeout)
	svc := service.New(repository.New(db), inventoryClient, metrics)

	router := shiphttp.NewRouter(shiphttp.Deps{
		Service:     svc,
		Verifier:    verifier,
		DB:          db,
		Logger:      logger,
		ServiceName: serviceName,
	})

	logger.Info("shipment service starting", zap.String("port", port), zap.String("inventory", inventoryBaseURL))
	if err := httpserver.Run(router, port, nil); err != nil {
		logger.Fatal("http server", zap.Error(err))
	}
}

func migrationsDir() string {
	dir := config.String("MIGRATIONS_DIR", "migrations")
	if abs, err := filepath.Abs(dir); err == nil {
		return abs
	}
	return dir
}

// Command auth runs the Auth service: it mints RS256 JWTs and publishes its
// public key via JWKS. See the entrypoint sequence in technical plan §3.
package main

import (
	"context"
	"crypto/rsa"
	"os"
	"path/filepath"

	authhttp "github.com/adamkekesi/microservice-demo/auth/internal/http"
	"github.com/adamkekesi/microservice-demo/auth/internal/repository"
	"github.com/adamkekesi/microservice-demo/auth/internal/service"
	"github.com/adamkekesi/microservice-demo/platform/authn"
	"github.com/adamkekesi/microservice-demo/platform/config"
	"github.com/adamkekesi/microservice-demo/platform/database"
	"github.com/adamkekesi/microservice-demo/platform/httpserver"
	"github.com/adamkekesi/microservice-demo/platform/observability"
	"go.uber.org/zap"
)

func main() {
	port := config.String("PORT", "8001")
	dsn := config.MustString("DATABASE_URL")
	issuer := config.String("JWT_ISSUER", "auth-service")
	kid := config.String("JWT_KEY_ID", "auth-key-1")
	ttl := config.Seconds("JWT_TTL_SECONDS", 900)
	serviceName := config.String("DD_SERVICE", "auth-service")

	observability.InitTracer()
	defer observability.StopTracer()
	defer observability.InitProfiler()()
	logger := observability.NewLogger()
	defer func() { _ = logger.Sync() }()

	db, err := database.Connect(serviceName, dsn)
	if err != nil {
		logger.Fatal("connect database", zap.Error(err))
	}
	if err := database.RunMigrations(dsn, migrationsDir()); err != nil {
		logger.Fatal("run migrations", zap.Error(err))
	}

	userRepo := repository.NewUserRepository(db)
	keyRepo := repository.NewSigningKeyRepository(db)

	privPEM := config.String("JWT_PRIVATE_KEY", "")
	if privPEM == "" {
		if path := config.String("JWT_PRIVATE_KEY_PATH", ""); path != "" {
			b, rerr := os.ReadFile(path)
			if rerr != nil {
				logger.Fatal("read private key file", zap.Error(rerr))
			}
			privPEM = string(b)
		}
	}

	ctx := context.Background()
	signer, err := service.ResolveSigner(ctx, keyRepo, privPEM, kid, issuer, ttl)
	if err != nil {
		logger.Fatal("resolve signing key", zap.Error(err))
	}

	svc := service.New(userRepo, signer)
	if err := svc.EnsureAdmin(ctx, config.String("ADMIN_EMAIL", ""), config.String("ADMIN_PASSWORD", "")); err != nil {
		logger.Error("ensure seed admin", zap.Error(err))
	}

	// Auth verifies its own tokens locally with its own public key.
	verifier := authn.NewLocalVerifier(issuer, map[string]*rsa.PublicKey{
		signer.KeyID(): signer.PublicKey(),
	})

	router := authhttp.NewRouter(authhttp.Deps{
		Service:     svc,
		Verifier:    verifier,
		DB:          db,
		Logger:      logger,
		ServiceName: serviceName,
	})

	logger.Info("auth service starting", zap.String("port", port), zap.String("kid", signer.KeyID()))
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

// Package http assembles the Auth service's Gin router. NewRouter is callable
// from both main and integration tests (technical plan §2.3).
package http

import (
	"net/http"

	gintrace "github.com/DataDog/dd-trace-go/contrib/gin-gonic/gin/v2"
	"github.com/adamkekesi/microservice-demo/auth/internal/handler"
	"github.com/adamkekesi/microservice-demo/auth/internal/service"
	"github.com/adamkekesi/microservice-demo/platform/authn"
	"github.com/adamkekesi/microservice-demo/platform/database"
	"github.com/adamkekesi/microservice-demo/platform/observability"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// Deps are the dependencies needed to build the router.
type Deps struct {
	Service     *service.Service
	Verifier    *authn.Verifier
	DB          *gorm.DB
	Logger      *zap.Logger
	ServiceName string
}

// NewRouter wires middleware (tracing → recovery → request logger → auth) and
// routes.
func NewRouter(d Deps) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gintrace.Middleware(d.ServiceName))
	r.Use(gin.Recovery())
	r.Use(observability.RequestLogger(d.Logger))

	h := handler.New(d.Service)

	// Public, no auth.
	r.GET("/health", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })
	r.GET("/ready", func(c *gin.Context) {
		if err := database.Ping(d.DB); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not ready", "reason": "database"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	registerDocs(r) // GET /docs (Swagger UI) + GET /openapi.yaml
	r.GET("/.well-known/jwks.json", h.JWKS)
	r.POST("/auth/register", h.Register)
	r.POST("/auth/login", h.Login)

	// Authenticated.
	authed := r.Group("")
	authed.Use(authn.RequireAuth(d.Verifier))
	authed.GET("/auth/me", h.Me)
	authed.POST("/auth/users", authn.RequireRole(authn.RoleAdmin), h.CreateUser)
	// Bulk retention sweep registered before the /:id route so it isn't shadowed.
	authed.DELETE("/auth/users", authn.RequireRole(authn.RoleAdmin), h.PurgeUsers)
	authed.DELETE("/auth/users/:id", authn.RequireRole(authn.RoleAdmin), h.DeleteUser)

	return r
}

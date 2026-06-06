// Package http assembles the Inventory service's Gin router.
package http

import (
	"net/http"

	gintrace "github.com/DataDog/dd-trace-go/contrib/gin-gonic/gin/v2"
	"github.com/adamkekesi/microservice-demo/inventory/internal/handler"
	"github.com/adamkekesi/microservice-demo/inventory/internal/service"
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

// NewRouter wires middleware and routes. Administration endpoints require the
// operator/admin role; reads and reservations require only authentication
// (ownership is enforced in the service layer).
func NewRouter(d Deps) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gintrace.Middleware(d.ServiceName))
	r.Use(gin.Recovery())
	r.Use(observability.RequestLogger(d.Logger))

	h := handler.New(d.Service)

	r.GET("/health", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })
	r.GET("/ready", func(c *gin.Context) {
		if err := database.Ping(d.DB); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not ready", "reason": "database"})
			return
		}
		if !d.Verifier.Ready() {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not ready", "reason": "jwks"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	registerDocs(r) // GET /docs (Swagger UI) + GET /openapi.yaml

	authed := r.Group("")
	authed.Use(authn.RequireAuth(d.Verifier))

	// Administration: operator | admin.
	admin := authed.Group("")
	admin.Use(authn.RequireRole(authn.RoleOperator, authn.RoleAdmin))
	admin.POST("/warehouses", h.CreateWarehouse)
	admin.POST("/items", h.CreateItem)
	admin.PUT("/stock", h.SetStock)

	// Reads: any authenticated.
	authed.GET("/warehouses", h.ListWarehouses)
	authed.GET("/items", h.ListItems)
	authed.GET("/stock", h.GetStock)

	// Reservations: any authenticated; ownership enforced in the service.
	authed.POST("/reservations", h.CreateReservation)
	authed.GET("/reservations/:id", h.GetReservation)
	authed.POST("/reservations/:id/commit", h.CommitReservation)
	authed.POST("/reservations/:id/release", h.ReleaseReservation)
	// Bulk retention sweep (operator/admin, enforced in the service) registered
	// before /:id so DELETE /reservations isn't shadowed by the param route.
	authed.DELETE("/reservations", h.PurgeReservations)
	authed.DELETE("/reservations/:id", h.DeleteReservation)

	return r
}

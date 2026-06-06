// Package http assembles the Shipment service's Gin router.
package http

import (
	"net/http"

	gintrace "github.com/DataDog/dd-trace-go/contrib/gin-gonic/gin/v2"
	"github.com/adamkekesi/microservice-demo/platform/authn"
	"github.com/adamkekesi/microservice-demo/platform/database"
	"github.com/adamkekesi/microservice-demo/platform/observability"
	"github.com/adamkekesi/microservice-demo/shipment/internal/handler"
	"github.com/adamkekesi/microservice-demo/shipment/internal/service"
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

// NewRouter wires middleware and routes. All shipment routes require
// authentication; ownership is enforced in the service layer.
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

	authed := r.Group("")
	authed.Use(authn.RequireAuth(d.Verifier))
	authed.POST("/shipments", h.CreateShipment)
	authed.GET("/shipments", h.ListShipments)
	authed.GET("/shipments/:id", h.GetShipment)
	authed.POST("/shipments/:id/confirm", h.ConfirmShipment)
	authed.POST("/shipments/:id/cancel", h.CancelShipment)

	return r
}

// Package handler holds the Inventory Gin HTTP handlers.
package handler

import (
	"net/http"

	"github.com/adamkekesi/microservice-demo/inventory/internal/model"
	"github.com/adamkekesi/microservice-demo/inventory/internal/service"
	"github.com/adamkekesi/microservice-demo/platform/apperror"
	"github.com/gin-gonic/gin"
)

// Handler bundles the Inventory HTTP handlers.
type Handler struct {
	svc *service.Service
}

// New builds an Inventory handler.
func New(svc *service.Service) *Handler { return &Handler{svc: svc} }

func bindJSON(c *gin.Context, dst any) bool {
	if err := c.ShouldBindJSON(dst); err != nil {
		apperror.Respond(c, apperror.Validation("invalid request body", map[string]any{"body": err.Error()}))
		return false
	}
	return true
}

func (h *Handler) CreateWarehouse(c *gin.Context) {
	var req model.CreateWarehouseRequest
	if !bindJSON(c, &req) {
		return
	}
	resp, err := h.svc.CreateWarehouse(c.Request.Context(), req)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(http.StatusCreated, resp)
}

func (h *Handler) ListWarehouses(c *gin.Context) {
	resp, err := h.svc.ListWarehouses(c.Request.Context())
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

func (h *Handler) CreateItem(c *gin.Context) {
	var req model.CreateItemRequest
	if !bindJSON(c, &req) {
		return
	}
	resp, err := h.svc.CreateItem(c.Request.Context(), req)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(http.StatusCreated, resp)
}

func (h *Handler) ListItems(c *gin.Context) {
	resp, err := h.svc.ListItems(c.Request.Context())
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

func (h *Handler) SetStock(c *gin.Context) {
	var req model.SetStockRequest
	if !bindJSON(c, &req) {
		return
	}
	resp, err := h.svc.SetStock(c.Request.Context(), req)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

func (h *Handler) GetStock(c *gin.Context) {
	resp, err := h.svc.GetStock(c.Request.Context(), c.Query("warehouse_id"), c.Query("item_id"))
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

func (h *Handler) CreateReservation(c *gin.Context) {
	var req model.CreateReservationRequest
	if !bindJSON(c, &req) {
		return
	}
	resp, created, err := h.svc.CreateReservation(c.Request.Context(), req)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	status := http.StatusCreated
	if !created {
		status = http.StatusOK // idempotent hit
	}
	c.JSON(status, resp)
}

func (h *Handler) GetReservation(c *gin.Context) {
	resp, err := h.svc.GetReservation(c.Request.Context(), c.Param("id"))
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

func (h *Handler) CommitReservation(c *gin.Context) {
	resp, err := h.svc.CommitReservation(c.Request.Context(), c.Param("id"))
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

func (h *Handler) ReleaseReservation(c *gin.Context) {
	resp, err := h.svc.ReleaseReservation(c.Request.Context(), c.Param("id"))
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

func (h *Handler) DeleteReservation(c *gin.Context) {
	if err := h.svc.DeleteReservation(c.Request.Context(), c.Param("id")); err != nil {
		apperror.Respond(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) PurgeReservations(c *gin.Context) {
	resp, err := h.svc.PurgeReservations(c.Request.Context(), c.Query("before"))
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// Package handler holds the Shipment Gin HTTP handlers.
package handler

import (
	"net/http"
	"strconv"

	"github.com/adamkekesi/microservice-demo/platform/apperror"
	"github.com/adamkekesi/microservice-demo/shipment/internal/model"
	"github.com/adamkekesi/microservice-demo/shipment/internal/service"
	"github.com/gin-gonic/gin"
)

// Handler bundles the Shipment HTTP handlers.
type Handler struct {
	svc *service.Service
}

// New builds a Shipment handler.
func New(svc *service.Service) *Handler { return &Handler{svc: svc} }

func bindJSON(c *gin.Context, dst any) bool {
	if err := c.ShouldBindJSON(dst); err != nil {
		apperror.Respond(c, apperror.Validation("invalid request body", map[string]any{"body": err.Error()}))
		return false
	}
	return true
}

func (h *Handler) CreateShipment(c *gin.Context) {
	var req model.CreateShipmentRequest
	if !bindJSON(c, &req) {
		return
	}
	resp, err := h.svc.CreateShipment(c.Request.Context(), c.GetHeader("Authorization"), req)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(http.StatusCreated, resp)
}

func (h *Handler) ConfirmShipment(c *gin.Context) {
	resp, err := h.svc.ConfirmShipment(c.Request.Context(), c.GetHeader("Authorization"), c.Param("id"))
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

func (h *Handler) CancelShipment(c *gin.Context) {
	resp, err := h.svc.CancelShipment(c.Request.Context(), c.GetHeader("Authorization"), c.Param("id"))
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

func (h *Handler) GetShipment(c *gin.Context) {
	resp, err := h.svc.GetShipment(c.Request.Context(), c.Param("id"))
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

func (h *Handler) DeleteShipment(c *gin.Context) {
	if err := h.svc.DeleteShipment(c.Request.Context(), c.Param("id")); err != nil {
		apperror.Respond(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) PurgeShipments(c *gin.Context) {
	resp, err := h.svc.PurgeShipments(c.Request.Context(), c.Query("before"))
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

func (h *Handler) ListShipments(c *gin.Context) {
	all := c.Query("all") == "true"
	limit, _ := strconv.Atoi(c.Query("limit"))
	offset, _ := strconv.Atoi(c.Query("offset"))
	resp, err := h.svc.ListShipments(c.Request.Context(), all, limit, offset)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

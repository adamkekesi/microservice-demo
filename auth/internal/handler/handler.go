// Package handler holds the Gin HTTP handlers for the Auth service. Handlers
// deal only with HTTP concerns (binding, status codes) and delegate to service.
package handler

import (
	"net/http"

	"github.com/adamkekesi/microservice-demo/auth/internal/model"
	"github.com/adamkekesi/microservice-demo/auth/internal/service"
	"github.com/adamkekesi/microservice-demo/platform/apperror"
	"github.com/adamkekesi/microservice-demo/platform/authn"
	"github.com/gin-gonic/gin"
)

// Handler bundles the Auth HTTP handlers.
type Handler struct {
	svc *service.Service
}

// New builds an Auth handler.
func New(svc *service.Service) *Handler { return &Handler{svc: svc} }

// JWKS publishes the public verifying key(s). Public, no auth.
func (h *Handler) JWKS(c *gin.Context) {
	signer := h.svc.Signer()
	c.JSON(http.StatusOK, authn.JWKS{
		Keys: []authn.JWK{authn.PublicJWK(signer.PublicKey(), signer.KeyID())},
	})
}

// Register creates a customer account. Public.
func (h *Handler) Register(c *gin.Context) {
	var req model.RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		apperror.Respond(c, apperror.Validation("invalid request body", map[string]any{"body": err.Error()}))
		return
	}
	resp, err := h.svc.Register(c.Request.Context(), req)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(http.StatusCreated, resp)
}

// Login authenticates and returns an access token. Public.
func (h *Handler) Login(c *gin.Context) {
	var req model.LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		apperror.Respond(c, apperror.Validation("invalid request body", map[string]any{"body": err.Error()}))
		return
	}
	resp, err := h.svc.Login(c.Request.Context(), req)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// Me returns the authenticated user's profile.
func (h *Handler) Me(c *gin.Context) {
	claims, ok := authn.GinClaims(c)
	if !ok {
		apperror.Respond(c, apperror.Unauthenticated(""))
		return
	}
	resp, err := h.svc.Me(c.Request.Context(), claims.Subject())
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// CreateUser creates a user with an explicit role. Admin only.
func (h *Handler) CreateUser(c *gin.Context) {
	var req model.CreateUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		apperror.Respond(c, apperror.Validation("invalid request body", map[string]any{"body": err.Error()}))
		return
	}
	resp, err := h.svc.CreateUser(c.Request.Context(), req)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(http.StatusCreated, resp)
}

// DeleteUser removes a user by id. Admin only.
func (h *Handler) DeleteUser(c *gin.Context) {
	if err := h.svc.DeleteUser(c.Request.Context(), c.Param("id")); err != nil {
		apperror.Respond(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// PurgeUsers bulk-deletes old customer accounts (retention sweep). Admin only.
func (h *Handler) PurgeUsers(c *gin.Context) {
	resp, err := h.svc.PurgeUsers(c.Request.Context(), c.Query("before"))
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// Package apperror defines the single error type used across all three
// services and the Gin responder that renders it in the standard envelope
// from the Feature Spec §2.5:
//
//	{ "error": { "code": "STRING_CODE", "message": "human readable", "details": { } } }
package apperror

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
)

// Standard top-level codes (Feature Spec §2.5). Service-specific 409 subcodes
// (e.g. INSUFFICIENT_STOCK, EMAIL_TAKEN) are passed directly as the Code on a
// Conflict and are defined where they are used.
const (
	CodeValidation            = "VALIDATION_ERROR"
	CodeUnauthenticated       = "UNAUTHENTICATED"
	CodeForbidden             = "FORBIDDEN"
	CodeNotFound              = "NOT_FOUND"
	CodeConflict              = "CONFLICT"
	CodeDependencyUnavailable = "DEPENDENCY_UNAVAILABLE"
	CodeInternal              = "INTERNAL"
)

// AppError is a domain error carrying everything needed to render the standard
// envelope: an HTTP status, a stable string code, a human message, and
// optional structured details.
type AppError struct {
	Status  int
	Code    string
	Message string
	Details map[string]any
	wrapped error
}

func (e *AppError) Error() string {
	if e.wrapped != nil {
		return e.Code + ": " + e.Message + ": " + e.wrapped.Error()
	}
	return e.Code + ": " + e.Message
}

func (e *AppError) Unwrap() error { return e.wrapped }

// WithDetails attaches structured details and returns the same error for
// fluent construction.
func (e *AppError) WithDetails(d map[string]any) *AppError {
	e.Details = d
	return e
}

// Wrap attaches an underlying cause (not serialised to clients).
func (e *AppError) Wrap(err error) *AppError {
	e.wrapped = err
	return e
}

// --- Constructors for the standard codes ---

func Validation(message string, details map[string]any) *AppError {
	return &AppError{Status: http.StatusBadRequest, Code: CodeValidation, Message: message, Details: details}
}

func Unauthenticated(message string) *AppError {
	if message == "" {
		message = "authentication required"
	}
	return &AppError{Status: http.StatusUnauthorized, Code: CodeUnauthenticated, Message: message}
}

func Forbidden(message string) *AppError {
	if message == "" {
		message = "you are not allowed to perform this action"
	}
	return &AppError{Status: http.StatusForbidden, Code: CodeForbidden, Message: message}
}

func NotFound(message string) *AppError {
	if message == "" {
		message = "resource not found"
	}
	return &AppError{Status: http.StatusNotFound, Code: CodeNotFound, Message: message}
}

// Conflict builds a 409 with a specific subcode (e.g. INSUFFICIENT_STOCK).
func Conflict(code, message string) *AppError {
	return &AppError{Status: http.StatusConflict, Code: code, Message: message}
}

// Unauthorized builds a 401 with a specific subcode (e.g. INVALID_CREDENTIALS).
func Unauthorized(code, message string) *AppError {
	return &AppError{Status: http.StatusUnauthorized, Code: code, Message: message}
}

func DependencyUnavailable(message string) *AppError {
	if message == "" {
		message = "a downstream dependency is unavailable"
	}
	return &AppError{Status: http.StatusBadGateway, Code: CodeDependencyUnavailable, Message: message}
}

func Internal(message string) *AppError {
	if message == "" {
		message = "an unexpected error occurred"
	}
	return &AppError{Status: http.StatusInternalServerError, Code: CodeInternal, Message: message}
}

// envelope mirrors the spec body shape.
type envelope struct {
	Error body `json:"error"`
}

type body struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// Respond writes err to the client in the standard envelope. Any error that is
// not an *AppError is treated as an INTERNAL 500 so internals never leak.
func Respond(c *gin.Context, err error) {
	var ae *AppError
	if !errors.As(err, &ae) {
		ae = Internal("")
		ae.wrapped = err
	}
	c.AbortWithStatusJSON(ae.Status, envelope{Error: body{
		Code:    ae.Code,
		Message: ae.Message,
		Details: ae.Details,
	}})
}

package authn

import (
	"strings"

	"github.com/adamkekesi/microservice-demo/platform/apperror"
	"github.com/gin-gonic/gin"
)

const ginClaimsKey = "authn.claims"

// RequireAuth verifies the bearer token, then stores the claims on both the
// Gin context and the request context (for the service layer). Invalid/expired
// tokens are rejected with 401 UNAUTHENTICATED.
func RequireAuth(v *Verifier) gin.HandlerFunc {
	const prefix = "Bearer "
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
			apperror.Respond(c, apperror.Unauthenticated("missing bearer token"))
			return
		}
		token := strings.TrimSpace(header[len(prefix):])
		claims, err := v.Verify(c.Request.Context(), token)
		if err != nil {
			apperror.Respond(c, apperror.Unauthenticated("invalid or expired token"))
			return
		}
		c.Request = c.Request.WithContext(ContextWithClaims(c.Request.Context(), claims))
		c.Set(ginClaimsKey, claims)
		c.Next()
	}
}

// GinClaims returns the verified claims set by RequireAuth.
func GinClaims(c *gin.Context) (*Claims, bool) {
	v, ok := c.Get(ginClaimsKey)
	if !ok {
		return nil, false
	}
	claims, ok := v.(*Claims)
	return claims, ok && claims != nil
}

// RequireRole aborts with 403 FORBIDDEN unless the caller holds one of roles.
// It must be chained after RequireAuth.
func RequireRole(roles ...Role) gin.HandlerFunc {
	allowed := make(map[Role]bool, len(roles))
	for _, r := range roles {
		allowed[r] = true
	}
	return func(c *gin.Context) {
		claims, ok := GinClaims(c)
		if !ok {
			apperror.Respond(c, apperror.Unauthenticated(""))
			return
		}
		if !allowed[claims.Role] {
			apperror.Respond(c, apperror.Forbidden(""))
			return
		}
		c.Next()
	}
}

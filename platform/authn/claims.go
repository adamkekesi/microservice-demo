// Package authn holds the shared authentication layer: JWT claims, the RSA
// signer (Auth side only), the JWKS verifier with in-memory caching (verifier
// side), and the Gin middleware. The private key lives only in Auth; verifiers
// only ever hold public keys fetched via JWKS (Feature Spec §2.3).
package authn

import (
	"context"

	"github.com/golang-jwt/jwt/v5"
)

// Role is the authorization role carried in the token.
type Role string

const (
	RoleCustomer Role = "customer"
	RoleOperator Role = "operator"
	RoleAdmin    Role = "admin"
)

// Valid reports whether r is a recognised role.
func (r Role) Valid() bool {
	switch r {
	case RoleCustomer, RoleOperator, RoleAdmin:
		return true
	}
	return false
}

// IsOperatorOrAdmin reports whether the role may act on any user's resources
// (Feature Spec §2.4).
func (r Role) IsOperatorOrAdmin() bool {
	return r == RoleOperator || r == RoleAdmin
}

// Claims are the JWT claims minted by Auth and verified downstream. sub, role,
// iat, exp are required; iss is verified by downstream services.
type Claims struct {
	Role Role `json:"role"`
	jwt.RegisteredClaims
}

// Subject returns the user id (sub) the token was issued for.
func (c *Claims) Subject() string {
	if c == nil {
		return ""
	}
	return c.RegisteredClaims.Subject
}

type claimsKey struct{}

// ContextWithClaims stores verified claims on a context for the service layer.
func ContextWithClaims(ctx context.Context, c *Claims) context.Context {
	return context.WithValue(ctx, claimsKey{}, c)
}

// ClaimsFrom retrieves verified claims previously stored on the context.
func ClaimsFrom(ctx context.Context) (*Claims, bool) {
	c, ok := ctx.Value(claimsKey{}).(*Claims)
	return c, ok && c != nil
}

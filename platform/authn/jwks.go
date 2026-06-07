package authn

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// JWK is a single public key in JWKS format; JWKS is the published set.
type JWK struct {
	Kty string `json:"kty"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// JWKS is the JSON Web Key Set published at /.well-known/jwks.json.
type JWKS struct {
	Keys []JWK `json:"keys"`
}

// PublicJWK builds a JWKS entry for an RSA public key (Auth side).
func PublicJWK(pub *rsa.PublicKey, kid string) JWK {
	return JWK{
		Kty: "RSA",
		Use: "sig",
		Alg: "RS256",
		Kid: kid,
		N:   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		E:   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}
}

func (k JWK) toRSAPublicKey() (*rsa.PublicKey, error) {
	if k.Kty != "RSA" {
		return nil, fmt.Errorf("unsupported key type %q", k.Kty)
	}
	nBytes, err := base64.RawURLEncoding.DecodeString(k.N)
	if err != nil {
		return nil, fmt.Errorf("decode modulus: %w", err)
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(k.E)
	if err != nil {
		return nil, fmt.Errorf("decode exponent: %w", err)
	}
	e := new(big.Int).SetBytes(eBytes)
	if !e.IsInt64() || e.Int64() < 2 || e.Int64() > 1<<31 {
		return nil, errors.New("invalid RSA exponent")
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(nBytes), E: int(e.Int64())}, nil
}

// Verifier verifies RS256 tokens using public keys fetched from a JWKS URL and
// cached in memory. Cache behaviour follows Feature Spec §2.3:
//   - fetched once at startup and cached for the TTL,
//   - refreshed on expiry,
//   - on an unknown kid the set is re-fetched once before rejecting,
//   - if the JWKS cannot be fetched and the cache is empty/expired, verification
//     fails closed (never accept an unverified token).
type Verifier struct {
	jwksURL string
	issuer  string
	ttl     time.Duration
	client  *http.Client

	mu        sync.RWMutex
	keys      map[string]*rsa.PublicKey
	fetchedAt time.Time
}

// NewVerifier builds a verifier for the given JWKS URL and expected issuer.
func NewVerifier(jwksURL, issuer string, ttl time.Duration) *Verifier {
	return &Verifier{
		jwksURL: jwksURL,
		issuer:  issuer,
		ttl:     ttl,
		client:  &http.Client{Timeout: 10 * time.Second},
		keys:    map[string]*rsa.PublicKey{},
	}
}

// NewLocalVerifier builds a verifier seeded with in-memory keys and no JWKS URL.
// The Auth service uses it to verify its own tokens without an HTTP round-trip
// to itself. Unknown kids still fail closed (the empty URL fetch errors).
func NewLocalVerifier(issuer string, keys map[string]*rsa.PublicKey) *Verifier {
	return &Verifier{
		issuer:    issuer,
		ttl:       100 * 365 * 24 * time.Hour, // effectively never expires
		client:    &http.Client{Timeout: 10 * time.Second},
		keys:      keys,
		fetchedAt: time.Now(),
	}
}

// Prime fetches the JWKS once (used at startup and for readiness).
func (v *Verifier) Prime(ctx context.Context) error { return v.fetch(ctx) }

// Ready reports whether at least one key has been cached.
func (v *Verifier) Ready() bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return len(v.keys) > 0
}

// EnsureReady reports readiness, fetching the JWKS once if the cache is still
// empty. Readiness probes call this so a pod that missed the startup fetch
// (Prime) recovers on a later probe instead of deadlocking: it never becomes
// Ready, so no authenticated request arrives to trigger the lazy refresh in
// keyForKid. Once keys are cached the fast path returns immediately with no
// fetch.
func (v *Verifier) EnsureReady(ctx context.Context) bool {
	if v.Ready() {
		return true
	}
	if err := v.fetch(ctx); err != nil {
		return false
	}
	return v.Ready()
}

func (v *Verifier) fetch(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.jwksURL, nil)
	if err != nil {
		return err
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("jwks fetch returned status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	var set JWKS
	if err := json.Unmarshal(raw, &set); err != nil {
		return fmt.Errorf("decode jwks: %w", err)
	}
	m := make(map[string]*rsa.PublicKey, len(set.Keys))
	for _, k := range set.Keys {
		pk, err := k.toRSAPublicKey()
		if err != nil {
			continue
		}
		m[k.Kid] = pk
	}
	if len(m) == 0 {
		return errors.New("jwks contained no usable keys")
	}
	v.mu.Lock()
	v.keys = m
	v.fetchedAt = time.Now()
	v.mu.Unlock()
	return nil
}

// keyForKid returns the public key for kid, applying the cache rules above.
func (v *Verifier) keyForKid(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	v.mu.RLock()
	_, found := v.keys[kid]
	empty := len(v.keys) == 0
	expired := time.Since(v.fetchedAt) > v.ttl
	v.mu.RUnlock()

	// Fetch when the cache is cold, expired, or the kid is unknown (rotation).
	if empty || expired || !found {
		if err := v.fetch(ctx); err != nil {
			// Fail closed: never trust a stale/expired cache when refresh fails.
			return nil, fmt.Errorf("jwks unavailable: %w", err)
		}
	}
	v.mu.RLock()
	pk, ok := v.keys[kid]
	v.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("no verifying key for kid %q", kid)
	}
	return pk, nil
}

// Verify parses and validates a token, returning its claims. It enforces
// RS256, a matching issuer, and presence/validity of exp.
func (v *Verifier) Verify(ctx context.Context, tokenString string) (*Claims, error) {
	claims := &Claims{}
	keyfunc := func(t *jwt.Token) (any, error) {
		kidRaw, ok := t.Header["kid"]
		if !ok {
			return nil, errors.New("token missing kid header")
		}
		kid, ok := kidRaw.(string)
		if !ok || kid == "" {
			return nil, errors.New("token has invalid kid header")
		}
		return v.keyForKid(ctx, kid)
	}
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{"RS256"}),
		jwt.WithIssuer(v.issuer),
		jwt.WithExpirationRequired(),
	)
	if _, err := parser.ParseWithClaims(tokenString, claims, keyfunc); err != nil {
		return nil, err
	}
	if !claims.Role.Valid() {
		return nil, fmt.Errorf("token has invalid role %q", claims.Role)
	}
	return claims, nil
}

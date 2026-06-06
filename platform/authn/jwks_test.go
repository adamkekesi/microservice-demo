package authn

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// jwksServer serves a mutable set of public keys and counts fetches so tests
// can assert re-fetch behaviour.
type jwksServer struct {
	*httptest.Server
	keys    atomic.Value // JWKS
	fetches atomic.Int64
}

func newJWKSServer(initial JWKS) *jwksServer {
	s := &jwksServer{}
	s.keys.Store(initial)
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.fetches.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.keys.Load().(JWKS))
	}))
	return s
}

func (s *jwksServer) setKeys(set JWKS) { s.keys.Store(set) }

const testIssuer = "auth-service"

func TestVerifier_HappyPath(t *testing.T) {
	key, err := GenerateRSAKey(2048)
	require.NoError(t, err)
	signer := NewSigner(key, "auth-key-1", testIssuer, time.Minute)

	srv := newJWKSServer(JWKS{Keys: []JWK{PublicJWK(signer.PublicKey(), "auth-key-1")}})
	defer srv.Close()

	token, expiresIn, err := signer.Sign("user-123", RoleCustomer)
	require.NoError(t, err)
	require.Equal(t, 60, expiresIn)

	v := NewVerifier(srv.URL, testIssuer, 10*time.Minute)
	claims, err := v.Verify(context.Background(), token)
	require.NoError(t, err)
	require.Equal(t, "user-123", claims.Subject())
	require.Equal(t, RoleCustomer, claims.Role)
	require.True(t, v.Ready())
}

func TestVerifier_ForgedTokenRejected(t *testing.T) {
	good, _ := GenerateRSAKey(2048)
	attacker, _ := GenerateRSAKey(2048)

	// JWKS publishes the GOOD key under kid auth-key-1.
	srv := newJWKSServer(JWKS{Keys: []JWK{PublicJWK(&good.PublicKey, "auth-key-1")}})
	defer srv.Close()

	// Attacker signs with their own key but claims kid auth-key-1.
	forged := NewSigner(attacker, "auth-key-1", testIssuer, time.Minute)
	token, _, err := forged.Sign("user-123", RoleAdmin)
	require.NoError(t, err)

	v := NewVerifier(srv.URL, testIssuer, 10*time.Minute)
	_, err = v.Verify(context.Background(), token)
	require.Error(t, err, "token signed by an unknown key must be rejected")
}

func TestVerifier_UnknownKidTriggersRefetch(t *testing.T) {
	keyOld, _ := GenerateRSAKey(2048)
	keyNew, _ := GenerateRSAKey(2048)

	srv := newJWKSServer(JWKS{Keys: []JWK{PublicJWK(&keyOld.PublicKey, "auth-key-1")}})
	defer srv.Close()

	v := NewVerifier(srv.URL, testIssuer, 10*time.Minute)
	require.NoError(t, v.Prime(context.Background())) // 1 fetch, caches auth-key-1
	require.Equal(t, int64(1), srv.fetches.Load())

	// Key rotation: a new token is signed with auth-key-2, not yet in cache.
	srv.setKeys(JWKS{Keys: []JWK{PublicJWK(&keyNew.PublicKey, "auth-key-2")}})
	newSigner := NewSigner(keyNew, "auth-key-2", testIssuer, time.Minute)
	token, _, _ := newSigner.Sign("user-9", RoleOperator)

	claims, err := v.Verify(context.Background(), token)
	require.NoError(t, err, "unknown kid must trigger a single re-fetch and then verify")
	require.Equal(t, "user-9", claims.Subject())
	require.Equal(t, int64(2), srv.fetches.Load(), "exactly one additional fetch for the unknown kid")
}

func TestVerifier_FailsClosedWhenJWKSUnavailable(t *testing.T) {
	key, _ := GenerateRSAKey(2048)
	signer := NewSigner(key, "auth-key-1", testIssuer, time.Minute)
	token, _, _ := signer.Sign("user-123", RoleCustomer)

	// Point at a closed server: cache is empty and fetch fails -> fail closed.
	srv := newJWKSServer(JWKS{Keys: []JWK{PublicJWK(signer.PublicKey(), "auth-key-1")}})
	srv.Close()

	v := NewVerifier(srv.URL, testIssuer, 10*time.Minute)
	_, err := v.Verify(context.Background(), token)
	require.Error(t, err)
	require.False(t, v.Ready())
}

func TestVerifier_ExpiredTokenRejected(t *testing.T) {
	key, _ := GenerateRSAKey(2048)
	signer := NewSigner(key, "auth-key-1", testIssuer, -time.Minute) // already expired

	srv := newJWKSServer(JWKS{Keys: []JWK{PublicJWK(signer.PublicKey(), "auth-key-1")}})
	defer srv.Close()

	token, _, _ := signer.Sign("user-123", RoleCustomer)
	v := NewVerifier(srv.URL, testIssuer, 10*time.Minute)
	_, err := v.Verify(context.Background(), token)
	require.Error(t, err, "expired token must be rejected")
}

func TestVerifier_WrongIssuerRejected(t *testing.T) {
	key, _ := GenerateRSAKey(2048)
	signer := NewSigner(key, "auth-key-1", "evil-issuer", time.Minute)

	srv := newJWKSServer(JWKS{Keys: []JWK{PublicJWK(signer.PublicKey(), "auth-key-1")}})
	defer srv.Close()

	token, _, _ := signer.Sign("user-123", RoleCustomer)
	v := NewVerifier(srv.URL, testIssuer, 10*time.Minute)
	_, err := v.Verify(context.Background(), token)
	require.Error(t, err, "token with mismatched issuer must be rejected")
}

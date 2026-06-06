//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	authhttp "github.com/adamkekesi/microservice-demo/auth/internal/http"
	"github.com/adamkekesi/microservice-demo/auth/internal/model"
	"github.com/adamkekesi/microservice-demo/auth/internal/repository"
	"github.com/adamkekesi/microservice-demo/auth/internal/service"
	"github.com/adamkekesi/microservice-demo/platform/authn"
	"github.com/adamkekesi/microservice-demo/platform/observability"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func setup(t *testing.T) (*gin.Engine, *service.Service) {
	t.Helper()
	truncate(t, "users", "signing_keys")

	key, err := authn.GenerateRSAKey(2048)
	require.NoError(t, err)
	signer := authn.NewSigner(key, "auth-key-1", "auth-service", 15*time.Minute)

	svc := service.New(repository.NewUserRepository(testDB), signer)
	verifier := authn.NewLocalVerifier("auth-service", map[string]*rsa.PublicKey{
		signer.KeyID(): signer.PublicKey(),
	})
	router := authhttp.NewRouter(authhttp.Deps{
		Service:     svc,
		Verifier:    verifier,
		DB:          testDB,
		Logger:      observability.NewLogger(),
		ServiceName: "auth-test",
	})
	return router, svc
}

func doJSON(t *testing.T, router *gin.Engine, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func decodeJWTHeader(t *testing.T, token string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	require.Len(t, parts, 3)
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	require.NoError(t, err)
	var header map[string]any
	require.NoError(t, json.Unmarshal(raw, &header))
	return header
}

// Acceptance 1: register -> login -> /auth/me returns the same id/email, role customer.
func TestRegisterLoginMe(t *testing.T) {
	router, _ := setup(t)

	rec := doJSON(t, router, http.MethodPost, "/auth/register", "", model.RegisterRequest{Email: "alice@example.com", Password: "supersecret"})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var reg model.UserResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &reg))
	require.Equal(t, "customer", reg.Role)

	rec = doJSON(t, router, http.MethodPost, "/auth/login", "", model.LoginRequest{Email: "alice@example.com", Password: "supersecret"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var login model.LoginResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &login))
	require.NotEmpty(t, login.AccessToken)

	rec = doJSON(t, router, http.MethodGet, "/auth/me", login.AccessToken, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var me model.UserResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &me))
	require.Equal(t, reg.ID, me.ID)
	require.Equal(t, "alice@example.com", me.Email)
	require.Equal(t, "customer", me.Role)
}

// Acceptance 2: duplicate registration email -> 409 EMAIL_TAKEN.
func TestDuplicateEmail(t *testing.T) {
	router, _ := setup(t)
	body := model.RegisterRequest{Email: "dup@example.com", Password: "supersecret"}
	require.Equal(t, http.StatusCreated, doJSON(t, router, http.MethodPost, "/auth/register", "", body).Code)

	rec := doJSON(t, router, http.MethodPost, "/auth/register", "", body)
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Contains(t, rec.Body.String(), "EMAIL_TAKEN")
}

// Acceptance 3: wrong password and unknown email both -> identical 401 INVALID_CREDENTIALS.
func TestInvalidCredentialsIdentical(t *testing.T) {
	router, _ := setup(t)
	require.Equal(t, http.StatusCreated, doJSON(t, router, http.MethodPost, "/auth/register", "",
		model.RegisterRequest{Email: "bob@example.com", Password: "supersecret"}).Code)

	wrongPw := doJSON(t, router, http.MethodPost, "/auth/login", "", model.LoginRequest{Email: "bob@example.com", Password: "nope-wrong"})
	unknown := doJSON(t, router, http.MethodPost, "/auth/login", "", model.LoginRequest{Email: "ghost@example.com", Password: "supersecret"})

	require.Equal(t, http.StatusUnauthorized, wrongPw.Code)
	require.Equal(t, http.StatusUnauthorized, unknown.Code)
	require.Equal(t, wrongPw.Body.String(), unknown.Body.String(), "responses must be identical")
	require.Contains(t, wrongPw.Body.String(), "INVALID_CREDENTIALS")
}

// Acceptance 4: non-admin POST /auth/users -> 403; admin -> 201.
func TestCreateUserRequiresAdmin(t *testing.T) {
	router, svc := setup(t)

	// Customer cannot create users.
	require.Equal(t, http.StatusCreated, doJSON(t, router, http.MethodPost, "/auth/register", "",
		model.RegisterRequest{Email: "cust@example.com", Password: "supersecret"}).Code)
	custLogin := doJSON(t, router, http.MethodPost, "/auth/login", "", model.LoginRequest{Email: "cust@example.com", Password: "supersecret"})
	var cl model.LoginResponse
	require.NoError(t, json.Unmarshal(custLogin.Body.Bytes(), &cl))

	rec := doJSON(t, router, http.MethodPost, "/auth/users", cl.AccessToken,
		model.CreateUserRequest{Email: "new@example.com", Password: "supersecret", Role: "operator"})
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())

	// Seed an admin directly, log in, and create an operator over HTTP.
	_, err := svc.CreateUser(context.Background(), model.CreateUserRequest{Email: "admin@example.com", Password: "supersecret", Role: "admin"})
	require.NoError(t, err)
	adminLogin := doJSON(t, router, http.MethodPost, "/auth/login", "", model.LoginRequest{Email: "admin@example.com", Password: "supersecret"})
	var al model.LoginResponse
	require.NoError(t, json.Unmarshal(adminLogin.Body.Bytes(), &al))

	rec = doJSON(t, router, http.MethodPost, "/auth/users", al.AccessToken,
		model.CreateUserRequest{Email: "operator@example.com", Password: "supersecret", Role: "operator"})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var created model.UserResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	require.Equal(t, "operator", created.Role)
}

// Acceptance 4a: JWKS contains the kid from a freshly issued token and no private material.
func TestJWKSMatchesTokenKid(t *testing.T) {
	router, _ := setup(t)
	require.Equal(t, http.StatusCreated, doJSON(t, router, http.MethodPost, "/auth/register", "",
		model.RegisterRequest{Email: "k@example.com", Password: "supersecret"}).Code)
	login := doJSON(t, router, http.MethodPost, "/auth/login", "", model.LoginRequest{Email: "k@example.com", Password: "supersecret"})
	var lr model.LoginResponse
	require.NoError(t, json.Unmarshal(login.Body.Bytes(), &lr))

	header := decodeJWTHeader(t, lr.AccessToken)
	require.Equal(t, "RS256", header["alg"])
	tokenKid, _ := header["kid"].(string)
	require.NotEmpty(t, tokenKid)

	rec := doJSON(t, router, http.MethodGet, "/.well-known/jwks.json", "", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotContains(t, rec.Body.String(), `"d"`, "JWKS must not expose private key material")

	var jwks authn.JWKS
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &jwks))
	require.Len(t, jwks.Keys, 1)
	require.Equal(t, tokenKid, jwks.Keys[0].Kid)
	require.Equal(t, "RSA", jwks.Keys[0].Kty)
	require.NotEmpty(t, jwks.Keys[0].N)
	require.NotEmpty(t, jwks.Keys[0].E)
}

func TestUnauthenticatedRejected(t *testing.T) {
	router, _ := setup(t)
	rec := doJSON(t, router, http.MethodGet, "/auth/me", "", nil)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Contains(t, rec.Body.String(), "UNAUTHENTICATED")
}

func TestHealthAndReady(t *testing.T) {
	router, _ := setup(t)
	require.Equal(t, http.StatusOK, doJSON(t, router, http.MethodGet, "/health", "", nil).Code)
	require.Equal(t, http.StatusOK, doJSON(t, router, http.MethodGet, "/ready", "", nil).Code)
}

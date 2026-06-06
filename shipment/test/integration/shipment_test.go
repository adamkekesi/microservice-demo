//go:build integration

package integration

import (
	"bytes"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/adamkekesi/microservice-demo/platform/authn"
	"github.com/adamkekesi/microservice-demo/platform/observability"
	"github.com/adamkekesi/microservice-demo/shipment/internal/client"
	shiphttp "github.com/adamkekesi/microservice-demo/shipment/internal/http"
	"github.com/adamkekesi/microservice-demo/shipment/internal/model"
	"github.com/adamkekesi/microservice-demo/shipment/internal/repository"
	"github.com/adamkekesi/microservice-demo/shipment/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// --- mock Inventory (httptest.Server) ---

type canned struct {
	status int
	body   string
}

type mockInventory struct {
	server        *httptest.Server
	reservationID string
	closeOnce     sync.Once

	mu                                      sync.Mutex
	reserve, commit, release                canned
	reserveCalls, commitCalls, releaseCalls int
}

func newMockInventory() *mockInventory {
	m := &mockInventory{reservationID: uuid.NewString()}
	m.reserve = canned{http.StatusCreated, fmt.Sprintf(
		`{"id":%q,"status":"PENDING","warehouse_id":"w","item_id":"i","quantity":1,"expires_at":"2999-01-01T00:00:00Z"}`, m.reservationID)}
	m.commit = canned{http.StatusOK, fmt.Sprintf(`{"id":%q,"status":"COMMITTED"}`, m.reservationID)}
	m.release = canned{http.StatusOK, fmt.Sprintf(`{"id":%q,"status":"RELEASED"}`, m.reservationID)}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /reservations", func(w http.ResponseWriter, _ *http.Request) {
		m.mu.Lock()
		m.reserveCalls++
		c := m.reserve
		m.mu.Unlock()
		writeCanned(w, c)
	})
	mux.HandleFunc("POST /reservations/{id}/commit", func(w http.ResponseWriter, _ *http.Request) {
		m.mu.Lock()
		m.commitCalls++
		c := m.commit
		m.mu.Unlock()
		writeCanned(w, c)
	})
	mux.HandleFunc("POST /reservations/{id}/release", func(w http.ResponseWriter, _ *http.Request) {
		m.mu.Lock()
		m.releaseCalls++
		c := m.release
		m.mu.Unlock()
		writeCanned(w, c)
	})
	m.server = httptest.NewServer(mux)
	return m
}

func writeCanned(w http.ResponseWriter, c canned) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(c.status)
	_, _ = io.WriteString(w, c.body)
}

func (m *mockInventory) URL() string         { return m.server.URL }
func (m *mockInventory) Close()              { m.closeOnce.Do(m.server.Close) }
func (m *mockInventory) setReserve(c canned) { m.mu.Lock(); m.reserve = c; m.mu.Unlock() }
func (m *mockInventory) setCommit(c canned)  { m.mu.Lock(); m.commit = c; m.mu.Unlock() }
func (m *mockInventory) commitCount() int    { m.mu.Lock(); defer m.mu.Unlock(); return m.commitCalls }
func (m *mockInventory) reserveCount() int   { m.mu.Lock(); defer m.mu.Unlock(); return m.reserveCalls }
func (m *mockInventory) releaseCount() int   { m.mu.Lock(); defer m.mu.Unlock(); return m.releaseCalls }

// --- harness ---

func setup(t *testing.T) (*gin.Engine, *authn.Signer, *mockInventory) {
	t.Helper()
	truncate(t, "shipment_status_history", "shipments")

	mock := newMockInventory()
	t.Cleanup(mock.Close)

	key, err := authn.GenerateRSAKey(2048)
	require.NoError(t, err)
	signer := authn.NewSigner(key, "ship-key", "auth-service", 15*time.Minute)
	verifier := authn.NewLocalVerifier("auth-service", map[string]*rsa.PublicKey{signer.KeyID(): signer.PublicKey()})

	inv := client.NewHTTPClient(mock.URL(), 3*time.Second)
	svc := service.New(repository.New(testDB), inv, nil)
	router := shiphttp.NewRouter(shiphttp.Deps{
		Service:     svc,
		Verifier:    verifier,
		DB:          testDB,
		Logger:      observability.NewLogger(),
		ServiceName: "shipment-test",
	})
	return router, signer, mock
}

func tokenFor(t *testing.T, signer *authn.Signer, sub string, role authn.Role) string {
	t.Helper()
	tok, _, err := signer.Sign(sub, role)
	require.NoError(t, err)
	return tok
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

type errBody struct {
	Error struct {
		Code    string         `json:"code"`
		Details map[string]any `json:"details"`
	} `json:"error"`
}

func parseErr(t *testing.T, rec *httptest.ResponseRecorder) errBody {
	t.Helper()
	var e errBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &e))
	return e
}

func validBody() model.CreateShipmentRequest {
	return model.CreateShipmentRequest{
		ItemID:             uuid.NewString(),
		WarehouseID:        uuid.NewString(),
		Quantity:           3,
		DestinationAddress: "1 Main Street",
	}
}

func shipmentCount(t *testing.T) int {
	t.Helper()
	var n int64
	require.NoError(t, testDB.Model(&model.Shipment{}).Count(&n).Error)
	return int(n)
}

func createOK(t *testing.T, router *gin.Engine, token string) model.ShipmentResponse {
	t.Helper()
	rec := doJSON(t, router, http.MethodPost, "/shipments", token, validBody())
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var s model.ShipmentResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &s))
	return s
}

// Acceptance 14: create shipment -> PENDING, Inventory reserve invoked.
func TestCreateShipmentPending(t *testing.T) {
	router, signer, mock := setup(t)
	cust := tokenFor(t, signer, uuid.NewString(), authn.RoleCustomer)

	s := createOK(t, router, cust)
	require.Equal(t, "PENDING", s.Status)
	require.Equal(t, mock.reservationID, s.ReservationID)
	require.Equal(t, 1, mock.reserveCount())
	require.Equal(t, 1, shipmentCount(t))
}

// Acceptance 15: confirm -> CONFIRMED, Inventory commit invoked.
func TestConfirmShipment(t *testing.T) {
	router, signer, mock := setup(t)
	cust := tokenFor(t, signer, uuid.NewString(), authn.RoleCustomer)
	s := createOK(t, router, cust)

	rec := doJSON(t, router, http.MethodPost, "/shipments/"+s.ID+"/confirm", cust, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out model.ShipmentResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Equal(t, "CONFIRMED", out.Status)
	require.Equal(t, 1, mock.commitCount())
}

// Acceptance 16: create with qty > available -> 409 INSUFFICIENT_STOCK, no shipment row.
func TestCreateInsufficientStock(t *testing.T) {
	router, signer, mock := setup(t)
	mock.setReserve(canned{http.StatusConflict,
		`{"error":{"code":"INSUFFICIENT_STOCK","message":"no stock","details":{"available":1,"requested":3}}}`})
	cust := tokenFor(t, signer, uuid.NewString(), authn.RoleCustomer)

	rec := doJSON(t, router, http.MethodPost, "/shipments", cust, validBody())
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	e := parseErr(t, rec)
	require.Equal(t, "INSUFFICIENT_STOCK", e.Error.Code)
	require.EqualValues(t, 1, e.Error.Details["available"])
	require.EqualValues(t, 3, e.Error.Details["requested"])
	require.Equal(t, 0, shipmentCount(t), "no shipment row may be created")
}

// Acceptance 17: cancel a PENDING shipment -> CANCELLED, Inventory release invoked.
func TestCancelPendingShipment(t *testing.T) {
	router, signer, mock := setup(t)
	cust := tokenFor(t, signer, uuid.NewString(), authn.RoleCustomer)
	s := createOK(t, router, cust)

	rec := doJSON(t, router, http.MethodPost, "/shipments/"+s.ID+"/cancel", cust, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out model.ShipmentResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Equal(t, "CANCELLED", out.Status)
	require.Equal(t, 1, mock.releaseCount())
}

// Acceptance 18: confirm a shipment whose reservation expired -> CANCELLED, response 409.
func TestConfirmExpiredReservation(t *testing.T) {
	router, signer, mock := setup(t)
	cust := tokenFor(t, signer, uuid.NewString(), authn.RoleCustomer)
	s := createOK(t, router, cust)

	mock.setCommit(canned{http.StatusConflict, `{"error":{"code":"RESERVATION_EXPIRED","message":"expired"}}`})
	rec := doJSON(t, router, http.MethodPost, "/shipments/"+s.ID+"/confirm", cust, nil)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.Equal(t, "RESERVATION_EXPIRED", parseErr(t, rec).Error.Code)

	get := doJSON(t, router, http.MethodGet, "/shipments/"+s.ID, cust, nil)
	var out model.ShipmentResponse
	require.NoError(t, json.Unmarshal(get.Body.Bytes(), &out))
	require.Equal(t, "CANCELLED", out.Status, "expired confirm must cancel the shipment")
}

// Acceptance 19: cancel a CONFIRMED shipment -> 409 INVALID_STATE_TRANSITION.
func TestCancelConfirmedShipment(t *testing.T) {
	router, signer, _ := setup(t)
	cust := tokenFor(t, signer, uuid.NewString(), authn.RoleCustomer)
	s := createOK(t, router, cust)
	require.Equal(t, http.StatusOK, doJSON(t, router, http.MethodPost, "/shipments/"+s.ID+"/confirm", cust, nil).Code)

	rec := doJSON(t, router, http.MethodPost, "/shipments/"+s.ID+"/cancel", cust, nil)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.Equal(t, "INVALID_STATE_TRANSITION", parseErr(t, rec).Error.Code)
}

// Acceptance 20: confirm an already-CONFIRMED shipment -> 200, no double commit.
func TestConfirmIdempotent(t *testing.T) {
	router, signer, mock := setup(t)
	cust := tokenFor(t, signer, uuid.NewString(), authn.RoleCustomer)
	s := createOK(t, router, cust)

	require.Equal(t, http.StatusOK, doJSON(t, router, http.MethodPost, "/shipments/"+s.ID+"/confirm", cust, nil).Code)
	require.Equal(t, http.StatusOK, doJSON(t, router, http.MethodPost, "/shipments/"+s.ID+"/confirm", cust, nil).Code)
	require.Equal(t, 1, mock.commitCount(), "must not commit twice")
}

// Acceptance 21: another user's shipment -> 403; operator can read it.
func TestShipmentOwnershipAuthz(t *testing.T) {
	router, signer, _ := setup(t)
	custA := tokenFor(t, signer, uuid.NewString(), authn.RoleCustomer)
	custB := tokenFor(t, signer, uuid.NewString(), authn.RoleCustomer)
	op := tokenFor(t, signer, uuid.NewString(), authn.RoleOperator)
	s := createOK(t, router, custA)

	require.Equal(t, http.StatusForbidden, doJSON(t, router, http.MethodGet, "/shipments/"+s.ID, custB, nil).Code)
	require.Equal(t, http.StatusOK, doJSON(t, router, http.MethodGet, "/shipments/"+s.ID, custA, nil).Code)
	require.Equal(t, http.StatusOK, doJSON(t, router, http.MethodGet, "/shipments/"+s.ID, op, nil).Code)
}

// Acceptance 22: with Inventory stopped, create -> 502 and no shipment row.
func TestInventoryDown(t *testing.T) {
	router, signer, mock := setup(t)
	cust := tokenFor(t, signer, uuid.NewString(), authn.RoleCustomer)
	mock.Close() // simulate Inventory being unreachable

	rec := doJSON(t, router, http.MethodPost, "/shipments", cust, validBody())
	require.Equal(t, http.StatusBadGateway, rec.Code, rec.Body.String())
	require.Equal(t, "DEPENDENCY_UNAVAILABLE", parseErr(t, rec).Error.Code)
	require.Equal(t, 0, shipmentCount(t))
}

func TestUnauthenticatedRejected(t *testing.T) {
	router, _, _ := setup(t)
	require.Equal(t, http.StatusUnauthorized, doJSON(t, router, http.MethodPost, "/shipments", "", validBody()).Code)
}

func TestListOwnShipments(t *testing.T) {
	router, signer, _ := setup(t)
	cust := tokenFor(t, signer, uuid.NewString(), authn.RoleCustomer)
	createOK(t, router, cust)
	createOK(t, router, cust)

	rec := doJSON(t, router, http.MethodGet, "/shipments", cust, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var list []model.ShipmentResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Len(t, list, 2)
}

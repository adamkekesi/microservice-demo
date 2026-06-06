//go:build integration

package integration

import (
	"bytes"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	invhttp "github.com/adamkekesi/microservice-demo/inventory/internal/http"
	"github.com/adamkekesi/microservice-demo/inventory/internal/model"
	"github.com/adamkekesi/microservice-demo/inventory/internal/repository"
	"github.com/adamkekesi/microservice-demo/inventory/internal/service"
	"github.com/adamkekesi/microservice-demo/platform/authn"
	"github.com/adamkekesi/microservice-demo/platform/observability"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func newRouter(t *testing.T) (*gin.Engine, *authn.Signer) {
	t.Helper()
	truncate(t, "reservations", "stock", "items", "warehouses")

	key, err := authn.GenerateRSAKey(2048)
	require.NoError(t, err)
	signer := authn.NewSigner(key, "inv-key", "auth-service", 15*time.Minute)
	verifier := authn.NewLocalVerifier("auth-service", map[string]*rsa.PublicKey{signer.KeyID(): signer.PublicKey()})

	svc := service.New(repository.New(testDB), 15*time.Minute, nil)
	router := invhttp.NewRouter(invhttp.Deps{
		Service:     svc,
		Verifier:    verifier,
		DB:          testDB,
		Logger:      observability.NewLogger(),
		ServiceName: "inventory-test",
	})
	return router, signer
}

func tokenFor(t *testing.T, signer *authn.Signer, sub string, role authn.Role) string {
	t.Helper()
	tok, _, err := signer.Sign(sub, role)
	require.NoError(t, err)
	return tok
}

func doRaw(router *gin.Engine, method, path, token string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func doJSON(t *testing.T, router *gin.Engine, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var b []byte
	if body != nil {
		var err error
		b, err = json.Marshal(body)
		require.NoError(t, err)
	}
	return doRaw(router, method, path, token, b)
}

type errBody struct {
	Error struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Details map[string]any `json:"details"`
	} `json:"error"`
}

func parseErr(t *testing.T, rec *httptest.ResponseRecorder) errBody {
	t.Helper()
	var e errBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &e))
	return e
}

func intPtr(v int) *int { return &v }

func createWHItemStock(t *testing.T, router *gin.Engine, opToken string, onHand int) (string, string) {
	t.Helper()
	wRec := doJSON(t, router, http.MethodPost, "/warehouses", opToken,
		model.CreateWarehouseRequest{Code: "WH-" + uuid.NewString()[:8], Name: "Warehouse"})
	require.Equal(t, http.StatusCreated, wRec.Code, wRec.Body.String())
	var w model.WarehouseResponse
	require.NoError(t, json.Unmarshal(wRec.Body.Bytes(), &w))

	iRec := doJSON(t, router, http.MethodPost, "/items", opToken,
		model.CreateItemRequest{SKU: "SKU-" + uuid.NewString()[:8], Name: "Item"})
	require.Equal(t, http.StatusCreated, iRec.Code, iRec.Body.String())
	var it model.ItemResponse
	require.NoError(t, json.Unmarshal(iRec.Body.Bytes(), &it))

	sRec := doJSON(t, router, http.MethodPut, "/stock", opToken,
		model.SetStockRequest{WarehouseID: w.ID, ItemID: it.ID, QuantityOnHand: intPtr(onHand)})
	require.Equal(t, http.StatusOK, sRec.Code, sRec.Body.String())
	return w.ID, it.ID
}

func getStock(t *testing.T, router *gin.Engine, token, wh, item string) model.StockResponse {
	t.Helper()
	rec := doJSON(t, router, http.MethodGet, "/stock?warehouse_id="+wh+"&item_id="+item, token, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var s model.StockResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &s))
	return s
}

func reserve(t *testing.T, router *gin.Engine, token, wh, item string, qty int) *httptest.ResponseRecorder {
	t.Helper()
	return doJSON(t, router, http.MethodPost, "/reservations", token,
		model.CreateReservationRequest{WarehouseID: wh, ItemID: item, Quantity: qty})
}

// Acceptance 5 & 6: reserve 30 -> available 70 / on_hand 100; commit -> on_hand 70 / available 70.
func TestReserveThenCommit(t *testing.T) {
	router, signer := newRouter(t)
	op := tokenFor(t, signer, uuid.NewString(), authn.RoleOperator)
	cust := tokenFor(t, signer, uuid.NewString(), authn.RoleCustomer)
	wh, item := createWHItemStock(t, router, op, 100)

	rec := reserve(t, router, cust, wh, item, 30)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var r model.ReservationResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &r))
	require.Equal(t, "PENDING", r.Status)

	s := getStock(t, router, cust, wh, item)
	require.Equal(t, 100, s.QuantityOnHand)
	require.Equal(t, 30, s.QuantityReserved)
	require.Equal(t, 70, s.QuantityAvailable)

	cRec := doJSON(t, router, http.MethodPost, "/reservations/"+r.ID+"/commit", cust, nil)
	require.Equal(t, http.StatusOK, cRec.Code, cRec.Body.String())

	s = getStock(t, router, cust, wh, item)
	require.Equal(t, 70, s.QuantityOnHand)
	require.Equal(t, 0, s.QuantityReserved)
	require.Equal(t, 70, s.QuantityAvailable)
}

// Acceptance 7: reserve 30 then release -> available 100 / on_hand 100.
func TestReserveThenRelease(t *testing.T) {
	router, signer := newRouter(t)
	op := tokenFor(t, signer, uuid.NewString(), authn.RoleOperator)
	cust := tokenFor(t, signer, uuid.NewString(), authn.RoleCustomer)
	wh, item := createWHItemStock(t, router, op, 100)

	rec := reserve(t, router, cust, wh, item, 30)
	require.Equal(t, http.StatusCreated, rec.Code)
	var r model.ReservationResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &r))

	relRec := doJSON(t, router, http.MethodPost, "/reservations/"+r.ID+"/release", cust, nil)
	require.Equal(t, http.StatusOK, relRec.Code, relRec.Body.String())

	s := getStock(t, router, cust, wh, item)
	require.Equal(t, 100, s.QuantityOnHand)
	require.Equal(t, 100, s.QuantityAvailable)
}

// Acceptance 8: reserve 60, then reserve 60 against on_hand=100 -> 409 with details.
func TestInsufficientStock(t *testing.T) {
	router, signer := newRouter(t)
	op := tokenFor(t, signer, uuid.NewString(), authn.RoleOperator)
	cust := tokenFor(t, signer, uuid.NewString(), authn.RoleCustomer)
	wh, item := createWHItemStock(t, router, op, 100)

	require.Equal(t, http.StatusCreated, reserve(t, router, cust, wh, item, 60).Code)

	rec := reserve(t, router, cust, wh, item, 60)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	e := parseErr(t, rec)
	require.Equal(t, "INSUFFICIENT_STOCK", e.Error.Code)
	require.EqualValues(t, 40, e.Error.Details["available"])
	require.EqualValues(t, 60, e.Error.Details["requested"])
}

// Acceptance 9: two concurrent reserves that jointly exceed availability -> exactly one succeeds.
func TestConcurrentReservesExactlyOneSucceeds(t *testing.T) {
	router, signer := newRouter(t)
	op := tokenFor(t, signer, uuid.NewString(), authn.RoleOperator)
	cust := tokenFor(t, signer, uuid.NewString(), authn.RoleCustomer)
	wh, item := createWHItemStock(t, router, op, 100)

	body, err := json.Marshal(model.CreateReservationRequest{WarehouseID: wh, ItemID: item, Quantity: 60})
	require.NoError(t, err)

	const n = 2
	codes := make([]int, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			codes[idx] = doRaw(router, http.MethodPost, "/reservations", cust, body).Code
		}(i)
	}
	wg.Wait()

	var ok, conflict int
	for _, c := range codes {
		switch c {
		case http.StatusCreated:
			ok++
		case http.StatusConflict:
			conflict++
		}
	}
	require.Equal(t, 1, ok, "exactly one reserve should succeed, got codes %v", codes)
	require.Equal(t, 1, conflict, "exactly one reserve should conflict, got codes %v", codes)
}

// Acceptance 10: commit on a reservation past expires_at -> 409 RESERVATION_EXPIRED; on_hand untouched.
func TestCommitExpiredReservation(t *testing.T) {
	router, signer := newRouter(t)
	op := tokenFor(t, signer, uuid.NewString(), authn.RoleOperator)
	cust := tokenFor(t, signer, uuid.NewString(), authn.RoleCustomer)
	wh, item := createWHItemStock(t, router, op, 100)

	rec := reserve(t, router, cust, wh, item, 30)
	require.Equal(t, http.StatusCreated, rec.Code)
	var r model.ReservationResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &r))

	// Force expiry directly in the DB.
	require.NoError(t, testDB.Exec("UPDATE reservations SET expires_at = ? WHERE id = ?",
		time.Now().Add(-time.Hour), r.ID).Error)

	cRec := doJSON(t, router, http.MethodPost, "/reservations/"+r.ID+"/commit", cust, nil)
	require.Equal(t, http.StatusConflict, cRec.Code, cRec.Body.String())
	require.Equal(t, "RESERVATION_EXPIRED", parseErr(t, cRec).Error.Code)

	s := getStock(t, router, cust, wh, item)
	require.Equal(t, 100, s.QuantityOnHand, "expired reservation must never reduce on_hand")
	require.Equal(t, 100, s.QuantityAvailable, "expired reservation is excluded from reserved")
}

// Acceptance 11: repeating a reserve with the same idempotency_key returns the same reservation.
func TestIdempotentReserve(t *testing.T) {
	router, signer := newRouter(t)
	op := tokenFor(t, signer, uuid.NewString(), authn.RoleOperator)
	cust := tokenFor(t, signer, uuid.NewString(), authn.RoleCustomer)
	wh, item := createWHItemStock(t, router, op, 100)

	key := uuid.NewString()
	body := model.CreateReservationRequest{WarehouseID: wh, ItemID: item, Quantity: 10, IdempotencyKey: &key}

	first := doJSON(t, router, http.MethodPost, "/reservations", cust, body)
	require.Equal(t, http.StatusCreated, first.Code, first.Body.String())
	var r1 model.ReservationResponse
	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &r1))

	second := doJSON(t, router, http.MethodPost, "/reservations", cust, body)
	require.Equal(t, http.StatusOK, second.Code, "idempotent hit must be 200, not a new 201")
	var r2 model.ReservationResponse
	require.NoError(t, json.Unmarshal(second.Body.Bytes(), &r2))
	require.Equal(t, r1.ID, r2.ID)

	// Only one reservation's worth of stock is held.
	s := getStock(t, router, cust, wh, item)
	require.Equal(t, 10, s.QuantityReserved)
}

// Acceptance 12: PUT /stock below current reserved -> 409 STOCK_BELOW_RESERVED.
func TestSetStockBelowReserved(t *testing.T) {
	router, signer := newRouter(t)
	op := tokenFor(t, signer, uuid.NewString(), authn.RoleOperator)
	cust := tokenFor(t, signer, uuid.NewString(), authn.RoleCustomer)
	wh, item := createWHItemStock(t, router, op, 100)
	require.Equal(t, http.StatusCreated, reserve(t, router, cust, wh, item, 30).Code)

	rec := doJSON(t, router, http.MethodPut, "/stock", op,
		model.SetStockRequest{WarehouseID: wh, ItemID: item, QuantityOnHand: intPtr(10)})
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	e := parseErr(t, rec)
	require.Equal(t, "STOCK_BELOW_RESERVED", e.Error.Code)
	require.EqualValues(t, 30, e.Error.Details["reserved"])
}

// Acceptance 13: release on a COMMITTED reservation -> 409 RESERVATION_ALREADY_COMMITTED.
func TestReleaseCommitted(t *testing.T) {
	router, signer := newRouter(t)
	op := tokenFor(t, signer, uuid.NewString(), authn.RoleOperator)
	cust := tokenFor(t, signer, uuid.NewString(), authn.RoleCustomer)
	wh, item := createWHItemStock(t, router, op, 100)

	rec := reserve(t, router, cust, wh, item, 30)
	require.Equal(t, http.StatusCreated, rec.Code)
	var r model.ReservationResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &r))
	require.Equal(t, http.StatusOK, doJSON(t, router, http.MethodPost, "/reservations/"+r.ID+"/commit", cust, nil).Code)

	relRec := doJSON(t, router, http.MethodPost, "/reservations/"+r.ID+"/release", cust, nil)
	require.Equal(t, http.StatusConflict, relRec.Code, relRec.Body.String())
	require.Equal(t, "RESERVATION_ALREADY_COMMITTED", parseErr(t, relRec).Error.Code)
}

func TestReservationOwnershipAuthz(t *testing.T) {
	router, signer := newRouter(t)
	op := tokenFor(t, signer, uuid.NewString(), authn.RoleOperator)
	custA := tokenFor(t, signer, uuid.NewString(), authn.RoleCustomer)
	custB := tokenFor(t, signer, uuid.NewString(), authn.RoleCustomer)
	wh, item := createWHItemStock(t, router, op, 100)

	rec := reserve(t, router, custA, wh, item, 10)
	require.Equal(t, http.StatusCreated, rec.Code)
	var r model.ReservationResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &r))

	require.Equal(t, http.StatusForbidden, doJSON(t, router, http.MethodGet, "/reservations/"+r.ID, custB, nil).Code)
	require.Equal(t, http.StatusOK, doJSON(t, router, http.MethodGet, "/reservations/"+r.ID, custA, nil).Code)
	require.Equal(t, http.StatusOK, doJSON(t, router, http.MethodGet, "/reservations/"+r.ID, op, nil).Code)
	require.Equal(t, http.StatusForbidden, doJSON(t, router, http.MethodPost, "/reservations/"+r.ID+"/commit", custB, nil).Code)
}

func TestDuplicateCodeAndSKU(t *testing.T) {
	router, signer := newRouter(t)
	op := tokenFor(t, signer, uuid.NewString(), authn.RoleOperator)

	code := "WH-DUP-" + uuid.NewString()[:6]
	require.Equal(t, http.StatusCreated, doJSON(t, router, http.MethodPost, "/warehouses", op, model.CreateWarehouseRequest{Code: code, Name: "A"}).Code)
	dup := doJSON(t, router, http.MethodPost, "/warehouses", op, model.CreateWarehouseRequest{Code: code, Name: "B"})
	require.Equal(t, http.StatusConflict, dup.Code)
	require.Equal(t, "CODE_TAKEN", parseErr(t, dup).Error.Code)

	sku := "SKU-DUP-" + uuid.NewString()[:6]
	require.Equal(t, http.StatusCreated, doJSON(t, router, http.MethodPost, "/items", op, model.CreateItemRequest{SKU: sku, Name: "A"}).Code)
	dupItem := doJSON(t, router, http.MethodPost, "/items", op, model.CreateItemRequest{SKU: sku, Name: "B"})
	require.Equal(t, http.StatusConflict, dupItem.Code)
	require.Equal(t, "SKU_TAKEN", parseErr(t, dupItem).Error.Code)
}

func TestAdminEndpointsRequireOperator(t *testing.T) {
	router, signer := newRouter(t)
	cust := tokenFor(t, signer, uuid.NewString(), authn.RoleCustomer)
	rec := doJSON(t, router, http.MethodPost, "/warehouses", cust, model.CreateWarehouseRequest{Code: "X", Name: "Y"})
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
}

func TestUnauthenticatedRejected(t *testing.T) {
	router, _ := newRouter(t)
	require.Equal(t, http.StatusUnauthorized, doJSON(t, router, http.MethodGet, "/warehouses", "", nil).Code)
}

func TestGetStockUnknownWarehouse(t *testing.T) {
	router, signer := newRouter(t)
	cust := tokenFor(t, signer, uuid.NewString(), authn.RoleCustomer)
	rec := doJSON(t, router, http.MethodGet, "/stock?warehouse_id="+uuid.NewString()+"&item_id="+uuid.NewString(), cust, nil)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

func TestHealthAndReady(t *testing.T) {
	router, _ := newRouter(t)
	require.Equal(t, http.StatusOK, doJSON(t, router, http.MethodGet, "/health", "", nil).Code)
	require.Equal(t, http.StatusOK, doJSON(t, router, http.MethodGet, "/ready", "", nil).Code)
}

func reserveResp(t *testing.T, rec *httptest.ResponseRecorder) model.ReservationResponse {
	t.Helper()
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var r model.ReservationResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &r))
	return r
}

// Delete a terminal (RELEASED) reservation -> 204; the row is gone.
func TestDeleteReleasedReservation(t *testing.T) {
	router, signer := newRouter(t)
	op := tokenFor(t, signer, uuid.NewString(), authn.RoleOperator)
	cust := tokenFor(t, signer, uuid.NewString(), authn.RoleCustomer)
	wh, item := createWHItemStock(t, router, op, 100)

	r := reserveResp(t, reserve(t, router, cust, wh, item, 10))
	require.Equal(t, http.StatusOK, doJSON(t, router, http.MethodPost, "/reservations/"+r.ID+"/release", cust, nil).Code)

	rec := doJSON(t, router, http.MethodDelete, "/reservations/"+r.ID, cust, nil)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	require.Equal(t, http.StatusNotFound, doJSON(t, router, http.MethodGet, "/reservations/"+r.ID, cust, nil).Code)
}

// Deleting an active (PENDING) reservation is refused -> 409.
func TestDeletePendingReservationRejected(t *testing.T) {
	router, signer := newRouter(t)
	op := tokenFor(t, signer, uuid.NewString(), authn.RoleOperator)
	cust := tokenFor(t, signer, uuid.NewString(), authn.RoleCustomer)
	wh, item := createWHItemStock(t, router, op, 100)

	r := reserveResp(t, reserve(t, router, cust, wh, item, 10))
	rec := doJSON(t, router, http.MethodDelete, "/reservations/"+r.ID, cust, nil)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.Equal(t, "RESERVATION_NOT_ACTIVE", parseErr(t, rec).Error.Code)
	require.Equal(t, http.StatusOK, doJSON(t, router, http.MethodGet, "/reservations/"+r.ID, cust, nil).Code)
}

// Bulk purge: operator removes terminal reservations older than the cutoff; a
// PENDING one survives. A customer may not purge.
func TestPurgeReservations(t *testing.T) {
	router, signer := newRouter(t)
	op := tokenFor(t, signer, uuid.NewString(), authn.RoleOperator)
	cust := tokenFor(t, signer, uuid.NewString(), authn.RoleCustomer)
	wh, item := createWHItemStock(t, router, op, 100)
	cutoff := url.QueryEscape(time.Now().Add(time.Hour).Format(time.RFC3339))

	released := reserveResp(t, reserve(t, router, cust, wh, item, 10))
	require.Equal(t, http.StatusOK, doJSON(t, router, http.MethodPost, "/reservations/"+released.ID+"/release", cust, nil).Code)
	pending := reserveResp(t, reserve(t, router, cust, wh, item, 10)) // stays PENDING

	require.Equal(t, http.StatusForbidden,
		doJSON(t, router, http.MethodDelete, "/reservations?before="+cutoff, cust, nil).Code)

	rec := doJSON(t, router, http.MethodDelete, "/reservations?before="+cutoff, op, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var p model.PurgeResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &p))
	require.EqualValues(t, 1, p.Deleted, "only the terminal reservation is purged")
	require.Equal(t, http.StatusOK, doJSON(t, router, http.MethodGet, "/reservations/"+pending.ID, cust, nil).Code,
		"the PENDING reservation survives")
}

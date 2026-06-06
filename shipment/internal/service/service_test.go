package service

import (
	"context"
	"errors"
	"testing"

	"github.com/adamkekesi/microservice-demo/platform/apperror"
	"github.com/adamkekesi/microservice-demo/platform/authn"
	"github.com/adamkekesi/microservice-demo/shipment/internal/client"
	"github.com/adamkekesi/microservice-demo/shipment/internal/model"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// --- fakes ---

type fakeInventory struct {
	reserveFn  func(req client.ReserveRequest) (*client.Reservation, error)
	commitErr  error
	releaseErr error
	committed  []string
	released   []string
}

func (f *fakeInventory) Reserve(_ context.Context, _ string, req client.ReserveRequest) (*client.Reservation, error) {
	if f.reserveFn != nil {
		return f.reserveFn(req)
	}
	return &client.Reservation{ID: uuid.NewString(), Status: "PENDING"}, nil
}

func (f *fakeInventory) Commit(_ context.Context, _ string, reservationID string) error {
	f.committed = append(f.committed, reservationID)
	return f.commitErr
}

func (f *fakeInventory) Release(_ context.Context, _ string, reservationID string) error {
	f.released = append(f.released, reservationID)
	return f.releaseErr
}

type mockRepo struct {
	shipments map[string]*model.Shipment
	createErr error
}

func newMockRepo() *mockRepo { return &mockRepo{shipments: map[string]*model.Shipment{}} }

func (m *mockRepo) Create(_ context.Context, s *model.Shipment) error {
	if m.createErr != nil {
		return m.createErr
	}
	m.shipments[s.ID] = s
	return nil
}

func (m *mockRepo) Get(_ context.Context, id string) (*model.Shipment, error) {
	if s, ok := m.shipments[id]; ok {
		return s, nil
	}
	return nil, gorm.ErrRecordNotFound
}

func (m *mockRepo) Transition(_ context.Context, s *model.Shipment, to model.ShipmentStatus, _ *string) error {
	s.Status = to
	m.shipments[s.ID] = s
	return nil
}

func (m *mockRepo) List(_ context.Context, ownerID string, all bool, _, _ int) ([]model.Shipment, error) {
	var out []model.Shipment
	for _, s := range m.shipments {
		if all || s.OwnerUserID == ownerID {
			out = append(out, *s)
		}
	}
	return out, nil
}

// --- helpers ---

func ctxWithClaims(sub string, role authn.Role) context.Context {
	c := &authn.Claims{Role: role}
	c.RegisteredClaims.Subject = sub
	return authn.ContextWithClaims(context.Background(), c)
}

func requireAppError(t *testing.T, err error, code string, status int) {
	t.Helper()
	var ae *apperror.AppError
	require.ErrorAs(t, err, &ae)
	require.Equal(t, code, ae.Code)
	require.Equal(t, status, ae.Status)
}

func validCreateReq() model.CreateShipmentRequest {
	return model.CreateShipmentRequest{
		ItemID:             uuid.NewString(),
		WarehouseID:        uuid.NewString(),
		Quantity:           3,
		DestinationAddress: "1 Main St",
	}
}

// --- create / saga ---

func TestCreateShipment_Success(t *testing.T) {
	repo := newMockRepo()
	inv := &fakeInventory{}
	svc := New(repo, inv, nil)

	sub := uuid.NewString()
	resp, err := svc.CreateShipment(ctxWithClaims(sub, authn.RoleCustomer), "Bearer t", validCreateReq())
	require.NoError(t, err)
	require.Equal(t, "PENDING", resp.Status)
	require.NotEmpty(t, resp.ReservationID)
	require.Len(t, repo.shipments, 1)
	require.Equal(t, sub, repo.shipments[resp.ID].OwnerUserID)
}

func TestCreateShipment_InsufficientStock(t *testing.T) {
	repo := newMockRepo()
	inv := &fakeInventory{reserveFn: func(client.ReserveRequest) (*client.Reservation, error) {
		return nil, apperror.Conflict("INSUFFICIENT_STOCK", "no stock").
			WithDetails(map[string]any{"available": 1, "requested": 3})
	}}
	svc := New(repo, inv, nil)

	_, err := svc.CreateShipment(ctxWithClaims(uuid.NewString(), authn.RoleCustomer), "Bearer t", validCreateReq())
	requireAppError(t, err, "INSUFFICIENT_STOCK", 409)
	require.Empty(t, repo.shipments, "no shipment must be created")
}

func TestCreateShipment_NotFound(t *testing.T) {
	repo := newMockRepo()
	inv := &fakeInventory{reserveFn: func(client.ReserveRequest) (*client.Reservation, error) {
		return nil, apperror.NotFound("unknown item")
	}}
	svc := New(repo, inv, nil)

	_, err := svc.CreateShipment(ctxWithClaims(uuid.NewString(), authn.RoleCustomer), "Bearer t", validCreateReq())
	requireAppError(t, err, apperror.CodeNotFound, 404)
	require.Empty(t, repo.shipments)
}

func TestCreateShipment_DependencyUnavailable(t *testing.T) {
	repo := newMockRepo()
	inv := &fakeInventory{reserveFn: func(client.ReserveRequest) (*client.Reservation, error) {
		return nil, apperror.DependencyUnavailable("inventory down")
	}}
	svc := New(repo, inv, nil)

	_, err := svc.CreateShipment(ctxWithClaims(uuid.NewString(), authn.RoleCustomer), "Bearer t", validCreateReq())
	requireAppError(t, err, apperror.CodeDependencyUnavailable, 502)
	require.Empty(t, repo.shipments)
}

func TestCreateShipment_PersistFailsCompensatesWithRelease(t *testing.T) {
	repo := newMockRepo()
	repo.createErr = errors.New("db write failed")
	reservationID := uuid.NewString()
	inv := &fakeInventory{reserveFn: func(client.ReserveRequest) (*client.Reservation, error) {
		return &client.Reservation{ID: reservationID, Status: "PENDING"}, nil
	}}
	svc := New(repo, inv, nil)

	_, err := svc.CreateShipment(ctxWithClaims(uuid.NewString(), authn.RoleCustomer), "Bearer t", validCreateReq())
	requireAppError(t, err, apperror.CodeInternal, 500)
	require.Equal(t, []string{reservationID}, inv.released, "must compensate by releasing the reservation")
	require.Empty(t, repo.shipments)
}

func TestCreateShipment_Validation(t *testing.T) {
	svc := New(newMockRepo(), &fakeInventory{}, nil)
	ctx := ctxWithClaims(uuid.NewString(), authn.RoleCustomer)

	bad := validCreateReq()
	bad.Quantity = 0
	_, err := svc.CreateShipment(ctx, "Bearer t", bad)
	requireAppError(t, err, apperror.CodeValidation, 400)

	bad = validCreateReq()
	bad.DestinationAddress = "   "
	_, err = svc.CreateShipment(ctx, "Bearer t", bad)
	requireAppError(t, err, apperror.CodeValidation, 400)

	bad = validCreateReq()
	bad.WarehouseID = "not-a-uuid"
	_, err = svc.CreateShipment(ctx, "Bearer t", bad)
	requireAppError(t, err, apperror.CodeValidation, 400)
}

// --- confirm ---

func seedShipment(repo *mockRepo, owner string, status model.ShipmentStatus) *model.Shipment {
	s := &model.Shipment{
		ID:            uuid.NewString(),
		OwnerUserID:   owner,
		ItemID:        uuid.NewString(),
		WarehouseID:   uuid.NewString(),
		Quantity:      2,
		Status:        status,
		ReservationID: uuid.NewString(),
	}
	repo.shipments[s.ID] = s
	return s
}

func TestConfirmShipment_Success(t *testing.T) {
	repo := newMockRepo()
	owner := uuid.NewString()
	sh := seedShipment(repo, owner, model.StatusPending)
	inv := &fakeInventory{}
	svc := New(repo, inv, nil)

	resp, err := svc.ConfirmShipment(ctxWithClaims(owner, authn.RoleCustomer), "Bearer t", sh.ID)
	require.NoError(t, err)
	require.Equal(t, "CONFIRMED", resp.Status)
	require.Equal(t, []string{sh.ReservationID}, inv.committed)
}

func TestConfirmShipment_IdempotentWhenConfirmed(t *testing.T) {
	repo := newMockRepo()
	owner := uuid.NewString()
	sh := seedShipment(repo, owner, model.StatusConfirmed)
	inv := &fakeInventory{}
	svc := New(repo, inv, nil)

	resp, err := svc.ConfirmShipment(ctxWithClaims(owner, authn.RoleCustomer), "Bearer t", sh.ID)
	require.NoError(t, err)
	require.Equal(t, "CONFIRMED", resp.Status)
	require.Empty(t, inv.committed, "must not double-commit")
}

func TestConfirmShipment_CancelledIsInvalidTransition(t *testing.T) {
	repo := newMockRepo()
	owner := uuid.NewString()
	sh := seedShipment(repo, owner, model.StatusCancelled)
	svc := New(repo, &fakeInventory{}, nil)

	_, err := svc.ConfirmShipment(ctxWithClaims(owner, authn.RoleCustomer), "Bearer t", sh.ID)
	requireAppError(t, err, "INVALID_STATE_TRANSITION", 409)
}

func TestConfirmShipment_ReservationExpiredCancels(t *testing.T) {
	repo := newMockRepo()
	owner := uuid.NewString()
	sh := seedShipment(repo, owner, model.StatusPending)
	inv := &fakeInventory{commitErr: apperror.Conflict("RESERVATION_EXPIRED", "expired")}
	svc := New(repo, inv, nil)

	_, err := svc.ConfirmShipment(ctxWithClaims(owner, authn.RoleCustomer), "Bearer t", sh.ID)
	requireAppError(t, err, "RESERVATION_EXPIRED", 409)
	require.Equal(t, model.StatusCancelled, repo.shipments[sh.ID].Status, "shipment must be cancelled (reservation_expired)")
}

func TestConfirmShipment_DependencyUnavailableLeavesPending(t *testing.T) {
	repo := newMockRepo()
	owner := uuid.NewString()
	sh := seedShipment(repo, owner, model.StatusPending)
	inv := &fakeInventory{commitErr: apperror.DependencyUnavailable("down")}
	svc := New(repo, inv, nil)

	_, err := svc.ConfirmShipment(ctxWithClaims(owner, authn.RoleCustomer), "Bearer t", sh.ID)
	requireAppError(t, err, apperror.CodeDependencyUnavailable, 502)
	require.Equal(t, model.StatusPending, repo.shipments[sh.ID].Status, "must stay PENDING for retry")
}

// --- cancel ---

func TestCancelShipment_Success(t *testing.T) {
	repo := newMockRepo()
	owner := uuid.NewString()
	sh := seedShipment(repo, owner, model.StatusPending)
	inv := &fakeInventory{}
	svc := New(repo, inv, nil)

	resp, err := svc.CancelShipment(ctxWithClaims(owner, authn.RoleCustomer), "Bearer t", sh.ID)
	require.NoError(t, err)
	require.Equal(t, "CANCELLED", resp.Status)
	require.Equal(t, []string{sh.ReservationID}, inv.released)
}

func TestCancelShipment_ConfirmedIsInvalidTransition(t *testing.T) {
	repo := newMockRepo()
	owner := uuid.NewString()
	sh := seedShipment(repo, owner, model.StatusConfirmed)
	svc := New(repo, &fakeInventory{}, nil)

	_, err := svc.CancelShipment(ctxWithClaims(owner, authn.RoleCustomer), "Bearer t", sh.ID)
	requireAppError(t, err, "INVALID_STATE_TRANSITION", 409)
}

func TestCancelShipment_IdempotentWhenCancelled(t *testing.T) {
	repo := newMockRepo()
	owner := uuid.NewString()
	sh := seedShipment(repo, owner, model.StatusCancelled)
	inv := &fakeInventory{}
	svc := New(repo, inv, nil)

	resp, err := svc.CancelShipment(ctxWithClaims(owner, authn.RoleCustomer), "Bearer t", sh.ID)
	require.NoError(t, err)
	require.Equal(t, "CANCELLED", resp.Status)
	require.Empty(t, inv.released, "must not re-release")
}

// --- authorization ---

func TestGetShipment_OwnershipAuthz(t *testing.T) {
	repo := newMockRepo()
	owner := uuid.NewString()
	sh := seedShipment(repo, owner, model.StatusPending)
	svc := New(repo, &fakeInventory{}, nil)

	_, err := svc.GetShipment(ctxWithClaims(uuid.NewString(), authn.RoleCustomer), sh.ID)
	requireAppError(t, err, apperror.CodeForbidden, 403)

	_, err = svc.GetShipment(ctxWithClaims(owner, authn.RoleCustomer), sh.ID)
	require.NoError(t, err)

	_, err = svc.GetShipment(ctxWithClaims(uuid.NewString(), authn.RoleOperator), sh.ID)
	require.NoError(t, err, "operator can read any shipment")
}

func TestGetShipment_NotFound(t *testing.T) {
	svc := New(newMockRepo(), &fakeInventory{}, nil)
	_, err := svc.GetShipment(ctxWithClaims(uuid.NewString(), authn.RoleCustomer), uuid.NewString())
	requireAppError(t, err, apperror.CodeNotFound, 404)
}

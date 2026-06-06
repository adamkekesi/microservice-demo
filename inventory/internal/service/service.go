// Package service holds the Inventory business logic: validation, existence
// checks, ownership authorization, idempotency orchestration and domain
// metrics. It has no Gin/HTTP types so it is unit-testable with a mock repo.
package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/adamkekesi/microservice-demo/inventory/internal/model"
	"github.com/adamkekesi/microservice-demo/inventory/internal/repository"
	"github.com/adamkekesi/microservice-demo/platform/apperror"
	"github.com/adamkekesi/microservice-demo/platform/authn"
	"github.com/adamkekesi/microservice-demo/platform/observability"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Service implements the Inventory use cases.
type Service struct {
	repo       repository.Repository
	defaultTTL time.Duration
	metrics    *observability.Metrics
	now        func() time.Time
}

// New builds an Inventory service. defaultTTL is RESERVATION_TTL_SECONDS.
func New(repo repository.Repository, defaultTTL time.Duration, metrics *observability.Metrics) *Service {
	return &Service{repo: repo, defaultTTL: defaultTTL, metrics: metrics, now: time.Now}
}

// --- Administration ---

func (s *Service) CreateWarehouse(ctx context.Context, req model.CreateWarehouseRequest) (*model.WarehouseResponse, error) {
	if strings.TrimSpace(req.Code) == "" || strings.TrimSpace(req.Name) == "" {
		return nil, apperror.Validation("code and name are required", map[string]any{"code": "required", "name": "required"})
	}
	w := &model.Warehouse{ID: uuid.NewString(), Code: req.Code, Name: req.Name}
	if err := s.repo.CreateWarehouse(ctx, w); err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, apperror.Conflict(repository.CodeCodeTaken, "warehouse code already exists")
		}
		return nil, apperror.Internal("").Wrap(err)
	}
	resp := w.ToResponse()
	return &resp, nil
}

func (s *Service) CreateItem(ctx context.Context, req model.CreateItemRequest) (*model.ItemResponse, error) {
	if strings.TrimSpace(req.SKU) == "" || strings.TrimSpace(req.Name) == "" {
		return nil, apperror.Validation("sku and name are required", map[string]any{"sku": "required", "name": "required"})
	}
	i := &model.Item{ID: uuid.NewString(), SKU: req.SKU, Name: req.Name}
	if err := s.repo.CreateItem(ctx, i); err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, apperror.Conflict(repository.CodeSKUTaken, "item sku already exists")
		}
		return nil, apperror.Internal("").Wrap(err)
	}
	resp := i.ToResponse()
	return &resp, nil
}

func (s *Service) ListWarehouses(ctx context.Context) ([]model.WarehouseResponse, error) {
	ws, err := s.repo.ListWarehouses(ctx)
	if err != nil {
		return nil, apperror.Internal("").Wrap(err)
	}
	out := make([]model.WarehouseResponse, 0, len(ws))
	for i := range ws {
		out = append(out, ws[i].ToResponse())
	}
	return out, nil
}

func (s *Service) ListItems(ctx context.Context) ([]model.ItemResponse, error) {
	items, err := s.repo.ListItems(ctx)
	if err != nil {
		return nil, apperror.Internal("").Wrap(err)
	}
	out := make([]model.ItemResponse, 0, len(items))
	for i := range items {
		out = append(out, items[i].ToResponse())
	}
	return out, nil
}

func (s *Service) SetStock(ctx context.Context, req model.SetStockRequest) (*model.SetStockResponse, error) {
	if !validUUID(req.WarehouseID) || !validUUID(req.ItemID) {
		return nil, apperror.Validation("warehouse_id and item_id must be valid UUIDs", nil)
	}
	if req.QuantityOnHand == nil || *req.QuantityOnHand < 0 {
		return nil, apperror.Validation("quantity_on_hand must be an integer >= 0", map[string]any{"quantity_on_hand": "required, >= 0"})
	}
	if err := s.requireWarehouseAndItem(ctx, req.WarehouseID, req.ItemID); err != nil {
		return nil, err
	}
	st, err := s.repo.SetStock(ctx, req.WarehouseID, req.ItemID, *req.QuantityOnHand, s.now())
	if err != nil {
		return nil, mapErr(err)
	}
	return &model.SetStockResponse{WarehouseID: st.WarehouseID, ItemID: st.ItemID, QuantityOnHand: st.QuantityOnHand}, nil
}

func (s *Service) GetStock(ctx context.Context, warehouseID, itemID string) (*model.StockResponse, error) {
	if !validUUID(warehouseID) || !validUUID(itemID) {
		return nil, apperror.Validation("warehouse_id and item_id must be valid UUIDs", nil)
	}
	if err := s.requireWarehouseAndItem(ctx, warehouseID, itemID); err != nil {
		return nil, err
	}
	onHand, reserved, available, err := s.repo.GetStockView(ctx, warehouseID, itemID)
	if err != nil {
		return nil, apperror.Internal("").Wrap(err)
	}
	return &model.StockResponse{
		WarehouseID:       warehouseID,
		ItemID:            itemID,
		QuantityOnHand:    onHand,
		QuantityReserved:  reserved,
		QuantityAvailable: available,
	}, nil
}

// --- Reservations ---

// CreateReservation reserves stock. The returned bool reports whether a new
// reservation was created (201) or an existing idempotent one was returned (200).
func (s *Service) CreateReservation(ctx context.Context, req model.CreateReservationRequest) (*model.ReservationResponse, bool, error) {
	if req.Quantity <= 0 {
		return nil, false, apperror.Validation("quantity must be > 0", map[string]any{"quantity": "must be > 0"})
	}
	if !validUUID(req.WarehouseID) || !validUUID(req.ItemID) {
		return nil, false, apperror.Validation("warehouse_id and item_id must be valid UUIDs", nil)
	}
	if req.TTLSeconds != nil && *req.TTLSeconds <= 0 {
		return nil, false, apperror.Validation("ttl_seconds must be > 0", map[string]any{"ttl_seconds": "must be > 0"})
	}
	if err := s.requireWarehouseAndItem(ctx, req.WarehouseID, req.ItemID); err != nil {
		return nil, false, err
	}
	claims, ok := authn.ClaimsFrom(ctx)
	if !ok {
		return nil, false, apperror.Unauthenticated("")
	}

	ttl := s.defaultTTL
	if req.TTLSeconds != nil {
		ttl = time.Duration(*req.TTLSeconds) * time.Second
	}
	key := ""
	if req.IdempotencyKey != nil {
		key = *req.IdempotencyKey
	}
	now := s.now()

	res, created, err := s.repo.Reserve(ctx, repository.ReserveParams{
		WarehouseID:    req.WarehouseID,
		ItemID:         req.ItemID,
		Quantity:       req.Quantity,
		IdempotencyKey: key,
		ReservedBy:     claims.Subject(),
		ExpiresAt:      now.Add(ttl),
		Now:            now,
	})
	if err != nil {
		var ae *apperror.AppError
		if errors.As(err, &ae) && ae.Code == repository.CodeInsufficientStock {
			s.metrics.Incr("logistics.reservation.insufficient_stock")
		}
		return nil, false, mapErr(err)
	}
	resp := res.ToResponse()
	return &resp, created, nil
}

func (s *Service) GetReservation(ctx context.Context, id string) (*model.ReservationResponse, error) {
	res, err := s.loadAndAuthorize(ctx, id)
	if err != nil {
		return nil, err
	}
	resp := res.ToResponse()
	return &resp, nil
}

func (s *Service) CommitReservation(ctx context.Context, id string) (*model.ReservationResponse, error) {
	if _, err := s.loadAndAuthorize(ctx, id); err != nil {
		return nil, err
	}
	start := time.Now()
	updated, err := s.repo.Commit(ctx, id, s.now())
	s.metrics.Timing("logistics.inventory.commit.latency", time.Since(start))
	if err != nil {
		return nil, mapErr(err)
	}
	resp := updated.ToResponse()
	return &resp, nil
}

func (s *Service) ReleaseReservation(ctx context.Context, id string) (*model.ReservationResponse, error) {
	if _, err := s.loadAndAuthorize(ctx, id); err != nil {
		return nil, err
	}
	updated, err := s.repo.Release(ctx, id)
	if err != nil {
		return nil, mapErr(err)
	}
	resp := updated.ToResponse()
	return &resp, nil
}

// loadAndAuthorize fetches a reservation and checks the caller may act on it
// (owner or operator/admin), returning the standard 404/403 errors.
func (s *Service) loadAndAuthorize(ctx context.Context, id string) (*model.Reservation, error) {
	if !validUUID(id) {
		return nil, apperror.NotFound("reservation not found")
	}
	res, err := s.repo.GetReservation(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperror.NotFound("reservation not found")
		}
		return nil, apperror.Internal("").Wrap(err)
	}
	claims, ok := authn.ClaimsFrom(ctx)
	if !ok {
		return nil, apperror.Unauthenticated("")
	}
	if !claims.Role.IsOperatorOrAdmin() && claims.Subject() != res.ReservedBy {
		return nil, apperror.Forbidden("")
	}
	return res, nil
}

func (s *Service) requireWarehouseAndItem(ctx context.Context, warehouseID, itemID string) error {
	okW, err := s.repo.WarehouseExists(ctx, warehouseID)
	if err != nil {
		return apperror.Internal("").Wrap(err)
	}
	if !okW {
		return apperror.NotFound("warehouse not found")
	}
	okI, err := s.repo.ItemExists(ctx, itemID)
	if err != nil {
		return apperror.Internal("").Wrap(err)
	}
	if !okI {
		return apperror.NotFound("item not found")
	}
	return nil
}

func validUUID(v string) bool {
	_, err := uuid.Parse(v)
	return err == nil
}

func mapErr(err error) error {
	if err == nil {
		return nil
	}
	var ae *apperror.AppError
	if errors.As(err, &ae) {
		return ae
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return apperror.NotFound("")
	}
	return apperror.Internal("").Wrap(err)
}

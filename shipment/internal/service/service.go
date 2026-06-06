// Package service holds the Shipment saga orchestration and state machine
// (Feature Spec §5). It depends on the InventoryClient interface, so the saga
// branches are unit-testable with a fake (no network).
package service

import (
	"context"
	"errors"
	"strings"

	"github.com/adamkekesi/microservice-demo/platform/apperror"
	"github.com/adamkekesi/microservice-demo/platform/authn"
	"github.com/adamkekesi/microservice-demo/platform/observability"
	"github.com/adamkekesi/microservice-demo/shipment/internal/client"
	"github.com/adamkekesi/microservice-demo/shipment/internal/model"
	"github.com/adamkekesi/microservice-demo/shipment/internal/repository"
	"github.com/google/uuid"
)

const (
	defaultLimit = 50
	maxLimit     = 200

	codeInvalidStateTransition = "INVALID_STATE_TRANSITION"
	codeInsufficientStock      = "INSUFFICIENT_STOCK"
	codeReservationExpired     = "RESERVATION_EXPIRED"
)

// Service implements the Shipment use cases.
type Service struct {
	repo      repository.Repository
	inventory client.InventoryClient
	metrics   *observability.Metrics
}

// New builds a Shipment service.
func New(repo repository.Repository, inventory client.InventoryClient, metrics *observability.Metrics) *Service {
	return &Service{repo: repo, inventory: inventory, metrics: metrics}
}

// CreateShipment runs the saga: reserve stock in Inventory, then persist the
// shipment; if persistence fails, compensate by releasing the reservation
// (Feature Spec §5.3).
func (s *Service) CreateShipment(ctx context.Context, authorization string, req model.CreateShipmentRequest) (*model.ShipmentResponse, error) {
	if req.Quantity <= 0 {
		return nil, apperror.Validation("quantity must be > 0", map[string]any{"quantity": "must be > 0"})
	}
	if strings.TrimSpace(req.DestinationAddress) == "" {
		return nil, apperror.Validation("destination_address must be non-empty", map[string]any{"destination_address": "required"})
	}
	if !validUUID(req.ItemID) || !validUUID(req.WarehouseID) {
		return nil, apperror.Validation("item_id and warehouse_id must be valid UUIDs", nil)
	}
	claims, ok := authn.ClaimsFrom(ctx)
	if !ok {
		return nil, apperror.Unauthenticated("")
	}

	// 1+2. Reserve stock (idempotency key scoped to this attempt).
	reservation, err := s.inventory.Reserve(ctx, authorization, client.ReserveRequest{
		WarehouseID:    req.WarehouseID,
		ItemID:         req.ItemID,
		Quantity:       req.Quantity,
		IdempotencyKey: uuid.NewString(),
	})
	if err != nil {
		return nil, s.mapReserveError(err)
	}

	// 3. Persist the shipment as PENDING.
	sh := &model.Shipment{
		ID:                 uuid.NewString(),
		OwnerUserID:        claims.Subject(),
		ItemID:             req.ItemID,
		WarehouseID:        req.WarehouseID,
		Quantity:           req.Quantity,
		DestinationAddress: req.DestinationAddress,
		Status:             model.StatusPending,
		ReservationID:      reservation.ID,
	}
	if err := s.repo.Create(ctx, sh); err != nil {
		// Compensation: release the reservation best-effort. If it also fails,
		// the reservation TTL is the safety net. Never leave a committed-but-
		// unrecorded reservation.
		_ = s.inventory.Release(ctx, authorization, reservation.ID)
		return nil, apperror.Internal("could not persist shipment").Wrap(err)
	}

	s.metrics.Incr("logistics.shipment.created", "status:PENDING")
	resp := sh.ToResponse()
	return &resp, nil
}

// ConfirmShipment commits the reservation and moves PENDING -> CONFIRMED.
func (s *Service) ConfirmShipment(ctx context.Context, authorization, id string) (*model.ShipmentResponse, error) {
	sh, err := s.loadAndAuthorize(ctx, id)
	if err != nil {
		return nil, err
	}
	switch sh.Status {
	case model.StatusConfirmed:
		resp := sh.ToResponse() // idempotent no-op, no double commit
		return &resp, nil
	case model.StatusCancelled:
		return nil, apperror.Conflict(codeInvalidStateTransition, "cannot confirm a cancelled shipment")
	}

	if err := s.inventory.Commit(ctx, authorization, sh.ReservationID); err != nil {
		var ae *apperror.AppError
		if errors.As(err, &ae) && ae.Code == codeReservationExpired {
			// Stock already freed by lazy expiry; just cancel the shipment.
			reason := model.ReasonReservationExpired
			if terr := s.repo.Transition(ctx, sh, model.StatusCancelled, &reason); terr != nil {
				return nil, apperror.Internal("could not record cancellation").Wrap(terr)
			}
			s.metrics.Incr("logistics.shipment.cancelled", "reason:reservation_expired")
			return nil, apperror.Conflict(codeReservationExpired, "reservation expired; shipment cancelled")
		}
		return nil, asDependencyError(err) // leave shipment PENDING; client may retry
	}

	if err := s.repo.Transition(ctx, sh, model.StatusConfirmed, nil); err != nil {
		return nil, apperror.Internal("could not record confirmation").Wrap(err)
	}
	s.metrics.Incr("logistics.shipment.confirmed")
	resp := sh.ToResponse()
	return &resp, nil
}

// CancelShipment releases the reservation and moves PENDING -> CANCELLED.
func (s *Service) CancelShipment(ctx context.Context, authorization, id string) (*model.ShipmentResponse, error) {
	sh, err := s.loadAndAuthorize(ctx, id)
	if err != nil {
		return nil, err
	}
	switch sh.Status {
	case model.StatusCancelled:
		resp := sh.ToResponse() // idempotent no-op
		return &resp, nil
	case model.StatusConfirmed:
		return nil, apperror.Conflict(codeInvalidStateTransition, "cannot cancel a confirmed shipment")
	}

	if err := s.inventory.Release(ctx, authorization, sh.ReservationID); err != nil {
		return nil, asDependencyError(err) // leave shipment PENDING
	}
	reason := model.ReasonUserCancelled
	if err := s.repo.Transition(ctx, sh, model.StatusCancelled, &reason); err != nil {
		return nil, apperror.Internal("could not record cancellation").Wrap(err)
	}
	s.metrics.Incr("logistics.shipment.cancelled", "reason:user_cancelled")
	resp := sh.ToResponse()
	return &resp, nil
}

// GetShipment returns one shipment if the caller may see it.
func (s *Service) GetShipment(ctx context.Context, id string) (*model.ShipmentResponse, error) {
	sh, err := s.loadAndAuthorize(ctx, id)
	if err != nil {
		return nil, err
	}
	resp := sh.ToResponse()
	return &resp, nil
}

// ListShipments returns the caller's shipments; operator/admin may pass all=true.
func (s *Service) ListShipments(ctx context.Context, all bool, limit, offset int) ([]model.ShipmentResponse, error) {
	claims, ok := authn.ClaimsFrom(ctx)
	if !ok {
		return nil, apperror.Unauthenticated("")
	}
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	if offset < 0 {
		offset = 0
	}
	effectiveAll := all && claims.Role.IsOperatorOrAdmin()

	shipments, err := s.repo.List(ctx, claims.Subject(), effectiveAll, limit, offset)
	if err != nil {
		return nil, apperror.Internal("").Wrap(err)
	}
	out := make([]model.ShipmentResponse, 0, len(shipments))
	for i := range shipments {
		out = append(out, shipments[i].ToResponse())
	}
	return out, nil
}

func (s *Service) loadAndAuthorize(ctx context.Context, id string) (*model.Shipment, error) {
	if !validUUID(id) {
		return nil, apperror.NotFound("shipment not found")
	}
	sh, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, apperror.NotFound("shipment not found")
	}
	claims, ok := authn.ClaimsFrom(ctx)
	if !ok {
		return nil, apperror.Unauthenticated("")
	}
	if !claims.Role.IsOperatorOrAdmin() && claims.Subject() != sh.OwnerUserID {
		return nil, apperror.Forbidden("")
	}
	return sh, nil
}

// mapReserveError translates an Inventory reserve failure to the outward error
// (Feature Spec §5.3): pass through INSUFFICIENT_STOCK (409) and NOT_FOUND
// (404); everything else is a dependency failure (502). No shipment is created.
func (s *Service) mapReserveError(err error) error {
	var ae *apperror.AppError
	if errors.As(err, &ae) {
		switch ae.Code {
		case codeInsufficientStock:
			s.metrics.Incr("logistics.shipment.create.insufficient_stock")
			return ae
		case apperror.CodeNotFound:
			return apperror.NotFound("item or warehouse not found")
		case apperror.CodeDependencyUnavailable:
			return ae
		}
	}
	return apperror.DependencyUnavailable("inventory reservation failed")
}

// asDependencyError preserves a DEPENDENCY_UNAVAILABLE and converts anything
// else into one (used for confirm/cancel where the shipment stays PENDING).
func asDependencyError(err error) error {
	var ae *apperror.AppError
	if errors.As(err, &ae) && ae.Code == apperror.CodeDependencyUnavailable {
		return ae
	}
	return apperror.DependencyUnavailable("inventory call failed")
}

func validUUID(v string) bool {
	_, err := uuid.Parse(v)
	return err == nil
}

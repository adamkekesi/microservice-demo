// Package repository owns Inventory's GORM data access, including the
// row-locked transactions that make the stock math correct under concurrency
// (Feature Spec §4.2). Domain conflicts decided inside a transaction are
// returned as *apperror.AppError; plain not-found is returned as
// gorm.ErrRecordNotFound for the service layer to map.
package repository

import (
	"context"
	"errors"
	"time"

	"github.com/adamkekesi/microservice-demo/inventory/internal/model"
	"github.com/adamkekesi/microservice-demo/platform/apperror"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Conflict subcodes (Feature Spec §4).
const (
	CodeCodeTaken                   = "CODE_TAKEN"
	CodeSKUTaken                    = "SKU_TAKEN"
	CodeInsufficientStock           = "INSUFFICIENT_STOCK"
	CodeStockBelowReserved          = "STOCK_BELOW_RESERVED"
	CodeStockUnderflow              = "STOCK_UNDERFLOW"
	CodeReservationExpired          = "RESERVATION_EXPIRED"
	CodeReservationNotActive        = "RESERVATION_NOT_ACTIVE"
	CodeReservationAlreadyCommitted = "RESERVATION_ALREADY_COMMITTED"
)

// ReserveParams carries everything needed to create a reservation.
type ReserveParams struct {
	WarehouseID    string
	ItemID         string
	Quantity       int
	IdempotencyKey string // empty when none
	ReservedBy     string
	ExpiresAt      time.Time
	Now            time.Time
}

// Repository is the Inventory data-access contract.
type Repository interface {
	CreateWarehouse(ctx context.Context, w *model.Warehouse) error
	ListWarehouses(ctx context.Context) ([]model.Warehouse, error)
	WarehouseExists(ctx context.Context, id string) (bool, error)

	CreateItem(ctx context.Context, i *model.Item) error
	ListItems(ctx context.Context) ([]model.Item, error)
	ItemExists(ctx context.Context, id string) (bool, error)
	// DeleteItem removes an item by id; its stock rows cascade (FK ON DELETE
	// CASCADE). Returns gorm.ErrRecordNotFound when no row matched.
	DeleteItem(ctx context.Context, id string) error
	// PurgeItemsBySKUPrefix bulk-deletes items whose SKU starts with prefix;
	// their stock rows cascade. Returns rows deleted.
	PurgeItemsBySKUPrefix(ctx context.Context, prefix string) (int64, error)

	SetStock(ctx context.Context, warehouseID, itemID string, onHand int, now time.Time) (*model.Stock, error)
	GetStockView(ctx context.Context, warehouseID, itemID string) (onHand, reserved, available int, err error)

	Reserve(ctx context.Context, p ReserveParams) (res *model.Reservation, created bool, err error)
	GetReservation(ctx context.Context, id string) (*model.Reservation, error)
	Commit(ctx context.Context, id string, now time.Time) (*model.Reservation, error)
	Release(ctx context.Context, id string) (*model.Reservation, error)
	// DeleteReservation removes a single reservation row by id.
	DeleteReservation(ctx context.Context, id string) error
	// PurgeTerminalReservationsBefore bulk-deletes terminal (COMMITTED/RELEASED)
	// reservations created before the cutoff. Returns rows deleted.
	PurgeTerminalReservationsBefore(ctx context.Context, before time.Time) (int64, error)
	// PurgeExpiredPendingReservations bulk-deletes PENDING reservations whose
	// expires_at is already in the past — dead rows that lazy expiry leaves behind
	// and that can never become active again. Returns rows deleted.
	PurgeExpiredPendingReservations(ctx context.Context, now time.Time) (int64, error)
}

type repo struct{ db *gorm.DB }

// New builds a GORM-backed Repository.
func New(db *gorm.DB) Repository { return &repo{db: db} }

func (r *repo) CreateWarehouse(ctx context.Context, w *model.Warehouse) error {
	return r.db.WithContext(ctx).Create(w).Error
}

func (r *repo) ListWarehouses(ctx context.Context) ([]model.Warehouse, error) {
	var out []model.Warehouse
	return out, r.db.WithContext(ctx).Order("created_at").Find(&out).Error
}

func (r *repo) WarehouseExists(ctx context.Context, id string) (bool, error) {
	return r.exists(ctx, &model.Warehouse{}, id)
}

func (r *repo) CreateItem(ctx context.Context, i *model.Item) error {
	return r.db.WithContext(ctx).Create(i).Error
}

func (r *repo) ListItems(ctx context.Context) ([]model.Item, error) {
	var out []model.Item
	return out, r.db.WithContext(ctx).Order("created_at").Find(&out).Error
}

func (r *repo) ItemExists(ctx context.Context, id string) (bool, error) {
	return r.exists(ctx, &model.Item{}, id)
}

// DeleteItem deletes the item; the stock_item_id_fkey ON DELETE CASCADE removes
// its stock rows in the same statement. Reservations have no FK to items, so
// they are left untouched (the retention sweep handles terminal ones).
func (r *repo) DeleteItem(ctx context.Context, id string) error {
	res := r.db.WithContext(ctx).Where("id = ?", id).Delete(&model.Item{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// PurgeItemsBySKUPrefix deletes every item whose SKU begins with prefix; the
// stock_item_id_fkey ON DELETE CASCADE removes their stock rows. The caller
// guarantees a non-empty prefix so this never wipes the whole catalog.
func (r *repo) PurgeItemsBySKUPrefix(ctx context.Context, prefix string) (int64, error) {
	res := r.db.WithContext(ctx).Where("sku LIKE ?", prefix+"%").Delete(&model.Item{})
	return res.RowsAffected, res.Error
}

func (r *repo) exists(ctx context.Context, m any, id string) (bool, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(m).Where("id = ?", id).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// sumActive returns the total quantity of ACTIVE reservations (PENDING and not
// yet expired) for a (warehouse, item) pair (Spec §4.2). Callers run this inside
// the locked tx. The "active" predicate mirrors stock.Active (the unit-tested
// source of truth) — KEEP THE TWO IN SYNC — but is evaluated in SQL: the DB
// filters via idx_reservations_active and returns a single SUM, instead of
// loading every PENDING row (including long-expired ones) into Go. `now` is bound
// as a parameter so the whole operation shares one notion of "now".
func sumActive(tx *gorm.DB, warehouseID, itemID string, now time.Time) (int, error) {
	var total int64
	err := tx.Model(&model.Reservation{}).
		Where("warehouse_id = ? AND item_id = ? AND status = ? AND expires_at > ?",
			warehouseID, itemID, model.StatusPending, now).
		Select("COALESCE(SUM(quantity), 0)"). // SUM over zero rows is NULL
		Scan(&total).Error
	if err != nil {
		return 0, err
	}
	return int(total), nil
}

func (r *repo) GetStockView(ctx context.Context, warehouseID, itemID string) (int, int, int, error) {
	var stock model.Stock
	err := r.db.WithContext(ctx).
		Where("warehouse_id = ? AND item_id = ?", warehouseID, itemID).First(&stock).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, 0, 0, nil // no row yet => all zero (Spec §4.3)
	}
	if err != nil {
		return 0, 0, 0, err
	}
	reserved, err := sumActive(r.db.WithContext(ctx), warehouseID, itemID, time.Now())
	if err != nil {
		return 0, 0, 0, err
	}
	return stock.QuantityOnHand, reserved, stock.QuantityOnHand - reserved, nil
}

// SetStock upserts the stock row to an absolute on-hand value, rejecting a
// value below the currently reserved quantity (Spec §4.3).
func (r *repo) SetStock(ctx context.Context, warehouseID, itemID string, onHand int, now time.Time) (*model.Stock, error) {
	var result *model.Stock
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var stock model.Stock
		serr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("warehouse_id = ? AND item_id = ?", warehouseID, itemID).First(&stock).Error
		exists := serr == nil
		if serr != nil && !errors.Is(serr, gorm.ErrRecordNotFound) {
			return serr
		}

		reserved, e := sumActive(tx, warehouseID, itemID, now)
		if e != nil {
			return e
		}
		if onHand < reserved {
			return apperror.Conflict(CodeStockBelowReserved,
				"cannot set on-hand below the currently reserved quantity").
				WithDetails(map[string]any{"reserved": reserved})
		}

		if exists {
			stock.QuantityOnHand = onHand
			if e := tx.Save(&stock).Error; e != nil {
				return e
			}
		} else {
			stock = model.Stock{
				ID:             uuid.NewString(),
				WarehouseID:    warehouseID,
				ItemID:         itemID,
				QuantityOnHand: onHand,
			}
			if e := tx.Create(&stock).Error; e != nil {
				return e
			}
		}
		result = &stock
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// Reserve creates a PENDING reservation inside a transaction that locks the
// stock row, so two concurrent reserves that jointly exceed availability
// produce exactly one success and one INSUFFICIENT_STOCK (Spec §4.2).
func (r *repo) Reserve(ctx context.Context, p ReserveParams) (*model.Reservation, bool, error) {
	var result *model.Reservation
	created := false

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Acquire the stock-row lock first — this is the serialization point.
		var stock model.Stock
		onHand := 0
		serr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("warehouse_id = ? AND item_id = ?", p.WarehouseID, p.ItemID).First(&stock).Error
		switch {
		case serr == nil:
			onHand = stock.QuantityOnHand
		case errors.Is(serr, gorm.ErrRecordNotFound):
			onHand = 0
		default:
			return serr
		}

		// Re-check idempotency AFTER the lock so concurrent same-key requests
		// converge on the same reservation rather than double-reserving.
		if p.IdempotencyKey != "" {
			var existing model.Reservation
			e := tx.Where("idempotency_key = ?", p.IdempotencyKey).First(&existing).Error
			if e == nil {
				result = &existing
				created = false
				return nil
			}
			if !errors.Is(e, gorm.ErrRecordNotFound) {
				return e
			}
		}

		reserved, e := sumActive(tx, p.WarehouseID, p.ItemID, p.Now)
		if e != nil {
			return e
		}
		available := onHand - reserved
		if available < p.Quantity {
			return apperror.Conflict(CodeInsufficientStock, "insufficient stock to reserve").
				WithDetails(map[string]any{"available": available, "requested": p.Quantity})
		}

		var keyPtr *string
		if p.IdempotencyKey != "" {
			k := p.IdempotencyKey
			keyPtr = &k
		}
		res := &model.Reservation{
			ID:             uuid.NewString(),
			WarehouseID:    p.WarehouseID,
			ItemID:         p.ItemID,
			Quantity:       p.Quantity,
			Status:         model.StatusPending,
			IdempotencyKey: keyPtr,
			ReservedBy:     p.ReservedBy,
			ExpiresAt:      p.ExpiresAt,
		}
		if e := tx.Create(res).Error; e != nil {
			if errors.Is(e, gorm.ErrDuplicatedKey) && p.IdempotencyKey != "" {
				var existing model.Reservation
				if e2 := tx.Where("idempotency_key = ?", p.IdempotencyKey).First(&existing).Error; e2 == nil {
					result = &existing
					created = false
					return nil
				}
			}
			return e
		}
		result = res
		created = true
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return result, created, nil
}

func (r *repo) GetReservation(ctx context.Context, id string) (*model.Reservation, error) {
	var res model.Reservation
	if err := r.db.WithContext(ctx).First(&res, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &res, nil
}

func (r *repo) DeleteReservation(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&model.Reservation{}).Error
}

func (r *repo) PurgeTerminalReservationsBefore(ctx context.Context, before time.Time) (int64, error) {
	terminal := []model.ReservationStatus{model.StatusCommitted, model.StatusReleased}
	res := r.db.WithContext(ctx).
		Where("status IN ? AND created_at < ?", terminal, before).
		Delete(&model.Reservation{})
	return res.RowsAffected, res.Error
}

func (r *repo) PurgeExpiredPendingReservations(ctx context.Context, now time.Time) (int64, error) {
	res := r.db.WithContext(ctx).
		Where("status = ? AND expires_at < ?", model.StatusPending, now).
		Delete(&model.Reservation{})
	return res.RowsAffected, res.Error
}

// Commit decrements on-hand and marks the reservation COMMITTED, inside a
// transaction that locks the reservation and stock rows (Spec §4.3).
func (r *repo) Commit(ctx context.Context, id string, now time.Time) (*model.Reservation, error) {
	var result *model.Reservation
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var res model.Reservation
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&res, "id = ?", id).Error; e != nil {
			return e
		}
		switch res.Status {
		case model.StatusCommitted:
			result = &res // idempotent no-op
			return nil
		case model.StatusReleased:
			return apperror.Conflict(CodeReservationNotActive, "reservation is not active")
		case model.StatusPending:
			if !res.ExpiresAt.After(now) {
				return apperror.Conflict(CodeReservationExpired, "reservation has expired")
			}
			var stock model.Stock
			if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("warehouse_id = ? AND item_id = ?", res.WarehouseID, res.ItemID).First(&stock).Error; e != nil {
				if errors.Is(e, gorm.ErrRecordNotFound) {
					return apperror.Conflict(CodeStockUnderflow, "stock row missing for commit")
				}
				return e
			}
			if stock.QuantityOnHand < res.Quantity {
				return apperror.Conflict(CodeStockUnderflow, "on-hand is below the reservation quantity")
			}
			stock.QuantityOnHand -= res.Quantity
			if e := tx.Save(&stock).Error; e != nil {
				return e
			}
			res.Status = model.StatusCommitted
			if e := tx.Save(&res).Error; e != nil {
				return e
			}
			result = &res
			return nil
		default:
			return apperror.Internal("unknown reservation status")
		}
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// Release marks a PENDING reservation RELEASED (Spec §4.3).
func (r *repo) Release(ctx context.Context, id string) (*model.Reservation, error) {
	var result *model.Reservation
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var res model.Reservation
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&res, "id = ?", id).Error; e != nil {
			return e
		}
		switch res.Status {
		case model.StatusReleased:
			result = &res // idempotent no-op
			return nil
		case model.StatusCommitted:
			return apperror.Conflict(CodeReservationAlreadyCommitted, "cannot release committed stock")
		case model.StatusPending:
			res.Status = model.StatusReleased
			if e := tx.Save(&res).Error; e != nil {
				return e
			}
			result = &res
			return nil
		default:
			return apperror.Internal("unknown reservation status")
		}
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

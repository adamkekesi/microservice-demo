// Package repository owns Shipment's GORM data access. Every status change is
// persisted together with a history row in a single transaction.
package repository

import (
	"context"
	"time"

	"github.com/adamkekesi/microservice-demo/shipment/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Repository is the Shipment data-access contract.
type Repository interface {
	// Create persists a new shipment and its initial history row (from null).
	Create(ctx context.Context, s *model.Shipment) error
	Get(ctx context.Context, id string) (*model.Shipment, error)
	// Transition updates a shipment's status and appends a history row, atomically.
	Transition(ctx context.Context, s *model.Shipment, to model.ShipmentStatus, reason *string) error
	List(ctx context.Context, ownerID string, all bool, limit, offset int) ([]model.Shipment, error)
	// Delete removes a shipment and its status-history rows in one transaction.
	Delete(ctx context.Context, id string) error
	// PurgeTerminalBefore bulk-deletes terminal (CONFIRMED/CANCELLED) shipments
	// created before the cutoff, with their history. Returns rows deleted.
	PurgeTerminalBefore(ctx context.Context, before time.Time) (int64, error)
}

type repo struct{ db *gorm.DB }

// New builds a GORM-backed Repository.
func New(db *gorm.DB) Repository { return &repo{db: db} }

func (r *repo) Create(ctx context.Context, s *model.Shipment) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(s).Error; err != nil {
			return err
		}
		return tx.Create(&model.ShipmentStatusHistory{
			ID:         uuid.NewString(),
			ShipmentID: s.ID,
			FromStatus: nil, // initial creation
			ToStatus:   string(s.Status),
			Reason:     nil,
		}).Error
	})
}

func (r *repo) Get(ctx context.Context, id string) (*model.Shipment, error) {
	var s model.Shipment
	if err := r.db.WithContext(ctx).First(&s, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *repo) Transition(ctx context.Context, s *model.Shipment, to model.ShipmentStatus, reason *string) error {
	from := string(s.Status)
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		s.Status = to
		if err := tx.Save(s).Error; err != nil {
			return err
		}
		return tx.Create(&model.ShipmentStatusHistory{
			ID:         uuid.NewString(),
			ShipmentID: s.ID,
			FromStatus: &from,
			ToStatus:   string(to),
			Reason:     reason,
		}).Error
	})
	if err != nil {
		s.Status = model.ShipmentStatus(from) // roll back the in-memory change
		return err
	}
	return nil
}

func (r *repo) List(ctx context.Context, ownerID string, all bool, limit, offset int) ([]model.Shipment, error) {
	q := r.db.WithContext(ctx).Order("created_at DESC").Limit(limit).Offset(offset)
	if !all {
		q = q.Where("owner_user_id = ?", ownerID)
	}
	var out []model.Shipment
	return out, q.Find(&out).Error
}

func (r *repo) Delete(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// History references shipments(id), so it must go first.
		if err := tx.Where("shipment_id = ?", id).Delete(&model.ShipmentStatusHistory{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", id).Delete(&model.Shipment{}).Error
	})
}

func (r *repo) PurgeTerminalBefore(ctx context.Context, before time.Time) (int64, error) {
	terminal := []string{string(model.StatusConfirmed), string(model.StatusCancelled)}
	var deleted int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Delete history of the to-be-purged shipments first (FK), via subquery.
		ids := tx.Model(&model.Shipment{}).Select("id").
			Where("status IN ? AND created_at < ?", terminal, before)
		if err := tx.Where("shipment_id IN (?)", ids).
			Delete(&model.ShipmentStatusHistory{}).Error; err != nil {
			return err
		}
		res := tx.Where("status IN ? AND created_at < ?", terminal, before).Delete(&model.Shipment{})
		deleted = res.RowsAffected
		return res.Error
	})
	return deleted, err
}

// Package model holds the Shipment service's GORM models and DTOs.
package model

import "time"

// ShipmentStatus enumerates the shipment lifecycle (Feature Spec §5.2).
type ShipmentStatus string

const (
	StatusPending   ShipmentStatus = "PENDING"
	StatusConfirmed ShipmentStatus = "CONFIRMED"
	StatusCancelled ShipmentStatus = "CANCELLED"
)

// History reasons.
const (
	ReasonUserCancelled      = "user_cancelled"
	ReasonReservationExpired = "reservation_expired"
)

// Shipment is a single-item parcel shipment (exactly one item per shipment).
type Shipment struct {
	ID                 string         `gorm:"type:uuid;primaryKey"`
	OwnerUserID        string         `gorm:"type:uuid;not null"`
	ItemID             string         `gorm:"type:uuid;not null"`
	WarehouseID        string         `gorm:"type:uuid;not null"`
	Quantity           int            `gorm:"not null"`
	DestinationAddress string         `gorm:"not null"`
	Status             ShipmentStatus `gorm:"type:text;not null"`
	ReservationID      string         `gorm:"type:uuid;not null"`
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

func (Shipment) TableName() string { return "shipments" }

// ShipmentStatusHistory records every status change, including creation
// (from_status null).
type ShipmentStatusHistory struct {
	ID         string  `gorm:"type:uuid;primaryKey"`
	ShipmentID string  `gorm:"type:uuid;not null"`
	FromStatus *string `gorm:"type:text"`
	ToStatus   string  `gorm:"type:text;not null"`
	Reason     *string `gorm:"type:text"`
	CreatedAt  time.Time
}

func (ShipmentStatusHistory) TableName() string { return "shipment_status_history" }

// --- DTOs ---

type CreateShipmentRequest struct {
	ItemID             string `json:"item_id"`
	WarehouseID        string `json:"warehouse_id"`
	Quantity           int    `json:"quantity"`
	DestinationAddress string `json:"destination_address"`
}

type ShipmentResponse struct {
	ID                 string `json:"id"`
	Status             string `json:"status"`
	ItemID             string `json:"item_id"`
	WarehouseID        string `json:"warehouse_id"`
	Quantity           int    `json:"quantity"`
	DestinationAddress string `json:"destination_address"`
	ReservationID      string `json:"reservation_id"`
	CreatedAt          string `json:"created_at"`
}

// ToResponse projects a Shipment to its public representation.
func (s *Shipment) ToResponse() ShipmentResponse {
	return ShipmentResponse{
		ID:                 s.ID,
		Status:             string(s.Status),
		ItemID:             s.ItemID,
		WarehouseID:        s.WarehouseID,
		Quantity:           s.Quantity,
		DestinationAddress: s.DestinationAddress,
		ReservationID:      s.ReservationID,
		CreatedAt:          s.CreatedAt.UTC().Format(time.RFC3339),
	}
}

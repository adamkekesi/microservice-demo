// Package model holds the Inventory service's GORM models and DTOs.
package model

import "time"

// ReservationStatus enumerates the reservation lifecycle.
type ReservationStatus string

const (
	StatusPending   ReservationStatus = "PENDING"
	StatusCommitted ReservationStatus = "COMMITTED"
	StatusReleased  ReservationStatus = "RELEASED"
)

// Warehouse is a stock location.
type Warehouse struct {
	ID        string `gorm:"type:uuid;primaryKey"`
	Code      string `gorm:"uniqueIndex;not null"`
	Name      string `gorm:"not null"`
	CreatedAt time.Time
}

func (Warehouse) TableName() string { return "warehouses" }

// Item is a stockable product.
type Item struct {
	ID        string `gorm:"type:uuid;primaryKey"`
	SKU       string `gorm:"uniqueIndex;not null"`
	Name      string `gorm:"not null"`
	CreatedAt time.Time
}

func (Item) TableName() string { return "items" }

// Stock is the on-hand quantity for a (warehouse, item) pair.
type Stock struct {
	ID             string `gorm:"type:uuid;primaryKey"`
	WarehouseID    string `gorm:"type:uuid;not null;uniqueIndex:uq_stock_wh_item"`
	ItemID         string `gorm:"type:uuid;not null;uniqueIndex:uq_stock_wh_item"`
	QuantityOnHand int    `gorm:"not null;default:0"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (Stock) TableName() string { return "stock" }

// Reservation is a hold on stock. Active iff status=PENDING AND expires_at>now.
type Reservation struct {
	ID             string            `gorm:"type:uuid;primaryKey"`
	WarehouseID    string            `gorm:"type:uuid;not null"`
	ItemID         string            `gorm:"type:uuid;not null"`
	Quantity       int               `gorm:"not null"`
	Status         ReservationStatus `gorm:"type:text;not null"`
	IdempotencyKey *string           `gorm:"uniqueIndex"`
	ReservedBy     string            `gorm:"type:uuid;not null"`
	ExpiresAt      time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (Reservation) TableName() string { return "reservations" }

// --- DTOs ---

type CreateWarehouseRequest struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

type CreateItemRequest struct {
	SKU  string `json:"sku"`
	Name string `json:"name"`
}

type SetStockRequest struct {
	WarehouseID    string `json:"warehouse_id"`
	ItemID         string `json:"item_id"`
	QuantityOnHand *int   `json:"quantity_on_hand"`
}

type CreateReservationRequest struct {
	WarehouseID    string  `json:"warehouse_id"`
	ItemID         string  `json:"item_id"`
	Quantity       int     `json:"quantity"`
	IdempotencyKey *string `json:"idempotency_key,omitempty"`
	TTLSeconds     *int    `json:"ttl_seconds,omitempty"`
}

type WarehouseResponse struct {
	ID   string `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}

type ItemResponse struct {
	ID   string `json:"id"`
	SKU  string `json:"sku"`
	Name string `json:"name"`
}

type StockResponse struct {
	WarehouseID       string `json:"warehouse_id"`
	ItemID            string `json:"item_id"`
	QuantityOnHand    int    `json:"quantity_on_hand"`
	QuantityReserved  int    `json:"quantity_reserved"`
	QuantityAvailable int    `json:"quantity_available"`
}

// SetStockResponse is the (smaller) body returned by PUT /stock (Spec §4.3).
type SetStockResponse struct {
	WarehouseID    string `json:"warehouse_id"`
	ItemID         string `json:"item_id"`
	QuantityOnHand int    `json:"quantity_on_hand"`
}

type ReservationResponse struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	WarehouseID string `json:"warehouse_id"`
	ItemID      string `json:"item_id"`
	Quantity    int    `json:"quantity"`
	ExpiresAt   string `json:"expires_at"`
}

func (w *Warehouse) ToResponse() WarehouseResponse {
	return WarehouseResponse{ID: w.ID, Code: w.Code, Name: w.Name}
}

func (i *Item) ToResponse() ItemResponse {
	return ItemResponse{ID: i.ID, SKU: i.SKU, Name: i.Name}
}

func (r *Reservation) ToResponse() ReservationResponse {
	return ReservationResponse{
		ID:          r.ID,
		Status:      string(r.Status),
		WarehouseID: r.WarehouseID,
		ItemID:      r.ItemID,
		Quantity:    r.Quantity,
		ExpiresAt:   r.ExpiresAt.UTC().Format(time.RFC3339),
	}
}

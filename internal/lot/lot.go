// Package lot commissions lots and reads the lots a tenant owns or holds (data-model.md §3.2,
// shipment-lifecycle.md §2).
package lot

import (
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/veritrace-platform/core-business-service/internal/calendar"
	"github.com/veritrace-platform/core-business-service/internal/policy"
)

// Lot is a production batch of one product. Its owner is the tenant that commissioned it; tenants that hold or
// held stock of it see it too. It carries the product's GTIN, name, and temperature bounds as they were at
// commissioning, so holders need no access to the owner's catalog.
type Lot struct {
	ID                     uuid.UUID        `json:"id"`
	OwnerTenantID          uuid.UUID        `json:"owner_tenant_id"`
	ProductID              uuid.UUID        `json:"product_id"`
	GTIN                   string           `json:"gtin"`
	ProductName            string           `json:"product_name"`
	MinTempCelsius         float64          `json:"min_temp_celsius"`
	MaxTempCelsius         float64          `json:"max_temp_celsius"`
	LotNumber              string           `json:"lot_number"`
	ProductionDate         calendar.Date    `json:"production_date"`
	ExpirationDate         calendar.Date    `json:"expiration_date"`
	QuantityCommissioned   int              `json:"quantity_commissioned"`
	CommissionedLocationID uuid.UUID        `json:"commissioned_location_id"`
	Status                 policy.LotStatus `json:"status"`
	RecalledAt             *time.Time       `json:"recalled_at"`
	CreatedAt              time.Time        `json:"created_at"`
	UpdatedAt              time.Time        `json:"updated_at"`
}

// NewLot is a validated request to commission a lot.
type NewLot struct {
	ProductID      uuid.UUID
	LotNumber      string
	ProductionDate calendar.Date
	ExpirationDate calendar.Date
	Quantity       int
	LocationID     uuid.UUID
}

// Product holds the fields of a product that commissioning checks and copies.
type Product struct {
	ID             uuid.UUID
	GTIN           string
	Name           string
	MinTempCelsius float64
	MaxTempCelsius float64
	IsActive       bool
}

// Filter selects a page of visible lots, newest first.
type Filter struct {
	ProductID *uuid.UUID
	Status    *policy.LotStatus
	// After is the last lot of the previous page; uuid.Nil starts at the newest lot.
	After uuid.UUID
	Limit int
}

// Lot errors.
var (
	// ErrNotFound reports a lot that does not exist or that the tenant neither owns nor holds.
	ErrNotFound = errors.New("lot not found")
	// ErrNotOwned reports a product or location that does not exist or belongs to another tenant.
	ErrNotOwned = errors.New("not a product or location of the tenant")
	// ErrProductInactive reports a deactivated product, which takes no new lots.
	ErrProductInactive = errors.New("the product is inactive")
	// ErrLocationInactive reports a deactivated location, which takes no new lots.
	ErrLocationInactive = errors.New("the location is inactive")
	// ErrLotNumberTaken reports a lot number that the product already has.
	ErrLotNumberTaken = errors.New("lot number already used for the product")
)

// Package product manages a tenant's trade items, identified by GTIN-14, with the temperature range that the
// cold chain must hold and the Q10 of their shelf life (data-model.md §3.2; cold-chain-monitoring.md §8).
package product

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// Limits of a product's temperature range, in degrees Celsius, and their precision as stored in
// numeric(5, 2).
const (
	MinTemperatureCelsius = -50
	MaxTemperatureCelsius = 80
	TemperatureDecimals   = 2
)

// Limits and default of a product's shelf-life Q10, and its precision as stored in numeric(4, 2). The default
// matches the activation energy that mean kinetic temperature assumes (ADR-0024).
const (
	MinShelfLifeQ10      = 1
	MaxShelfLifeQ10      = 10
	DefaultShelfLifeQ10  = 3.0
	ShelfLifeQ10Decimals = 2
)

// Product is a trade item of one tenant. ShelfLifeQ10 is how many times faster its goods age for every 10 °C above
// MaxTempCelsius.
type Product struct {
	ID             uuid.UUID `json:"id"`
	GTIN           string    `json:"gtin"`
	Name           string    `json:"name"`
	Description    *string   `json:"description"`
	MinTempCelsius float64   `json:"min_temp_celsius"`
	MaxTempCelsius float64   `json:"max_temp_celsius"`
	ShelfLifeQ10   float64   `json:"shelf_life_q10"`
	IsActive       bool      `json:"is_active"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// NewProduct is a validated product to add to a tenant.
type NewProduct struct {
	GTIN           string
	Name           string
	Description    *string
	MinTempCelsius float64
	MaxTempCelsius float64
	ShelfLifeQ10   float64
}

// Filter selects a page of products, newest first.
type Filter struct {
	// Search matches part of the name, ignoring case, or part of the GTIN; empty matches every product.
	Search   string
	IsActive *bool
	// After is the last product of the previous page; uuid.Nil starts at the newest product.
	After uuid.UUID
	Limit int
}

// Patch lists the changes to a product; nil fields stay as they are. ClearDescription removes the
// description. The GTIN identifies the product and never changes.
type Patch struct {
	Name             *string
	Description      *string
	ClearDescription bool
	MinTempCelsius   *float64
	MaxTempCelsius   *float64
	ShelfLifeQ10     *float64
	IsActive         *bool
}

// Product errors.
var (
	// ErrNotFound reports a product that does not exist or belongs to another tenant.
	ErrNotFound = errors.New("product not found")
	// ErrGTINTaken reports a GTIN that another product, of any tenant, already has.
	ErrGTINTaken = errors.New("GTIN already registered")
	// ErrTemperatureRange reports a patch that leaves the minimum temperature at or above the maximum.
	ErrTemperatureRange = errors.New("the minimum temperature must be lower than the maximum")
)

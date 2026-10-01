// Package inventory keeps the stock ledger (data-model.md §3.2, shipment-lifecycle.md §1): one balance per
// location and lot, changed only by movements recorded in the same transaction.
package inventory

import (
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/veritrace-platform/core-business-service/internal/calendar"
	"github.com/veritrace-platform/core-business-service/internal/policy"
)

// Reason is why a balance changed.
type Reason string

// Reasons of movements.
const (
	ReasonCommissioned      Reason = "COMMISSIONED"
	ReasonShipmentCreated   Reason = "SHIPMENT_CREATED"
	ReasonShipmentCancelled Reason = "SHIPMENT_CANCELLED"
	ReasonShipmentDelivered Reason = "SHIPMENT_DELIVERED"
)

// Movement is one change of the stock of a lot at a location of the tenant.
type Movement struct {
	TenantID   uuid.UUID
	LocationID uuid.UUID
	LotID      uuid.UUID
	// Delta is positive when stock arrives and negative when it leaves; never zero.
	Delta  int
	Reason Reason
	// ShipmentID is set for every reason but ReasonCommissioned.
	ShipmentID *uuid.UUID
	CreatedBy  uuid.UUID
}

// ErrInsufficientStock reports a movement that would take a balance below zero.
var ErrInsufficientStock = errors.New("insufficient stock")

// LocationRef names the location of a balance.
type LocationRef struct {
	ID   uuid.UUID `json:"id"`
	GLN  string    `json:"gln"`
	Name string    `json:"name"`
}

// LotRef names the lot of a balance.
type LotRef struct {
	ID             uuid.UUID        `json:"id"`
	LotNumber      string           `json:"lot_number"`
	GTIN           string           `json:"gtin"`
	ProductName    string           `json:"product_name"`
	ExpirationDate calendar.Date    `json:"expiration_date"`
	Status         policy.LotStatus `json:"status"`
}

// Balance is the stock of one lot at one location of the tenant.
type Balance struct {
	Location       LocationRef `json:"location"`
	Lot            LotRef      `json:"lot"`
	QuantityOnHand int         `json:"quantity_on_hand"`
	UpdatedAt      time.Time   `json:"updated_at"`
}

// Key identifies a balance.
type Key struct {
	LocationID uuid.UUID
	LotID      uuid.UUID
}

// Filter selects a page of balances above zero, ordered by location and then lot, newest first.
type Filter struct {
	LocationID *uuid.UUID
	LotID      *uuid.UUID
	// After is the last balance of the previous page; nil starts at the first balance.
	After *Key
	Limit int
}

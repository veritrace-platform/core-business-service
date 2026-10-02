// Package shipment runs the shipment lifecycle (shipment-lifecycle.md §3–§7): creation from a lot's stock,
// carrier and driver assignment, the custody handover, and cancellation. Every state change appends an event to
// the shipment's hash-chained log in the same transaction. Shipments are visible to their participants only.
package shipment

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/veritrace-platform/core-business-service/internal/calendar"
	"github.com/veritrace-platform/core-business-service/internal/policy"
)

// Role is a tenant's part in a shipment.
type Role string

// Participant roles. One tenant may hold several, such as the owner that carries its own goods.
const (
	RoleOwner     Role = "OWNER"
	RoleCarrier   Role = "CARRIER"
	RoleConsignee Role = "CONSIGNEE"
	RoleInspector Role = "INSPECTOR"
)

// Participant is a tenant involved in a shipment, with its directory fields as they were when it joined.
type Participant struct {
	TenantID   uuid.UUID `json:"tenant_id"`
	Role       Role      `json:"role"`
	TenantCode string    `json:"tenant_code"`
	LegalName  string    `json:"legal_name"`
}

// LotRef is the shipped lot as it was when the shipment was created.
type LotRef struct {
	ID             uuid.UUID     `json:"id"`
	LotNumber      string        `json:"lot_number"`
	ExpirationDate calendar.Date `json:"expiration_date"`
}

// ProductRef is the shipped product as the lot recorded it.
type ProductRef struct {
	GTIN           string  `json:"gtin"`
	Name           string  `json:"name"`
	MinTempCelsius float64 `json:"min_temp_celsius"`
	MaxTempCelsius float64 `json:"max_temp_celsius"`
}

// Facility is the origin or destination of a shipment as it was when the shipment was created.
type Facility struct {
	LocationID           uuid.UUID `json:"location_id"`
	GLN                  string    `json:"gln"`
	Name                 string    `json:"name"`
	Latitude             float64   `json:"latitude"`
	Longitude            float64   `json:"longitude"`
	GeoFenceRadiusMeters int       `json:"geo_fence_radius_meters"`
}

// Shipment moves a quantity of one lot, as one logistic unit identified by its SSCC, from an origin to a
// destination.
type Shipment struct {
	ID                uuid.UUID             `json:"id"`
	SSCC              string                `json:"sscc"`
	Status            policy.ShipmentStatus `json:"status"`
	Quantity          int                   `json:"quantity"`
	Lot               LotRef                `json:"lot"`
	Product           ProductRef            `json:"product"`
	Origin            Facility              `json:"origin"`
	Destination       Facility              `json:"destination"`
	OwnerTenantID     uuid.UUID             `json:"owner_tenant_id"`
	CarrierTenantID   uuid.UUID             `json:"carrier_tenant_id"`
	ConsigneeTenantID uuid.UUID             `json:"consignee_tenant_id"`
	AssignedDriverID  *uuid.UUID            `json:"assigned_driver_id"`
	Participants      []Participant         `json:"participants"`
	PickedUpAt        *time.Time            `json:"picked_up_at"`
	DeliveredAt       *time.Time            `json:"delivered_at"`
	CancelledAt       *time.Time            `json:"cancelled_at"`
	RecalledAt        *time.Time            `json:"recalled_at"`
	CreatedAt         time.Time             `json:"created_at"`
	UpdatedAt         time.Time             `json:"updated_at"`
}

// parties returns the caller's parts in the shipment, which the policy checks.
func (s Shipment) parties(tenantID uuid.UUID) []policy.Party {
	var parties []policy.Party
	if s.OwnerTenantID == tenantID {
		parties = append(parties, policy.Owner)
	}
	if s.CarrierTenantID == tenantID {
		parties = append(parties, policy.Carrier)
	}
	if s.ConsigneeTenantID == tenantID {
		parties = append(parties, policy.Consignee)
	}
	for _, p := range s.Participants {
		if p.TenantID == tenantID && p.Role == RoleInspector {
			parties = append(parties, policy.Inspector)
		}
	}
	return parties
}

// assignedTo reports whether userID is the shipment's assigned driver.
func (s Shipment) assignedTo(userID uuid.UUID) bool {
	return s.AssignedDriverID != nil && *s.AssignedDriverID == userID
}

// NewShipment is a validated request to ship a quantity of a lot.
type NewShipment struct {
	LotID            uuid.UUID
	Quantity         int
	OriginLocationID uuid.UUID
	DestinationGLN   string
	// CarrierTenantCode names the carrier; empty means the owner carries the goods itself.
	CarrierTenantCode string
	// DriverUserID assigns one of the owner's drivers when the owner is the carrier.
	DriverUserID *uuid.UUID
}

// Filter selects a page of the caller's shipments, newest first.
type Filter struct {
	Status *policy.ShipmentStatus
	// Party keeps the shipments in which the caller's tenant has this role.
	Party *Role
	SSCC  string
	LotID *uuid.UUID
	// DriverID keeps the shipments assigned to this driver.
	DriverID *uuid.UUID
	// After is the last shipment of the previous page; uuid.Nil starts at the newest shipment.
	After uuid.UUID
	Limit int
}

// Summary counts the caller's shipments by status.
type Summary struct {
	Created   int `json:"created"`
	InTransit int `json:"in_transit"`
	Delivered int `json:"delivered"`
	Cancelled int `json:"cancelled"`
	Recalled  int `json:"recalled"`
}

// Shipment errors.
var (
	// ErrNotFound reports a shipment that does not exist or in which the caller's tenant takes no part.
	ErrNotFound = errors.New("shipment not found")
	// ErrInsufficientStock reports an origin balance below the quantity to ship.
	ErrInsufficientStock = errors.New("the origin does not hold enough stock of the lot")
	// ErrSerialSpaceExhausted reports that the tenant has issued every SSCC of its extension digit.
	ErrSerialSpaceExhausted = errors.New("the SSCC serial space of the extension digit is used up")
)

// InvalidReferenceError reports a request field that names something the command cannot use, such as a lot that
// the tenant does not hold or a destination that the directory does not list.
type InvalidReferenceError struct {
	Field   string
	Message string
}

func (e *InvalidReferenceError) Error() string {
	return fmt.Sprintf("%s %s", e.Field, e.Message)
}

func invalidReference(field, message string) error {
	return &InvalidReferenceError{Field: field, Message: message}
}

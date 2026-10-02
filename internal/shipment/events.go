package shipment

import (
	"github.com/google/uuid"

	"github.com/veritrace-platform/core-business-service/internal/calendar"
)

// Payloads of shipment events, as messaging.md §3 defines them.

type facilityRef struct {
	LocationID uuid.UUID `json:"location_id"`
	GLN        string    `json:"gln"`
	Name       string    `json:"name"`
}

type participantRef struct {
	TenantID uuid.UUID `json:"tenant_id"`
	Role     Role      `json:"role"`
}

type createdPayload struct {
	OwnerTenantID    uuid.UUID        `json:"owner_tenant_id"`
	LotID            uuid.UUID        `json:"lot_id"`
	GTIN             string           `json:"gtin"`
	ProductName      string           `json:"product_name"`
	LotNumber        string           `json:"lot_number"`
	ExpirationDate   calendar.Date    `json:"expiration_date"`
	Quantity         int              `json:"quantity"`
	MinTempCelsius   float64          `json:"min_temp_celsius"`
	MaxTempCelsius   float64          `json:"max_temp_celsius"`
	Origin           facilityRef      `json:"origin"`
	Destination      facilityRef      `json:"destination"`
	Participants     []participantRef `json:"participants"`
	AssignedDriverID *uuid.UUID       `json:"assigned_driver_id"`
}

func createdData(owner uuid.UUID, lot lotSnapshot, ns NewShipment, origin, destination Facility, participants []Participant) createdPayload {
	refs := make([]participantRef, len(participants))
	for i, p := range participants {
		refs[i] = participantRef{TenantID: p.TenantID, Role: p.Role}
	}
	return createdPayload{
		OwnerTenantID: owner, LotID: lot.ID, GTIN: lot.GTIN, ProductName: lot.ProductName, LotNumber: lot.LotNumber,
		ExpirationDate: lot.ExpirationDate, Quantity: ns.Quantity, MinTempCelsius: lot.MinTempCelsius,
		MaxTempCelsius: lot.MaxTempCelsius,
		Origin:         facilityRef{LocationID: origin.LocationID, GLN: origin.GLN, Name: origin.Name},
		Destination:    facilityRef{LocationID: destination.LocationID, GLN: destination.GLN, Name: destination.Name},
		Participants:   refs, AssignedDriverID: ns.DriverUserID,
	}
}

type participantData struct {
	TenantID uuid.UUID `json:"tenant_id"`
	Role     Role      `json:"role"`
}

type driverData struct {
	DriverUserID uuid.UUID `json:"driver_user_id"`
}

type cancelledData struct {
	Reason string `json:"reason"`
}

type pickupData struct {
	DriverUserID   uuid.UUID `json:"driver_user_id"`
	Position       Position  `json:"position"`
	DistanceMeters float64   `json:"distance_meters"`
}

type facilityName struct {
	GLN  string `json:"gln"`
	Name string `json:"name"`
}

type checkpointData struct {
	Facility       facilityName `json:"facility"`
	Position       Position     `json:"position"`
	DistanceMeters float64      `json:"distance_meters"`
}

type deliveryData struct {
	ReceiverUserID uuid.UUID `json:"receiver_user_id"`
	Position       Position  `json:"position"`
	DistanceMeters float64   `json:"distance_meters"`
}

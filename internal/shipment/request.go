package shipment

import (
	"math"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"github.com/veritrace-platform/core-business-service/internal/gs1"
	"github.com/veritrace-platform/core-business-service/internal/location"
	"github.com/veritrace-platform/core-business-service/internal/platform/httpx"
	"github.com/veritrace-platform/core-business-service/internal/rest"
)

const maxReasonLength = 1000

// createRequest is the body of POST /api/v1/shipments.
type createRequest struct {
	LotID             string  `json:"lot_id"`
	Quantity          *int    `json:"quantity"`
	OriginLocationID  string  `json:"origin_location_id"`
	DestinationGLN    string  `json:"destination_gln"`
	CarrierTenantCode *string `json:"carrier_tenant_code"`
	DriverUserID      *string `json:"driver_user_id"`
}

// validate checks the request, recording errors in v, and returns the shipment it describes; the result is
// meaningful only when v holds no errors.
func (req *createRequest) validate(v *rest.Validator) NewShipment {
	var ns NewShipment
	ns.LotID, _ = v.UUID("lot_id", strings.TrimSpace(req.LotID))
	if req.Quantity == nil {
		v.Add("quantity", httpx.FieldRequired, "is required")
	} else if v.Int("quantity", *req.Quantity, 1, math.MaxInt32) {
		ns.Quantity = *req.Quantity
	}
	ns.OriginLocationID, _ = v.UUID("origin_location_id", strings.TrimSpace(req.OriginLocationID))
	ns.DestinationGLN = gln(v, "destination_gln", req.DestinationGLN)
	if req.CarrierTenantCode != nil {
		ns.CarrierTenantCode = tenantCode(*req.CarrierTenantCode)
	}
	if req.DriverUserID != nil && strings.TrimSpace(*req.DriverUserID) != "" {
		if id, ok := v.UUID("driver_user_id", strings.TrimSpace(*req.DriverUserID)); ok {
			ns.DriverUserID = &id
		}
	}
	return ns
}

// gln reads a required GLN field.
func gln(v *rest.Validator, field, raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		v.Add(field, httpx.FieldRequired, "is required")
	} else {
		v.Key(field, gs1.Validate(gs1.GLN, value))
	}
	return value
}

// tenantCode normalizes a tenant code; codes are upper case, so lower-case input finds its tenant.
func tenantCode(raw string) string {
	return strings.ToUpper(strings.TrimSpace(raw))
}

// carrierRequest is the body of POST /api/v1/shipments/{shipment_id}/carrier.
type carrierRequest struct {
	CarrierTenantCode string `json:"carrier_tenant_code"`
}

func (req carrierRequest) validate(v *rest.Validator) string {
	code := tenantCode(req.CarrierTenantCode)
	if code == "" {
		v.Add("carrier_tenant_code", httpx.FieldRequired, "is required")
	}
	return code
}

// driverRequest is the body of POST /api/v1/shipments/{shipment_id}/driver.
type driverRequest struct {
	DriverUserID string `json:"driver_user_id"`
}

func (req driverRequest) validate(v *rest.Validator) uuid.UUID {
	id, _ := v.UUID("driver_user_id", strings.TrimSpace(req.DriverUserID))
	return id
}

// cancelRequest is the body of POST /api/v1/shipments/{shipment_id}/cancel.
type cancelRequest struct {
	Reason string `json:"reason"`
}

func (req *cancelRequest) validate(v *rest.Validator) string {
	v.Text("reason", &req.Reason, 1, maxReasonLength)
	return req.Reason
}

// maxAccuracyMeters bounds the reported accuracy of a position; the geo-fence counts at most 50 m of it.
const maxAccuracyMeters = 100_000

// positionRequest is a device position in a request body.
type positionRequest struct {
	Latitude       *float64 `json:"latitude"`
	Longitude      *float64 `json:"longitude"`
	AccuracyMeters *float64 `json:"accuracy_meters"`
}

// readPosition checks a required position, recording errors under position.*. Coordinates are rounded to six
// decimals and the accuracy to one, as events record them.
func readPosition(v *rest.Validator, req *positionRequest) Position {
	if req == nil {
		v.Add("position", httpx.FieldRequired, "is required")
		return Position{}
	}
	var p Position
	if v.Float("position.latitude", req.Latitude, -90, 90) {
		p.Latitude = location.RoundCoordinate(*req.Latitude)
	}
	if v.Float("position.longitude", req.Longitude, -180, 180) {
		p.Longitude = location.RoundCoordinate(*req.Longitude)
	}
	if v.Float("position.accuracy_meters", req.AccuracyMeters, 0, maxAccuracyMeters) {
		p.AccuracyMeters = math.Round(*req.AccuracyMeters*10) / 10
	}
	return p
}

// scannedSSCC reads a required SSCC. A malformed one answers INVALID_GS1_IDENTIFIER rather than a mismatch.
func scannedSSCC(v *rest.Validator, raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		v.Add("sscc", httpx.FieldRequired, "is required")
	} else {
		v.Key("sscc", gs1.ValidateSSCC(value))
	}
	return value
}

var pickupCodePattern = regexp.MustCompile(`^[0-9]{6}$`)

// pickupRequest is the body of POST /api/v1/shipments/{shipment_id}/pickup.
type pickupRequest struct {
	SSCC     string           `json:"sscc"`
	Code     string           `json:"code"`
	Position *positionRequest `json:"position"`
}

func (req *pickupRequest) validate(v *rest.Validator) Pickup {
	pickup := Pickup{SSCC: scannedSSCC(v, req.SSCC), Code: strings.TrimSpace(req.Code)}
	if pickup.Code == "" {
		v.Add("code", httpx.FieldRequired, "is required")
	} else {
		v.Matches("code", pickup.Code, pickupCodePattern, "must be 6 digits")
	}
	pickup.Position = readPosition(v, req.Position)
	return pickup
}

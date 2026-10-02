package shipment

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"math/big"
	"time"

	"github.com/google/uuid"

	"github.com/veritrace-platform/core-business-service/internal/event"
	"github.com/veritrace-platform/core-business-service/internal/identity"
	"github.com/veritrace-platform/core-business-service/internal/inventory"
	"github.com/veritrace-platform/core-business-service/internal/policy"
)

// Pickup code rules (shipment-lifecycle.md §6.1).
const (
	PickupCodeDigits   = 6
	PickupCodeTTL      = 15 * time.Minute
	PickupCodeAttempts = 5
)

// Geo-fence rule (shipment-lifecycle.md §6.4): a position counts as at a facility within its radius plus the
// device's accuracy, up to 50 m of it.
const (
	earthRadiusMeters    = 6_371_000
	maxAccuracyAllowance = 50
)

// Position is a device-reported WGS-84 position.
type Position struct {
	Latitude       float64 `json:"latitude"`
	Longitude      float64 `json:"longitude"`
	AccuracyMeters float64 `json:"accuracy_meters"`
}

// Pickup is what the assigned driver submits at the origin.
type Pickup struct {
	SSCC     string
	Code     string
	Position Position
}

// IssuedCode is a pickup code as issued: the only time its plain text exists.
type IssuedCode struct {
	Code            string    `json:"code"`
	ExpiresAt       time.Time `json:"expires_at"`
	AttemptsAllowed int       `json:"attempts_allowed"`
}

// pickupCode is the stored form of an active code.
type pickupCode struct {
	ID             uuid.UUID
	Hash           []byte
	ExpiresAt      time.Time
	FailedAttempts int
}

// ErrSSCCMismatch reports a scanned SSCC that is not the shipment's.
var ErrSSCCMismatch = errors.New("the scanned SSCC is not the shipment's")

// PickupCodeReason is why a pickup code was refused.
type PickupCodeReason string

// Pickup code refusals.
const (
	// PickupCodeInvalid is a wrong code; it consumed one attempt.
	PickupCodeInvalid PickupCodeReason = "INVALID"
	// PickupCodeExpired means that no code is active, or that the active one has expired.
	PickupCodeExpired PickupCodeReason = "EXPIRED"
	// PickupCodeLocked means that the code has no attempts left; the owner must issue a new one.
	PickupCodeLocked PickupCodeReason = "LOCKED"
)

// PickupCodeError reports a pickup code that does not release the shipment.
type PickupCodeError struct {
	Reason            PickupCodeReason
	RemainingAttempts int
}

func (e *PickupCodeError) Error() string {
	return fmt.Sprintf("pickup code refused: %s", e.Reason)
}

// OutsideGeofenceError reports a position too far from a facility, in meters.
type OutsideGeofenceError struct {
	DistanceMeters float64
	AllowedMeters  float64
}

func (e *OutsideGeofenceError) Error() string {
	return fmt.Sprintf("position %.1f m from the facility, %.1f m allowed", e.DistanceMeters, e.AllowedMeters)
}

// distanceMeters returns the great-circle distance between two positions by the haversine formula.
func distanceMeters(lat1, lon1, lat2, lon2 float64) float64 {
	rad := func(degrees float64) float64 { return degrees * math.Pi / 180 }
	dLat, dLon := rad(lat2-lat1), rad(lon2-lon1)
	h := math.Pow(math.Sin(dLat/2), 2) + math.Cos(rad(lat1))*math.Cos(rad(lat2))*math.Pow(math.Sin(dLon/2), 2)
	return 2 * earthRadiusMeters * math.Asin(math.Sqrt(h))
}

// reach measures a position against the facility's geo-fence. The distances are rounded to decimeters for
// events and problems; the decision uses the exact ones.
func (f Facility) reach(p Position) (distance, allowed float64, inside bool) {
	d := distanceMeters(p.Latitude, p.Longitude, f.Latitude, f.Longitude)
	a := float64(f.GeoFenceRadiusMeters) + math.Min(p.AccuracyMeters, maxAccuracyAllowance)
	return math.Round(d*10) / 10, math.Round(a*10) / 10, d <= a
}

// geofence returns the fact of a geo-fence check and records a failure in *failure.
func geofence(f Facility, p Position, distance *float64, failure *error) func() (bool, error) {
	return func() (bool, error) {
		d, allowed, inside := f.reach(p)
		*distance = d
		if !inside {
			*failure = &OutsideGeofenceError{DistanceMeters: d, AllowedMeters: allowed}
		}
		return inside, nil
	}
}

// ssccMatch returns the fact of an SSCC check and records a failure in *failure.
func ssccMatch(sh Shipment, scanned string, failure *error) func() (bool, error) {
	return func() (bool, error) {
		if scanned != sh.SSCC {
			*failure = ErrSSCCMismatch
			return false, nil
		}
		return true, nil
	}
}

// newPickupCode draws a code uniformly from the 10^6 six-digit codes.
func newPickupCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(math.Pow10(PickupCodeDigits))))
	if err != nil {
		return "", fmt.Errorf("draw pickup code: %w", err)
	}
	return fmt.Sprintf("%0*d", PickupCodeDigits, n.Int64()), nil
}

// hashPickupCode returns HMAC-SHA256(pepper, shipment ID ‖ code), the shipment ID in its 16 bytes.
func (s *Service) hashPickupCode(shipmentID uuid.UUID, code string) []byte {
	mac := hmac.New(sha256.New, s.pepper)
	mac.Write(shipmentID[:])
	mac.Write([]byte(code))
	return mac.Sum(nil)
}

// IssuePickupCode issues a code for the assigned driver to collect the shipment with, and invalidates the code
// before it (shipment-lifecycle.md §6.1). Only its HMAC is stored.
func (s *Service) IssuePickupCode(ctx context.Context, p identity.Principal, id uuid.UUID) (IssuedCode, error) {
	var issued IssuedCode
	_, err := s.command(ctx, p, id, func(repo Repository, sh Shipment) error {
		err := policy.Evaluate(policy.Request{
			Action: policy.IssuePickupCode, Role: p.Role, Parties: sh.parties(p.TenantID), ShipmentStatus: sh.Status,
			Facts: func(policy.Check) (bool, error) {
				// The only check of this action: a driver is assigned.
				return sh.AssignedDriverID != nil, nil
			},
		})
		if err != nil {
			return err
		}
		code, err := newPickupCode()
		if err != nil {
			return err
		}
		now := s.now().UTC()
		if err := repo.InvalidatePickupCodes(ctx, id, now); err != nil {
			return err
		}
		issued = IssuedCode{Code: code, ExpiresAt: now.Add(PickupCodeTTL), AttemptsAllowed: PickupCodeAttempts}
		return repo.InsertPickupCode(ctx, id, s.hashPickupCode(id, code), issued.ExpiresAt, p.UserID, now)
	})
	return issued, err
}

// ConfirmPickup hands the shipment to its assigned driver (shipment-lifecycle.md §6.1). The checks run in order:
// not recalled, CREATED, the assigned driver, the SSCC, an active and unlocked code, the code itself, which
// consumes an attempt when wrong, and the origin geo-fence. On success the code is consumed, the shipment is
// IN_TRANSIT, and shipment.pickup_confirmed records the position.
func (s *Service) ConfirmPickup(ctx context.Context, p identity.Principal, id uuid.UUID, pickup Pickup) (Shipment, error) {
	var failure error
	sh, err := s.command(ctx, p, id, func(repo Repository, sh Shipment) error {
		now := s.now().UTC()
		var code pickupCode
		var distance float64
		err := policy.Evaluate(policy.Request{
			Action: policy.ConfirmPickup, Role: p.Role, Parties: sh.parties(p.TenantID), ShipmentStatus: sh.Status,
			AssignedDriver: sh.assignedTo(p.UserID),
			Facts: known(map[policy.Check]func() (bool, error){
				policy.SSCCMatch: ssccMatch(sh, pickup.SSCC, &failure),
				policy.PickupCodeActive: func() (bool, error) {
					var err error
					code, err = repo.LockPickupCode(ctx, id)
					switch {
					case errors.Is(err, errNotVisible):
						failure = &PickupCodeError{Reason: PickupCodeExpired}
					case err != nil:
						return false, err
					case code.FailedAttempts >= PickupCodeAttempts:
						failure = &PickupCodeError{Reason: PickupCodeLocked}
					case !now.Before(code.ExpiresAt):
						failure = &PickupCodeError{Reason: PickupCodeExpired}
					default:
						return true, nil
					}
					return false, nil
				},
				policy.PickupCodeMatch: func() (bool, error) {
					if hmac.Equal(code.Hash, s.hashPickupCode(id, pickup.Code)) {
						return true, nil
					}
					attempts, err := repo.RecordFailedAttempt(ctx, code.ID)
					if err != nil {
						return false, err
					}
					failure = &PickupCodeError{Reason: PickupCodeInvalid, RemainingAttempts: max(PickupCodeAttempts-attempts, 0)}
					return false, nil
				},
				policy.OriginGeofence: geofence(sh.Origin, pickup.Position, &distance, &failure),
			}),
		})
		if failure != nil {
			// Commit, so that a wrong code keeps its spent attempt; the failure is reported after the commit.
			return nil //nolint:nilerr // the denial is replaced by failure, which carries its details
		}
		if err != nil {
			return err
		}
		if err := repo.ConsumePickupCode(ctx, code.ID, now); err != nil {
			return err
		}
		if err := repo.SetPickedUp(ctx, id, now); err != nil {
			return err
		}
		return repo.AppendEvent(ctx, event.New{
			ShipmentID: id, SSCC: sh.SSCC, Status: policy.ShipmentInTransit, Type: event.TypePickupConfirmed,
			Actor: actor(p), OccurredAt: now,
			Data: pickupData{DriverUserID: p.UserID, Position: pickup.Position, DistanceMeters: distance},
		})
	})
	if err == nil && failure != nil {
		return Shipment{}, failure
	}
	return sh, err
}

// Checkpoint is what the assigned driver submits at an intermediate facility.
type Checkpoint struct {
	SSCC string
	// GLN names the facility, an active location of any tenant.
	GLN      string
	Position Position
}

// RecordCheckpoint records that the assigned driver passed a facility with the shipment
// (shipment-lifecycle.md §6.2): the scanned SSCC must match and the position must be inside the facility's
// geo-fence. The status stays IN_TRANSIT; shipment.checkpoint_recorded records the facility and the position.
func (s *Service) RecordCheckpoint(ctx context.Context, p identity.Principal, id uuid.UUID, cp Checkpoint) (Shipment, error) {
	var failure error
	sh, err := s.command(ctx, p, id, func(repo Repository, sh Shipment) error {
		var facility Facility
		var distance float64
		err := policy.Evaluate(policy.Request{
			Action: policy.RecordCheckpoint, Role: p.Role, Parties: sh.parties(p.TenantID), ShipmentStatus: sh.Status,
			AssignedDriver: sh.assignedTo(p.UserID),
			Facts: known(map[policy.Check]func() (bool, error){
				policy.SSCCMatch: ssccMatch(sh, cp.SSCC, &failure),
				policy.FacilityGeofence: func() (bool, error) {
					var err error
					facility, _, err = repo.LookUpLocation(ctx, cp.GLN)
					if errors.Is(err, errNotVisible) {
						failure = invalidReference("gln", "is not an active location in the directory")
						return false, nil
					}
					if err != nil {
						return false, err
					}
					return geofence(facility, cp.Position, &distance, &failure)()
				},
			}),
		})
		if failure != nil {
			return failure
		}
		if err != nil {
			return err
		}
		return repo.AppendEvent(ctx, event.New{
			ShipmentID: id, SSCC: sh.SSCC, Status: sh.Status, Type: event.TypeCheckpointRecorded, Actor: actor(p),
			OccurredAt: s.now(),
			Data: checkpointData{
				Facility: facilityName{GLN: facility.GLN, Name: facility.Name}, Position: cp.Position, DistanceMeters: distance,
			},
		})
	})
	return sh, err
}

// Delivery is what the consignee submits when the shipment arrives.
type Delivery struct {
	SSCC     string
	Position Position
}

// ConfirmDelivery records the consignee's receipt at the destination (shipment-lifecycle.md §6.3): the scanned
// SSCC must match and the position must be inside the destination geo-fence. The shipment is DELIVERED, the
// quantity lands on the consignee's balance at the destination, and shipment.delivery_confirmed records the
// position. The consignee then holds the lot.
func (s *Service) ConfirmDelivery(ctx context.Context, p identity.Principal, id uuid.UUID, d Delivery) (Shipment, error) {
	var failure error
	sh, err := s.command(ctx, p, id, func(repo Repository, sh Shipment) error {
		var distance float64
		err := policy.Evaluate(policy.Request{
			Action: policy.ConfirmDelivery, Role: p.Role, Parties: sh.parties(p.TenantID), ShipmentStatus: sh.Status,
			Facts: known(map[policy.Check]func() (bool, error){
				policy.SSCCMatch:           ssccMatch(sh, d.SSCC, &failure),
				policy.DestinationGeofence: geofence(sh.Destination, d.Position, &distance, &failure),
			}),
		})
		if failure != nil {
			return failure
		}
		if err != nil {
			return err
		}
		now := s.now().UTC()
		if err := repo.SetDelivered(ctx, id, now); err != nil {
			return err
		}
		err = repo.ApplyMovement(ctx, inventory.Movement{
			TenantID: p.TenantID, LocationID: sh.Destination.LocationID, LotID: sh.Lot.ID, Delta: sh.Quantity,
			Reason: inventory.ReasonShipmentDelivered, ShipmentID: &id, CreatedBy: p.UserID,
		})
		if err != nil {
			return err
		}
		return repo.AppendEvent(ctx, event.New{
			ShipmentID: id, SSCC: sh.SSCC, Status: policy.ShipmentDelivered, Type: event.TypeDeliveryConfirmed,
			Actor: actor(p), OccurredAt: now,
			Data: deliveryData{ReceiverUserID: p.UserID, Position: d.Position, DistanceMeters: distance},
		})
	})
	return sh, err
}

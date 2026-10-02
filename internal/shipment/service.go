package shipment

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/veritrace-platform/core-business-service/internal/calendar"
	"github.com/veritrace-platform/core-business-service/internal/event"
	"github.com/veritrace-platform/core-business-service/internal/identity"
	"github.com/veritrace-platform/core-business-service/internal/inventory"
	"github.com/veritrace-platform/core-business-service/internal/policy"
)

// Store opens tenant transactions.
type Store interface {
	WithTenantTx(ctx context.Context, tenantID uuid.UUID, fn func(Repository) error) error
}

// Repository reads and changes shipments inside one tenant transaction. Methods that look something up return
// errNotVisible when it does not exist for the tenant.
type Repository interface {
	// LockLot reads a lot that the tenant owns or holds, after taking the lot's lock in shared mode, which a
	// recall of the lot takes exclusively.
	LockLot(ctx context.Context, lotID uuid.UUID) (lotSnapshot, error)
	// Balance returns the tenant's stock of a lot at a location, 0 when it has none.
	Balance(ctx context.Context, locationID, lotID uuid.UUID) (int, error)
	// LockLocation reads one of the tenant's locations and keeps it from changing until the transaction ends.
	LockLocation(ctx context.Context, id uuid.UUID) (Facility, bool, error)
	// LookUpLocation resolves the GLN of an active location of any active tenant.
	LookUpLocation(ctx context.Context, gln string) (Facility, tenantRef, error)
	// LookUpTenant resolves the code of an active tenant.
	LookUpTenant(ctx context.Context, code string) (tenantRef, error)
	OwnTenant(ctx context.Context, tenantID uuid.UUID) (tenantRef, error)
	// IssueSSCC takes the tenant's next serial reference; it returns ErrSerialSpaceExhausted when none is left.
	IssueSSCC(ctx context.Context, tenantID uuid.UUID) (string, error)
	// ActiveDriver reports whether the user is an active DRIVER of the tenant.
	ActiveDriver(ctx context.Context, userID uuid.UUID) (bool, error)
	Insert(ctx context.Context, s newRow) (uuid.UUID, error)
	AddParticipant(ctx context.Context, shipmentID uuid.UUID, p Participant, at time.Time) error
	RemoveParticipant(ctx context.Context, shipmentID, tenantID uuid.UUID, role Role) error
	// ApplyMovement returns ErrInsufficientStock for a movement that would take a balance below zero.
	ApplyMovement(ctx context.Context, m inventory.Movement) error
	// AppendEvent must run while the transaction holds the shipment's row lock.
	AppendEvent(ctx context.Context, e event.New) error
	Get(ctx context.Context, id uuid.UUID) (Shipment, error)
	// Lock reads a shipment and holds its row lock until the transaction ends, which serializes its commands.
	Lock(ctx context.Context, id uuid.UUID) (Shipment, error)
	List(ctx context.Context, f Filter) ([]Shipment, error)
	Summary(ctx context.Context, driverID *uuid.UUID) (Summary, error)
	SetCarrier(ctx context.Context, id, carrierTenantID uuid.UUID) error
	SetDriver(ctx context.Context, id, driverID uuid.UUID) error
	SetCancelled(ctx context.Context, id uuid.UUID, at time.Time) error
	SetPickedUp(ctx context.Context, id uuid.UUID, at time.Time) error
	// InvalidatePickupCodes ends the shipment's active pickup code, if any.
	InvalidatePickupCodes(ctx context.Context, id uuid.UUID, at time.Time) error
	InsertPickupCode(ctx context.Context, id uuid.UUID, hash []byte, expiresAt time.Time, issuedBy uuid.UUID, at time.Time) error
	// LockPickupCode reads the shipment's active pickup code and locks it; errNotVisible means none is active.
	LockPickupCode(ctx context.Context, id uuid.UUID) (pickupCode, error)
	// RecordFailedAttempt returns the number of failed attempts after this one.
	RecordFailedAttempt(ctx context.Context, codeID uuid.UUID) (int, error)
	ConsumePickupCode(ctx context.Context, codeID uuid.UUID, at time.Time) error
	Events(ctx context.Context, id uuid.UUID, page event.Page) ([]event.Event, error)
	AllEvents(ctx context.Context, id uuid.UUID) ([]event.Event, error)
}

// errNotVisible reports a lot, location, or tenant that a command cannot see.
var errNotVisible = errors.New("not visible")

// lotSnapshot holds what a shipment copies from its lot, and the lot's status.
type lotSnapshot struct {
	ID             uuid.UUID
	GTIN           string
	ProductName    string
	MinTempCelsius float64
	MaxTempCelsius float64
	LotNumber      string
	ExpirationDate calendar.Date
	Status         policy.LotStatus
}

// tenantRef holds the directory fields of a tenant.
type tenantRef struct {
	ID        uuid.UUID
	Code      string
	LegalName string
}

// newRow is a shipment to insert.
type newRow struct {
	OwnerTenantID     uuid.UUID
	SSCC              string
	Lot               lotSnapshot
	Quantity          int
	Origin            Facility
	Destination       Facility
	ConsigneeTenantID uuid.UUID
	CarrierTenantID   uuid.UUID
	AssignedDriverID  *uuid.UUID
	CreatedBy         uuid.UUID
	CreatedAt         time.Time
}

// Service runs shipment commands and reads.
type Service struct {
	store Store
	// pepper keys the HMAC of pickup codes (PICKUP_CODE_PEPPER).
	pepper []byte
	now    func() time.Time
}

// NewService returns a Service.
func NewService(store Store, pepper []byte, now func() time.Time) *Service {
	return &Service{store: store, pepper: pepper, now: now}
}

// participantParties are every party a caller can have in a shipment. Row-level security lists only shipments in
// which the caller's tenant takes part, so a list query names all of them.
var participantParties = []policy.Party{policy.Owner, policy.Carrier, policy.Consignee, policy.Inspector}

// actor names the caller in events.
func actor(p identity.Principal) *event.Actor {
	return &event.Actor{TenantID: p.TenantID, UserID: p.UserID}
}

// Create ships a quantity of a lot from one of the caller's locations to a location of any tenant
// (shipment-lifecycle.md §4). In one transaction it issues an SSCC, records the snapshots and participants,
// allocates the quantity from the origin balance, and appends shipment.created.
func (s *Service) Create(ctx context.Context, p identity.Principal, ns NewShipment) (Shipment, error) {
	var created Shipment
	err := s.store.WithTenantTx(ctx, p.TenantID, func(repo Repository) error {
		lot, err := repo.LockLot(ctx, ns.LotID)
		if errors.Is(err, errNotVisible) {
			return invalidReference("lot_id", "is not a lot that your tenant holds")
		}
		if err != nil {
			return err
		}
		var origin Facility
		var originFound, originActive bool
		establish := map[policy.Check]func() (bool, error){
			// An inactive location takes no new shipments, so it cannot be the origin either.
			policy.OriginOwned: func() (bool, error) {
				var err error
				origin, originActive, err = repo.LockLocation(ctx, ns.OriginLocationID)
				if errors.Is(err, errNotVisible) {
					return false, nil
				}
				originFound = err == nil
				return originFound && originActive, err
			},
			policy.SufficientBalance: func() (bool, error) {
				available, err := repo.Balance(ctx, ns.OriginLocationID, lot.ID)
				return err == nil && available >= ns.Quantity, err
			},
		}
		err = policy.Evaluate(policy.Request{
			Action: policy.CreateShipment, Role: p.Role, Parties: []policy.Party{policy.LotHolder}, LotStatus: lot.Status,
			Facts: known(establish),
		})
		if err := createFailure(err, originFound); err != nil {
			return err
		}

		destination, consignee, err := repo.LookUpLocation(ctx, ns.DestinationGLN)
		if errors.Is(err, errNotVisible) {
			return invalidReference("destination_gln", "is not an active location in the directory")
		}
		if err != nil {
			return err
		}
		if destination.LocationID == origin.LocationID {
			return invalidReference("destination_gln", "must differ from the origin")
		}
		owner, err := repo.OwnTenant(ctx, p.TenantID)
		if err != nil {
			return err
		}
		carrier := owner
		if ns.CarrierTenantCode != "" {
			if carrier, err = repo.LookUpTenant(ctx, ns.CarrierTenantCode); errors.Is(err, errNotVisible) {
				return invalidReference("carrier_tenant_code", "is not an active tenant")
			} else if err != nil {
				return err
			}
		}
		if ns.DriverUserID != nil {
			if carrier.ID != owner.ID {
				return invalidReference("driver_user_id", "can be set only when your tenant carries the shipment")
			}
			if err := s.authorizeDriver(ctx, repo, p, []policy.Party{policy.Carrier}, policy.ShipmentCreated, *ns.DriverUserID); err != nil {
				return err
			}
		}

		sscc, err := repo.IssueSSCC(ctx, p.TenantID)
		if err != nil {
			return err
		}
		now := s.now().UTC()
		id, err := repo.Insert(ctx, newRow{
			OwnerTenantID: p.TenantID, SSCC: sscc, Lot: lot, Quantity: ns.Quantity, Origin: origin, Destination: destination,
			ConsigneeTenantID: consignee.ID, CarrierTenantID: carrier.ID, AssignedDriverID: ns.DriverUserID,
			CreatedBy: p.UserID, CreatedAt: now,
		})
		if err != nil {
			return err
		}
		participants := []Participant{
			{TenantID: owner.ID, Role: RoleOwner, TenantCode: owner.Code, LegalName: owner.LegalName},
			{TenantID: carrier.ID, Role: RoleCarrier, TenantCode: carrier.Code, LegalName: carrier.LegalName},
			{TenantID: consignee.ID, Role: RoleConsignee, TenantCode: consignee.Code, LegalName: consignee.LegalName},
		}
		for _, participant := range participants {
			if err := repo.AddParticipant(ctx, id, participant, now); err != nil {
				return err
			}
		}
		err = repo.ApplyMovement(ctx, inventory.Movement{
			TenantID: p.TenantID, LocationID: origin.LocationID, LotID: lot.ID, Delta: -ns.Quantity,
			Reason: inventory.ReasonShipmentCreated, ShipmentID: &id, CreatedBy: p.UserID,
		})
		if err != nil {
			return err
		}
		err = repo.AppendEvent(ctx, event.New{
			ShipmentID: id, SSCC: sscc, Status: policy.ShipmentCreated, Type: event.TypeCreated, Actor: actor(p),
			OccurredAt: now, Data: createdData(p.TenantID, lot, ns, origin, destination, participants),
		})
		if err != nil {
			return err
		}
		created, err = repo.Get(ctx, id)
		return err
	})
	return created, err
}

// createFailure turns the failed checks of shipment creation into the errors that the API reports.
// originFound reports that the origin is the tenant's, so that a failed ownership check means it is inactive.
func createFailure(err error, originFound bool) error {
	var denial *policy.DenialError
	if errors.As(err, &denial) && denial.Reason == policy.ReasonCheck {
		switch {
		case denial.Check == policy.OriginOwned && originFound:
			return invalidReference("origin_location_id", "is inactive")
		case denial.Check == policy.OriginOwned:
			return invalidReference("origin_location_id", "is not a location of your tenant")
		case denial.Check == policy.SufficientBalance:
			return ErrInsufficientStock
		}
	}
	return err
}

// known returns policy facts established by the functions; a check without one fails.
func known(establish map[policy.Check]func() (bool, error)) policy.Facts {
	return func(c policy.Check) (bool, error) {
		if check, ok := establish[c]; ok {
			return check()
		}
		return false, nil
	}
}

// authorizeDriver checks that the caller, a manager of the carrier, may assign the driver: an active DRIVER of its
// own tenant.
func (s *Service) authorizeDriver(ctx context.Context, repo Repository, p identity.Principal, parties []policy.Party,
	status policy.ShipmentStatus, driverID uuid.UUID,
) error {
	err := policy.Evaluate(policy.Request{
		Action: policy.AssignDriver, Role: p.Role, Parties: parties, ShipmentStatus: status,
		Facts: func(policy.Check) (bool, error) {
			// The only check of this action is the driver's eligibility.
			return repo.ActiveDriver(ctx, driverID)
		},
	})
	var denial *policy.DenialError
	if errors.As(err, &denial) && denial.Check == policy.DriverEligible {
		return invalidReference("driver_user_id", "is not an active driver of your tenant")
	}
	return err
}

// AssignCarrier hands a shipment that the owner was going to carry itself to another tenant
// (shipment-lifecycle.md §5). The owner's driver, if any, is released.
func (s *Service) AssignCarrier(ctx context.Context, p identity.Principal, id uuid.UUID, carrierCode string) (Shipment, error) {
	return s.command(ctx, p, id, func(repo Repository, sh Shipment) error {
		err := policy.Evaluate(policy.Request{
			Action: policy.AssignCarrier, Role: p.Role, Parties: sh.parties(p.TenantID), ShipmentStatus: sh.Status,
			Facts: func(policy.Check) (bool, error) {
				// The only check of this action: no carrier other than the owner yet.
				return sh.CarrierTenantID == sh.OwnerTenantID, nil
			},
		})
		if err != nil {
			return err
		}
		carrier, err := repo.LookUpTenant(ctx, carrierCode)
		if errors.Is(err, errNotVisible) {
			return invalidReference("carrier_tenant_code", "is not an active tenant")
		}
		if err != nil {
			return err
		}
		if carrier.ID == sh.OwnerTenantID {
			return invalidReference("carrier_tenant_code", "must name another tenant; yours already carries the shipment")
		}
		now := s.now().UTC()
		if err := repo.SetCarrier(ctx, id, carrier.ID); err != nil {
			return err
		}
		// A code issued for the released driver must not work for anyone.
		if err := repo.InvalidatePickupCodes(ctx, id, now); err != nil {
			return err
		}
		if err := repo.RemoveParticipant(ctx, id, sh.OwnerTenantID, RoleCarrier); err != nil {
			return err
		}
		err = repo.AddParticipant(ctx, id, Participant{
			TenantID: carrier.ID, Role: RoleCarrier, TenantCode: carrier.Code, LegalName: carrier.LegalName,
		}, now)
		if err != nil {
			return err
		}
		return repo.AppendEvent(ctx, event.New{
			ShipmentID: id, SSCC: sh.SSCC, Status: sh.Status, Type: event.TypeParticipantAdded, Actor: actor(p),
			OccurredAt: now, Data: participantData{TenantID: carrier.ID, Role: RoleCarrier},
		})
	})
}

// AssignDriver assigns one of the carrier's drivers, or replaces the one assigned (shipment-lifecycle.md §5).
// Assigning the driver already assigned changes nothing.
func (s *Service) AssignDriver(ctx context.Context, p identity.Principal, id, driverID uuid.UUID) (Shipment, error) {
	return s.command(ctx, p, id, func(repo Repository, sh Shipment) error {
		if err := s.authorizeDriver(ctx, repo, p, sh.parties(p.TenantID), sh.Status, driverID); err != nil {
			return err
		}
		if sh.assignedTo(driverID) {
			return nil
		}
		now := s.now().UTC()
		if err := repo.SetDriver(ctx, id, driverID); err != nil {
			return err
		}
		// A code issued for the previous driver must not work for the new one.
		if err := repo.InvalidatePickupCodes(ctx, id, now); err != nil {
			return err
		}
		return repo.AppendEvent(ctx, event.New{
			ShipmentID: id, SSCC: sh.SSCC, Status: sh.Status, Type: event.TypeDriverAssigned, Actor: actor(p),
			OccurredAt: now, Data: driverData{DriverUserID: driverID},
		})
	})
}

// Cancel cancels a shipment that has not been picked up and returns its quantity to the origin balance
// (shipment-lifecycle.md §7). The SSCC is never reused.
func (s *Service) Cancel(ctx context.Context, p identity.Principal, id uuid.UUID, reason string) (Shipment, error) {
	return s.command(ctx, p, id, func(repo Repository, sh Shipment) error {
		err := policy.Evaluate(policy.Request{
			Action: policy.CancelShipment, Role: p.Role, Parties: sh.parties(p.TenantID), ShipmentStatus: sh.Status,
		})
		if err != nil {
			return err
		}
		now := s.now().UTC()
		if err := repo.SetCancelled(ctx, id, now); err != nil {
			return err
		}
		if err := repo.InvalidatePickupCodes(ctx, id, now); err != nil {
			return err
		}
		err = repo.ApplyMovement(ctx, inventory.Movement{
			TenantID: p.TenantID, LocationID: sh.Origin.LocationID, LotID: sh.Lot.ID, Delta: sh.Quantity,
			Reason: inventory.ReasonShipmentCancelled, ShipmentID: &id, CreatedBy: p.UserID,
		})
		if err != nil {
			return err
		}
		return repo.AppendEvent(ctx, event.New{
			ShipmentID: id, SSCC: sh.SSCC, Status: policy.ShipmentCancelled, Type: event.TypeCancelled, Actor: actor(p),
			OccurredAt: now, Data: cancelledData{Reason: reason},
		})
	})
}

// command runs fn on a locked shipment in one transaction of the caller and returns the shipment as fn left it.
func (s *Service) command(ctx context.Context, p identity.Principal, id uuid.UUID, fn func(Repository, Shipment) error) (Shipment, error) {
	var result Shipment
	err := s.store.WithTenantTx(ctx, p.TenantID, func(repo Repository) error {
		sh, err := repo.Lock(ctx, id)
		if err != nil {
			return err
		}
		if err := fn(repo, sh); err != nil {
			return err
		}
		result, err = repo.Get(ctx, id)
		return err
	})
	return result, err
}

// authorizeView checks that the caller may read a shipment, its events, and its integrity.
func authorizeView(p identity.Principal, sh Shipment) error {
	return policy.Evaluate(policy.Request{
		Action: policy.ViewShipment, Role: p.Role, Parties: sh.parties(p.TenantID), ShipmentStatus: sh.Status,
		AssignedDriver: sh.assignedTo(p.UserID),
	})
}

// Get returns a shipment in which the caller's tenant takes part. Drivers read only the shipments assigned to them.
func (s *Service) Get(ctx context.Context, p identity.Principal, id uuid.UUID) (Shipment, error) {
	var sh Shipment
	err := s.store.WithTenantTx(ctx, p.TenantID, func(repo Repository) error {
		var err error
		if sh, err = repo.Get(ctx, id); err != nil {
			return err
		}
		return authorizeView(p, sh)
	})
	return sh, err
}

// authorizeList checks that the caller may list shipments and limits drivers to their assignments.
func authorizeList(p identity.Principal, driverID **uuid.UUID) error {
	if p.Role == identity.RoleDriver {
		*driverID = &p.UserID
	}
	return policy.Evaluate(policy.Request{
		Action: policy.ViewShipment, Role: p.Role, Parties: participantParties, AssignedDriver: *driverID != nil,
	})
}

// List returns a page of the shipments in which the caller's tenant takes part.
func (s *Service) List(ctx context.Context, p identity.Principal, f Filter) ([]Shipment, error) {
	if err := authorizeList(p, &f.DriverID); err != nil {
		return nil, err
	}
	var shipments []Shipment
	err := s.store.WithTenantTx(ctx, p.TenantID, func(repo Repository) error {
		var err error
		shipments, err = repo.List(ctx, f)
		return err
	})
	return shipments, err
}

// Summary counts the caller's shipments by status, as List would show them.
func (s *Service) Summary(ctx context.Context, p identity.Principal) (Summary, error) {
	var driverID *uuid.UUID
	if err := authorizeList(p, &driverID); err != nil {
		return Summary{}, err
	}
	var summary Summary
	err := s.store.WithTenantTx(ctx, p.TenantID, func(repo Repository) error {
		var err error
		summary, err = repo.Summary(ctx, driverID)
		return err
	})
	return summary, err
}

// Events returns a page of a shipment's event log, oldest first.
func (s *Service) Events(ctx context.Context, p identity.Principal, id uuid.UUID, page event.Page) ([]event.Event, error) {
	var events []event.Event
	err := s.store.WithTenantTx(ctx, p.TenantID, func(repo Repository) error {
		sh, err := repo.Get(ctx, id)
		if err != nil {
			return err
		}
		if err := authorizeView(p, sh); err != nil {
			return err
		}
		events, err = repo.Events(ctx, id, page)
		return err
	})
	return events, err
}

// Integrity recomputes a shipment's hash chain.
func (s *Service) Integrity(ctx context.Context, p identity.Principal, id uuid.UUID) (event.Integrity, error) {
	var result event.Integrity
	err := s.store.WithTenantTx(ctx, p.TenantID, func(repo Repository) error {
		sh, err := repo.Get(ctx, id)
		if err != nil {
			return err
		}
		if err := authorizeView(p, sh); err != nil {
			return err
		}
		events, err := repo.AllEvents(ctx, id)
		result = event.Verify(events)
		return err
	})
	return result, err
}

package shipment

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/veritrace-platform/core-business-service/internal/event"
	"github.com/veritrace-platform/core-business-service/internal/gs1"
	"github.com/veritrace-platform/core-business-service/internal/identity"
	"github.com/veritrace-platform/core-business-service/internal/inventory"
	"github.com/veritrace-platform/core-business-service/internal/policy"
	"github.com/veritrace-platform/core-business-service/internal/shipment/queries"
	"github.com/veritrace-platform/core-business-service/internal/tenancy"
)

// PostgresStore keeps shipments in PostgreSQL.
type PostgresStore struct {
	db *tenancy.DB
}

// NewPostgresStore returns a store that runs tenant-scoped work through db.
func NewPostgresStore(db *tenancy.DB) *PostgresStore {
	return &PostgresStore{db: db}
}

// WithTenantTx runs fn with a repository bound to one transaction of tenantID.
func (s *PostgresStore) WithTenantTx(ctx context.Context, tenantID uuid.UUID, fn func(Repository) error) error {
	return s.db.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return fn(repository{tx: tx, q: queries.New(tx)})
	})
}

type repository struct {
	tx pgx.Tx
	q  *queries.Queries
}

func (r repository) LockLot(ctx context.Context, lotID uuid.UUID) (lotSnapshot, error) {
	if err := r.q.LockLotForShipment(ctx, lotID); err != nil {
		return lotSnapshot{}, fmt.Errorf("lock lot: %w", err)
	}
	row, err := r.q.GetLot(ctx, lotID)
	if errors.Is(err, pgx.ErrNoRows) {
		return lotSnapshot{}, errNotVisible
	}
	if err != nil {
		return lotSnapshot{}, fmt.Errorf("read lot: %w", err)
	}
	return lotSnapshot{
		ID: row.ID, GTIN: row.Gtin, ProductName: row.ProductName, MinTempCelsius: row.MinTempCelsius,
		MaxTempCelsius: row.MaxTempCelsius, LotNumber: row.LotNumber, ExpirationDate: row.ExpirationDate,
		Status: policy.LotStatus(row.Status),
	}, nil
}

func (r repository) Balance(ctx context.Context, locationID, lotID uuid.UUID) (int, error) {
	balance, err := r.q.GetBalance(ctx, queries.GetBalanceParams{LocationID: locationID, LotID: lotID})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read balance: %w", err)
	}
	return int(balance), nil
}

func (r repository) LockLocation(ctx context.Context, id uuid.UUID) (Facility, bool, error) {
	row, err := r.q.LockLocation(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Facility{}, false, errNotVisible
	}
	if err != nil {
		return Facility{}, false, fmt.Errorf("lock location: %w", err)
	}
	return Facility{
		LocationID: row.ID, GLN: row.Gln, Name: row.Name, Latitude: row.Latitude, Longitude: row.Longitude,
		GeoFenceRadiusMeters: int(row.GeoFenceRadiusMeters),
	}, row.IsActive, nil
}

func (r repository) LookUpLocation(ctx context.Context, gln string) (Facility, tenantRef, error) {
	row, err := r.q.LookupLocation(ctx, gln)
	if err != nil {
		return Facility{}, tenantRef{}, fmt.Errorf("look up GLN: %w", err)
	}
	// The lookup returns NULL in every column for a GLN without an active location.
	if row.LocationID == nil || row.Gln == nil || row.Name == nil || row.Latitude == nil || row.Longitude == nil ||
		row.GeoFenceRadiusMeters == nil || row.TenantID == nil || row.TenantCode == nil || row.TenantLegalName == nil {
		return Facility{}, tenantRef{}, errNotVisible
	}
	facility := Facility{
		LocationID: *row.LocationID, GLN: *row.Gln, Name: *row.Name, Latitude: *row.Latitude, Longitude: *row.Longitude,
		GeoFenceRadiusMeters: int(*row.GeoFenceRadiusMeters),
	}
	return facility, tenantRef{ID: *row.TenantID, Code: *row.TenantCode, LegalName: *row.TenantLegalName}, nil
}

func (r repository) LookUpTenant(ctx context.Context, code string) (tenantRef, error) {
	row, err := r.q.LookupTenant(ctx, code)
	if err != nil {
		return tenantRef{}, fmt.Errorf("look up tenant code: %w", err)
	}
	if row.TenantID == nil || row.Code == nil || row.LegalName == nil {
		return tenantRef{}, errNotVisible
	}
	return tenantRef{ID: *row.TenantID, Code: *row.Code, LegalName: *row.LegalName}, nil
}

func (r repository) OwnTenant(ctx context.Context, tenantID uuid.UUID) (tenantRef, error) {
	row, err := r.q.GetOwnTenant(ctx, tenantID)
	if err != nil {
		return tenantRef{}, fmt.Errorf("read tenant: %w", err)
	}
	return tenantRef{ID: row.ID, Code: row.Code, LegalName: row.LegalName}, nil
}

func (r repository) IssueSSCC(ctx context.Context, tenantID uuid.UUID) (string, error) {
	row, err := r.q.TakeSSCCSerial(ctx, tenantID)
	if err != nil {
		return "", fmt.Errorf("take SSCC serial: %w", err)
	}
	sscc, err := gs1.BuildSSCC(int(row.SsccExtensionDigit), row.Gs1CompanyPrefix, row.Serial)
	if errors.Is(err, gs1.ErrSerialSpaceExhausted) {
		return "", ErrSerialSpaceExhausted
	}
	return sscc, err
}

func (r repository) ActiveDriver(ctx context.Context, userID uuid.UUID) (bool, error) {
	row, err := r.q.GetUser(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read user: %w", err)
	}
	return row.IsActive && identity.Role(row.Role) == identity.RoleDriver, nil
}

func (r repository) Insert(ctx context.Context, s newRow) (uuid.UUID, error) {
	id, err := r.q.InsertShipment(ctx, queries.InsertShipmentParams{
		OwnerTenantID: s.OwnerTenantID, Sscc: s.SSCC, LotID: s.Lot.ID,
		Quantity: int32(s.Quantity), //nolint:gosec // validated to fit an integer column
		Gtin:     s.Lot.GTIN, ProductName: s.Lot.ProductName, LotNumber: s.Lot.LotNumber,
		ExpirationDate: s.Lot.ExpirationDate, MinTempCelsius: s.Lot.MinTempCelsius, MaxTempCelsius: s.Lot.MaxTempCelsius,
		OriginLocationID: s.Origin.LocationID, OriginGln: s.Origin.GLN, OriginName: s.Origin.Name,
		OriginLatitude: s.Origin.Latitude, OriginLongitude: s.Origin.Longitude,
		OriginGeoFenceRadiusMeters: int32(s.Origin.GeoFenceRadiusMeters), //nolint:gosec // 50–5000
		DestinationLocationID:      s.Destination.LocationID, DestinationGln: s.Destination.GLN,
		DestinationName: s.Destination.Name, DestinationLatitude: s.Destination.Latitude,
		DestinationLongitude:            s.Destination.Longitude,
		DestinationGeoFenceRadiusMeters: int32(s.Destination.GeoFenceRadiusMeters), //nolint:gosec // 50–5000
		ConsigneeTenantID:               s.ConsigneeTenantID, CarrierTenantID: s.CarrierTenantID,
		AssignedDriverID: s.AssignedDriverID, CreatedBy: s.CreatedBy, CreatedAt: s.CreatedAt,
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("insert shipment: %w", err)
	}
	return id, nil
}

func (r repository) AddParticipant(ctx context.Context, shipmentID uuid.UUID, p Participant, at time.Time) error {
	err := r.q.InsertParticipant(ctx, queries.InsertParticipantParams{
		ShipmentID: shipmentID, TenantID: p.TenantID, Role: string(p.Role), TenantCode: p.TenantCode,
		TenantLegalName: p.LegalName, CreatedAt: at,
	})
	if err != nil {
		return fmt.Errorf("add %s participant: %w", p.Role, err)
	}
	return nil
}

func (r repository) RemoveParticipant(ctx context.Context, shipmentID, tenantID uuid.UUID, role Role) error {
	err := r.q.DeleteParticipant(ctx, queries.DeleteParticipantParams{ShipmentID: shipmentID, TenantID: tenantID, Role: string(role)})
	if err != nil {
		return fmt.Errorf("remove %s participant: %w", role, err)
	}
	return nil
}

func (r repository) ApplyMovement(ctx context.Context, m inventory.Movement) error {
	_, err := inventory.ApplyMovement(ctx, r.tx, m)
	if errors.Is(err, inventory.ErrInsufficientStock) {
		return ErrInsufficientStock
	}
	return err
}

func (r repository) AppendEvent(ctx context.Context, e event.New) error {
	_, err := event.Append(ctx, r.tx, e)
	return err
}

func (r repository) Get(ctx context.Context, id uuid.UUID) (Shipment, error) {
	row, err := r.q.GetShipment(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Shipment{}, ErrNotFound
	}
	if err != nil {
		return Shipment{}, fmt.Errorf("read shipment: %w", err)
	}
	return r.withParticipants(ctx, fromRow(row))
}

func (r repository) Lock(ctx context.Context, id uuid.UUID) (Shipment, error) {
	row, err := r.q.LockShipment(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Shipment{}, ErrNotFound
	}
	if err != nil {
		return Shipment{}, fmt.Errorf("lock shipment: %w", err)
	}
	return r.withParticipants(ctx, fromRow(queries.GetShipmentRow(row)))
}

func (r repository) withParticipants(ctx context.Context, s Shipment) (Shipment, error) {
	shipments := []Shipment{s}
	if err := r.addParticipants(ctx, shipments); err != nil {
		return Shipment{}, err
	}
	return shipments[0], nil
}

// addParticipants fills in the participants of every shipment with one query.
func (r repository) addParticipants(ctx context.Context, shipments []Shipment) error {
	if len(shipments) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(shipments))
	index := make(map[uuid.UUID]int, len(shipments))
	for i, s := range shipments {
		ids[i], index[s.ID] = s.ID, i
		shipments[i].Participants = []Participant{}
	}
	rows, err := r.q.ListParticipants(ctx, ids)
	if err != nil {
		return fmt.Errorf("list participants: %w", err)
	}
	for _, row := range rows {
		i := index[row.ShipmentID]
		shipments[i].Participants = append(shipments[i].Participants, Participant{
			TenantID: row.TenantID, Role: Role(row.Role), TenantCode: row.TenantCode, LegalName: row.TenantLegalName,
		})
	}
	return nil
}

func (r repository) List(ctx context.Context, f Filter) ([]Shipment, error) {
	params := queries.ListShipmentsParams{
		LotID: f.LotID, DriverID: f.DriverID, RowLimit: int32(f.Limit), //nolint:gosec // limit is at most 101
	}
	if f.Status != nil {
		status := string(*f.Status)
		params.Status = &status
	}
	if f.Party != nil {
		party := string(*f.Party)
		params.Party = &party
	}
	if f.SSCC != "" {
		params.Sscc = &f.SSCC
	}
	if f.After != uuid.Nil {
		params.After = &f.After
	}
	rows, err := r.q.ListShipments(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("list shipments: %w", err)
	}
	shipments := make([]Shipment, len(rows))
	for i, row := range rows {
		shipments[i] = fromRow(queries.GetShipmentRow(row))
	}
	if err := r.addParticipants(ctx, shipments); err != nil {
		return nil, err
	}
	return shipments, nil
}

func (r repository) Summary(ctx context.Context, driverID *uuid.UUID) (Summary, error) {
	row, err := r.q.CountShipments(ctx, driverID)
	if err != nil {
		return Summary{}, fmt.Errorf("count shipments: %w", err)
	}
	return Summary{
		Created: int(row.Created), InTransit: int(row.InTransit), Delivered: int(row.Delivered),
		Cancelled: int(row.Cancelled), Recalled: int(row.Recalled),
	}, nil
}

func (r repository) SetCarrier(ctx context.Context, id, carrierTenantID uuid.UUID) error {
	if err := r.q.SetCarrier(ctx, queries.SetCarrierParams{ID: id, CarrierTenantID: carrierTenantID}); err != nil {
		return fmt.Errorf("set carrier: %w", err)
	}
	return nil
}

func (r repository) SetDriver(ctx context.Context, id, driverID uuid.UUID) error {
	if err := r.q.SetDriver(ctx, queries.SetDriverParams{ID: id, AssignedDriverID: &driverID}); err != nil {
		return fmt.Errorf("set driver: %w", err)
	}
	return nil
}

func (r repository) SetCancelled(ctx context.Context, id uuid.UUID, at time.Time) error {
	if err := r.q.SetCancelled(ctx, queries.SetCancelledParams{ID: id, CancelledAt: &at}); err != nil {
		return fmt.Errorf("cancel shipment: %w", err)
	}
	return nil
}

func (r repository) SetPickedUp(ctx context.Context, id uuid.UUID, at time.Time) error {
	if err := r.q.SetPickedUp(ctx, queries.SetPickedUpParams{ID: id, PickedUpAt: &at}); err != nil {
		return fmt.Errorf("mark shipment picked up: %w", err)
	}
	return nil
}

func (r repository) InvalidatePickupCodes(ctx context.Context, id uuid.UUID, at time.Time) error {
	err := r.q.InvalidatePickupCodes(ctx, queries.InvalidatePickupCodesParams{ShipmentID: id, InvalidatedAt: &at})
	if err != nil {
		return fmt.Errorf("invalidate pickup codes: %w", err)
	}
	return nil
}

func (r repository) InsertPickupCode(ctx context.Context, id uuid.UUID, hash []byte, expiresAt time.Time, issuedBy uuid.UUID, at time.Time) error {
	err := r.q.InsertPickupCode(ctx, queries.InsertPickupCodeParams{
		ShipmentID: id, CodeHash: hash, ExpiresAt: expiresAt, IssuedBy: issuedBy, CreatedAt: at,
	})
	if err != nil {
		return fmt.Errorf("insert pickup code: %w", err)
	}
	return nil
}

func (r repository) LockPickupCode(ctx context.Context, id uuid.UUID) (pickupCode, error) {
	row, err := r.q.LockActivePickupCode(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return pickupCode{}, errNotVisible
	}
	if err != nil {
		return pickupCode{}, fmt.Errorf("lock pickup code: %w", err)
	}
	return pickupCode{ID: row.ID, Hash: row.CodeHash, ExpiresAt: row.ExpiresAt, FailedAttempts: int(row.FailedAttempts)}, nil
}

func (r repository) RecordFailedAttempt(ctx context.Context, codeID uuid.UUID) (int, error) {
	attempts, err := r.q.RecordFailedAttempt(ctx, codeID)
	if err != nil {
		return 0, fmt.Errorf("record failed attempt: %w", err)
	}
	return int(attempts), nil
}

func (r repository) ConsumePickupCode(ctx context.Context, codeID uuid.UUID, at time.Time) error {
	if err := r.q.ConsumePickupCode(ctx, queries.ConsumePickupCodeParams{ID: codeID, ConsumedAt: &at}); err != nil {
		return fmt.Errorf("consume pickup code: %w", err)
	}
	return nil
}

func (r repository) Events(ctx context.Context, id uuid.UUID, page event.Page) ([]event.Event, error) {
	return event.List(ctx, r.tx, id, page)
}

func (r repository) AllEvents(ctx context.Context, id uuid.UUID) ([]event.Event, error) {
	return event.All(ctx, r.tx, id)
}

func fromRow(row queries.GetShipmentRow) Shipment {
	return Shipment{
		ID: row.ID, SSCC: row.Sscc, Status: policy.ShipmentStatus(row.Status), Quantity: int(row.Quantity),
		Lot: LotRef{ID: row.LotID, LotNumber: row.LotNumber, ExpirationDate: row.ExpirationDate},
		Product: ProductRef{
			GTIN: row.Gtin, Name: row.ProductName, MinTempCelsius: row.MinTempCelsius, MaxTempCelsius: row.MaxTempCelsius,
		},
		Origin: Facility{
			LocationID: row.OriginLocationID, GLN: row.OriginGln, Name: row.OriginName, Latitude: row.OriginLatitude,
			Longitude: row.OriginLongitude, GeoFenceRadiusMeters: int(row.OriginGeoFenceRadiusMeters),
		},
		Destination: Facility{
			LocationID: row.DestinationLocationID, GLN: row.DestinationGln, Name: row.DestinationName,
			Latitude: row.DestinationLatitude, Longitude: row.DestinationLongitude,
			GeoFenceRadiusMeters: int(row.DestinationGeoFenceRadiusMeters),
		},
		OwnerTenantID: row.OwnerTenantID, CarrierTenantID: row.CarrierTenantID, ConsigneeTenantID: row.ConsigneeTenantID,
		AssignedDriverID: row.AssignedDriverID, PickedUpAt: row.PickedUpAt, DeliveredAt: row.DeliveredAt,
		CancelledAt: row.CancelledAt, RecalledAt: row.RecalledAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

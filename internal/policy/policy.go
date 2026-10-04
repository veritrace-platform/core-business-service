// Package policy decides what a user may do with a resource that row-level security already lets the user see
// (ADR-0009). Each action states the parties, roles, resource states, and context checks it needs; the table
// follows veritrace/docs/domain/access-control.md, and an action without an entry is denied.
package policy

import (
	"fmt"
	"slices"

	"github.com/veritrace-platform/core-business-service/internal/identity"
)

// Action is one thing a user can do.
type Action string

// Actions of the access control matrix.
const (
	ManageTenantProfile Action = "tenant.manage_profile"
	ManageUsers         Action = "users.manage"
	ListDrivers         Action = "users.list_drivers"
	ChangeOwnPassword   Action = "account.change_password"
	ViewCatalog         Action = "catalog.view"
	ManageLocations     Action = "locations.manage"
	ManageProducts      Action = "products.manage"
	LookUpDirectory     Action = "directory.look_up"
	ViewLots            Action = "lots.view"
	CommissionLot       Action = "lots.commission"
	ViewInventory       Action = "inventory.view"
	CreateShipment      Action = "shipments.create"
	AssignCarrier       Action = "shipments.assign_carrier"
	AssignDriver        Action = "shipments.assign_driver"
	IssuePickupCode     Action = "shipments.issue_pickup_code"
	ConfirmPickup       Action = "shipments.confirm_pickup"
	RecordCheckpoint    Action = "shipments.record_checkpoint"
	ConfirmDelivery     Action = "shipments.confirm_delivery"
	CancelShipment      Action = "shipments.cancel"
	RecallLot           Action = "lots.recall"
	ViewShipment        Action = "shipments.view"

	// M2.
	AddInspector     Action = "shipments.add_inspector"
	UploadDocument   Action = "documents.upload"
	ReadDocument     Action = "documents.read"
	IssueLabelSeries Action = "lots.issue_label_series"
)

// Party is the caller's relationship to a resource. Master data belongs to the caller's own tenant, an account
// to the caller itself, and a shipment or lot to its participants, owner, and holders. Directory entries are
// published to every tenant.
type Party string

// Parties.
const (
	AnyTenant Party = "ANY_TENANT"
	OwnTenant Party = "OWN_TENANT"
	Self      Party = "SELF"
	Owner     Party = "OWNER"
	Carrier   Party = "CARRIER"
	Consignee Party = "CONSIGNEE"
	Inspector Party = "INSPECTOR"
	LotOwner  Party = "LOT_OWNER"
	LotHolder Party = "LOT_HOLDER"
)

// ShipmentStatus is the state of a shipment (shipment-lifecycle.md §3).
type ShipmentStatus string

// Shipment statuses.
const (
	ShipmentCreated   ShipmentStatus = "CREATED"
	ShipmentInTransit ShipmentStatus = "IN_TRANSIT"
	ShipmentDelivered ShipmentStatus = "DELIVERED"
	ShipmentCancelled ShipmentStatus = "CANCELLED"
	ShipmentRecalled  ShipmentStatus = "RECALLED"
)

// LotStatus is the state of a lot.
type LotStatus string

// Lot statuses.
const (
	LotActive   LotStatus = "ACTIVE"
	LotRecalled LotStatus = "RECALLED"
)

// Check is a context condition that the service establishes, such as a scanned SSCC matching the shipment.
type Check string

// Checks named by the access control matrix.
const (
	CurrentPassword     Check = "CURRENT_PASSWORD"
	GLNPrefix           Check = "GLN_PREFIX"
	GTINPrefix          Check = "GTIN_PREFIX"
	ProductOwned        Check = "PRODUCT_OWNED"
	LocationOwned       Check = "LOCATION_OWNED"
	SufficientBalance   Check = "SUFFICIENT_BALANCE"
	OriginOwned         Check = "ORIGIN_OWNED"
	NoExternalCarrier   Check = "NO_EXTERNAL_CARRIER"
	DriverEligible      Check = "DRIVER_ELIGIBLE"
	DriverAssigned      Check = "DRIVER_ASSIGNED"
	SSCCMatch           Check = "SSCC_MATCH"
	PickupCodeActive    Check = "PICKUP_CODE_ACTIVE"
	PickupCodeMatch     Check = "PICKUP_CODE_MATCH"
	OriginGeofence      Check = "ORIGIN_GEOFENCE"
	FacilityGeofence    Check = "FACILITY_GEOFENCE"
	DestinationGeofence Check = "DESTINATION_GEOFENCE"
	InspectorTenant     Check = "INSPECTOR_TENANT_EXISTS"
	DocumentLimits      Check = "DOCUMENT_LIMITS"
)

// Facts establishes checks on demand. The policy asks for each check of an action in order and stops at the
// first that fails, so a check with side effects, such as one that consumes a pickup code attempt, runs only
// after every earlier condition holds.
type Facts func(Check) (bool, error)

// Known returns Facts backed by precomputed results; a check missing from results fails.
func Known(results map[Check]bool) Facts {
	return func(c Check) (bool, error) { return results[c], nil }
}

// Request is one attempted action.
type Request struct {
	Action Action
	Role   identity.Role
	// Parties are the caller's relationships to the resource.
	Parties []Party
	// ShipmentStatus is the shipment's state, for shipment actions.
	ShipmentStatus ShipmentStatus
	// LotStatus is the lot's state, for lot actions.
	LotStatus LotStatus
	// AssignedDriver reports that the caller is the shipment's assigned driver.
	AssignedDriver bool
	Facts          Facts
}

// Reason is why an action was denied.
type Reason string

// Reasons, in the order the policy evaluates them.
const (
	ReasonUnknownAction    Reason = "UNKNOWN_ACTION"
	ReasonShipmentRecalled Reason = "SHIPMENT_RECALLED_LOCKED"
	ReasonInvalidState     Reason = "INVALID_STATE_TRANSITION"
	ReasonLotRecalled      Reason = "LOT_RECALLED"
	ReasonParty            Reason = "PARTY"
	ReasonRole             Reason = "ROLE"
	ReasonCheck            Reason = "CHECK"
)

// DenialError reports a denied action. Check names the failed check when Reason is ReasonCheck.
type DenialError struct {
	Action Action
	Reason Reason
	Check  Check
}

func (d *DenialError) Error() string {
	if d.Reason == ReasonCheck {
		return fmt.Sprintf("%s denied: check %s failed", d.Action, d.Check)
	}
	return fmt.Sprintf("%s denied: %s", d.Action, d.Reason)
}

// rule is one row of the access control matrix.
type rule struct {
	parties []Party
	roles   []identity.Role
	// driverMustBeAssigned limits the DRIVER role to the shipment's assigned driver.
	driverMustBeAssigned bool
	// command marks a state change, which a recalled shipment rejects.
	command bool
	// shipmentStates and lotStates list the states in which the action is allowed; nil allows any.
	shipmentStates []ShipmentStatus
	lotStates      []LotStatus
	// fromLot marks an action that creates something from a lot, which a recalled lot rejects with
	// LOT_RECALLED rather than as an invalid state.
	fromLot bool
	// checks run in this order after every other condition holds.
	checks []Check
}

var (
	admin           = []identity.Role{identity.RoleAdmin}
	managers        = []identity.Role{identity.RoleAdmin, identity.RoleWarehouseManager}
	anyRole         = identity.Roles
	driver          = []identity.Role{identity.RoleDriver}
	managersAndInsp = []identity.Role{identity.RoleAdmin, identity.RoleWarehouseManager, identity.RoleInspector}
	participants    = []Party{Owner, Carrier, Consignee, Inspector}
	notCancelled    = []ShipmentStatus{ShipmentCreated, ShipmentInTransit, ShipmentDelivered, ShipmentRecalled}
)

// rules is the access control matrix (access-control.md §2).
var rules = map[Action]rule{
	ManageTenantProfile: {parties: []Party{OwnTenant}, roles: admin},
	ManageUsers:         {parties: []Party{OwnTenant}, roles: admin},
	ListDrivers:         {parties: []Party{OwnTenant}, roles: managers},
	ChangeOwnPassword:   {parties: []Party{Self}, roles: anyRole, checks: []Check{CurrentPassword}},
	ViewCatalog:         {parties: []Party{OwnTenant}, roles: managers},
	ManageLocations:     {parties: []Party{OwnTenant}, roles: admin, checks: []Check{GLNPrefix}},
	ManageProducts:      {parties: []Party{OwnTenant}, roles: managers, checks: []Check{GTINPrefix}},
	LookUpDirectory:     {parties: []Party{AnyTenant}, roles: anyRole},
	ViewLots:            {parties: []Party{LotOwner, LotHolder}, roles: managers},
	CommissionLot: {
		parties: []Party{OwnTenant}, roles: managers, checks: []Check{ProductOwned, LocationOwned},
	},
	ViewInventory: {parties: []Party{OwnTenant}, roles: managers},
	CreateShipment: {
		parties: []Party{LotHolder}, roles: managers, lotStates: []LotStatus{LotActive}, fromLot: true,
		checks: []Check{OriginOwned, SufficientBalance},
	},
	AssignCarrier: {
		parties: []Party{Owner}, roles: managers, command: true,
		shipmentStates: []ShipmentStatus{ShipmentCreated}, checks: []Check{NoExternalCarrier},
	},
	AssignDriver: {
		parties: []Party{Carrier}, roles: managers, command: true,
		shipmentStates: []ShipmentStatus{ShipmentCreated}, checks: []Check{DriverEligible},
	},
	IssuePickupCode: {
		parties: []Party{Owner}, roles: managers, command: true,
		shipmentStates: []ShipmentStatus{ShipmentCreated}, checks: []Check{DriverAssigned},
	},
	ConfirmPickup: {
		parties: []Party{Carrier}, roles: driver, driverMustBeAssigned: true, command: true,
		shipmentStates: []ShipmentStatus{ShipmentCreated},
		checks:         []Check{SSCCMatch, PickupCodeActive, PickupCodeMatch, OriginGeofence},
	},
	RecordCheckpoint: {
		parties: []Party{Carrier}, roles: driver, driverMustBeAssigned: true, command: true,
		shipmentStates: []ShipmentStatus{ShipmentInTransit}, checks: []Check{SSCCMatch, FacilityGeofence},
	},
	ConfirmDelivery: {
		parties: []Party{Consignee}, roles: managers, command: true,
		shipmentStates: []ShipmentStatus{ShipmentInTransit}, checks: []Check{SSCCMatch, DestinationGeofence},
	},
	CancelShipment: {
		parties: []Party{Owner}, roles: managers, command: true, shipmentStates: []ShipmentStatus{ShipmentCreated},
	},
	RecallLot: {parties: []Party{LotOwner}, roles: admin, lotStates: []LotStatus{LotActive}},
	ViewShipment: {
		parties: participants, roles: anyRole, driverMustBeAssigned: true,
	},

	// Audits continue after a recall, so inspectors and documents are allowed on a recalled shipment.
	AddInspector: {
		parties: []Party{Owner}, roles: admin, shipmentStates: notCancelled, checks: []Check{InspectorTenant},
	},
	UploadDocument: {
		parties: []Party{Owner, Inspector}, roles: managersAndInsp, shipmentStates: notCancelled,
		checks: []Check{DocumentLimits},
	},
	ReadDocument: {parties: participants, roles: managersAndInsp},
	IssueLabelSeries: {
		parties: []Party{LotOwner}, roles: managers, lotStates: []LotStatus{LotActive}, fromLot: true,
	},
}

// Evaluate returns nil when the request is allowed and a *DenialError when it is not. Conditions are checked in a
// fixed order, so every denial names the first condition that fails: a recalled shipment for commands, the
// resource state, the caller's party, its role (and, for drivers, the assignment), and then each check. An
// error from Facts is returned as is.
func Evaluate(req Request) error {
	r, ok := rules[req.Action]
	if !ok {
		return &DenialError{Action: req.Action, Reason: ReasonUnknownAction}
	}
	deny := func(reason Reason) error { return &DenialError{Action: req.Action, Reason: reason} }

	if r.command && req.ShipmentStatus == ShipmentRecalled {
		return deny(ReasonShipmentRecalled)
	}
	if r.shipmentStates != nil && !slices.Contains(r.shipmentStates, req.ShipmentStatus) {
		return deny(ReasonInvalidState)
	}
	if r.lotStates != nil && !slices.Contains(r.lotStates, req.LotStatus) {
		if r.fromLot && req.LotStatus == LotRecalled {
			return deny(ReasonLotRecalled)
		}
		return deny(ReasonInvalidState)
	}
	if !slices.ContainsFunc(req.Parties, func(p Party) bool { return slices.Contains(r.parties, p) }) {
		return deny(ReasonParty)
	}
	if !slices.Contains(r.roles, req.Role) || (req.Role == identity.RoleDriver && r.driverMustBeAssigned && !req.AssignedDriver) {
		return deny(ReasonRole)
	}
	for _, c := range r.checks {
		if req.Facts == nil {
			return &DenialError{Action: req.Action, Reason: ReasonCheck, Check: c}
		}
		ok, err := req.Facts(c)
		if err != nil {
			return fmt.Errorf("check %s for %s: %w", c, req.Action, err)
		}
		if !ok {
			return &DenialError{Action: req.Action, Reason: ReasonCheck, Check: c}
		}
	}
	return nil
}

// Checks returns the checks of an action in the order Evaluate asks for them.
func Checks(a Action) []Check {
	return slices.Clone(rules[a].checks)
}

// Actions returns every action that has a rule, sorted.
func Actions() []Action {
	actions := make([]Action, 0, len(rules))
	for a := range rules {
		actions = append(actions, a)
	}
	slices.Sort(actions)
	return actions
}

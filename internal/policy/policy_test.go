package policy_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/veritrace-platform/core-business-service/internal/identity"
	"github.com/veritrace-platform/core-business-service/internal/policy"
)

// row is one row of access-control.md §2, transcribed independently of the implementation.
type row struct {
	parties []policy.Party
	roles   []identity.Role
	// assignedDriverOnly admits DRIVER only as the shipment's assigned driver.
	assignedDriverOnly bool
	// shipmentStates and lotStates are the states that allow the action; empty means any.
	shipmentStates []policy.ShipmentStatus
	lotStates      []policy.LotStatus
	// command marks a shipment state change, which a recalled shipment rejects.
	command bool
	checks  []policy.Check
}

var (
	admin     = []identity.Role{identity.RoleAdmin}
	managers  = []identity.Role{identity.RoleAdmin, identity.RoleWarehouseManager}
	allRoles  = []identity.Role{identity.RoleAdmin, identity.RoleWarehouseManager, identity.RoleDriver, identity.RoleInspector}
	docRoles  = []identity.Role{identity.RoleAdmin, identity.RoleWarehouseManager, identity.RoleInspector}
	anyParty  = []policy.Party{policy.Owner, policy.Carrier, policy.Consignee, policy.Inspector}
	created   = []policy.ShipmentStatus{policy.ShipmentCreated}
	inTransit = []policy.ShipmentStatus{policy.ShipmentInTransit}
	active    = []policy.LotStatus{policy.LotActive}
	notCanc   = []policy.ShipmentStatus{policy.ShipmentCreated, policy.ShipmentInTransit, policy.ShipmentDelivered, policy.ShipmentRecalled}
)

var matrix = map[policy.Action]row{
	policy.ManageTenantProfile: {parties: []policy.Party{policy.OwnTenant}, roles: admin},
	policy.ManageUsers:         {parties: []policy.Party{policy.OwnTenant}, roles: admin},
	policy.ListDrivers:         {parties: []policy.Party{policy.OwnTenant}, roles: managers},
	policy.ChangeOwnPassword:   {parties: []policy.Party{policy.Self}, roles: allRoles, checks: []policy.Check{policy.CurrentPassword}},
	policy.ViewCatalog:         {parties: []policy.Party{policy.OwnTenant}, roles: managers},
	policy.LookUpDirectory:     {parties: []policy.Party{policy.AnyTenant}, roles: allRoles},
	policy.ViewLots:            {parties: []policy.Party{policy.LotOwner, policy.LotHolder}, roles: managers},
	policy.ManageLocations:     {parties: []policy.Party{policy.OwnTenant}, roles: admin, checks: []policy.Check{policy.GLNPrefix}},
	policy.ManageProducts:      {parties: []policy.Party{policy.OwnTenant}, roles: managers, checks: []policy.Check{policy.GTINPrefix}},
	policy.CommissionLot:       {parties: []policy.Party{policy.OwnTenant}, roles: managers, checks: []policy.Check{policy.ProductOwned, policy.LocationOwned}},
	policy.ViewInventory:       {parties: []policy.Party{policy.OwnTenant}, roles: managers},
	policy.CreateShipment:      {parties: []policy.Party{policy.LotHolder}, roles: managers, lotStates: active, checks: []policy.Check{policy.SufficientBalance, policy.OriginOwned}},
	policy.AssignCarrier:       {parties: []policy.Party{policy.Owner}, roles: managers, shipmentStates: created, command: true, checks: []policy.Check{policy.NoExternalCarrier}},
	policy.AssignDriver:        {parties: []policy.Party{policy.Carrier}, roles: managers, shipmentStates: created, command: true, checks: []policy.Check{policy.DriverEligible}},
	policy.IssuePickupCode:     {parties: []policy.Party{policy.Owner}, roles: managers, shipmentStates: created, command: true, checks: []policy.Check{policy.DriverAssigned}},
	policy.ConfirmPickup: {
		parties: []policy.Party{policy.Carrier}, roles: []identity.Role{identity.RoleDriver}, assignedDriverOnly: true,
		shipmentStates: created, command: true,
		checks: []policy.Check{policy.SSCCMatch, policy.PickupCodeActive, policy.PickupCodeMatch, policy.OriginGeofence},
	},
	policy.RecordCheckpoint: {
		parties: []policy.Party{policy.Carrier}, roles: []identity.Role{identity.RoleDriver}, assignedDriverOnly: true,
		shipmentStates: inTransit, command: true, checks: []policy.Check{policy.SSCCMatch, policy.FacilityGeofence},
	},
	policy.ConfirmDelivery: {parties: []policy.Party{policy.Consignee}, roles: managers, shipmentStates: inTransit, command: true, checks: []policy.Check{policy.SSCCMatch, policy.DestinationGeofence}},
	policy.CancelShipment:  {parties: []policy.Party{policy.Owner}, roles: managers, shipmentStates: created, command: true},
	policy.RecallLot:       {parties: []policy.Party{policy.LotOwner}, roles: admin, lotStates: active},
	policy.ViewShipment:    {parties: anyParty, roles: allRoles, assignedDriverOnly: true},

	policy.AddInspector:     {parties: []policy.Party{policy.Owner}, roles: admin, shipmentStates: notCanc, checks: []policy.Check{policy.InspectorTenant}},
	policy.UploadDocument:   {parties: []policy.Party{policy.Owner, policy.Inspector}, roles: docRoles, shipmentStates: notCanc, checks: []policy.Check{policy.DocumentLimits}},
	policy.ReadDocument:     {parties: anyParty, roles: docRoles},
	policy.IssueLabelSeries: {parties: []policy.Party{policy.LotOwner}, roles: managers, lotStates: active},
}

var (
	allParties = []policy.Party{
		policy.AnyTenant, policy.OwnTenant, policy.Self, policy.Owner, policy.Carrier, policy.Consignee, policy.Inspector,
		policy.LotOwner, policy.LotHolder,
	}
	allShipmentStates = []policy.ShipmentStatus{
		policy.ShipmentCreated, policy.ShipmentInTransit, policy.ShipmentDelivered, policy.ShipmentCancelled,
		policy.ShipmentRecalled,
	}
	allLotStates = []policy.LotStatus{policy.LotActive, policy.LotRecalled}
)

// satisfying returns a request for a that meets every condition except the ones the test sets.
func satisfying(a policy.Action, r row) policy.Request {
	req := policy.Request{Action: a, Facts: func(policy.Check) (bool, error) { return true, nil }}
	if len(r.shipmentStates) > 0 {
		req.ShipmentStatus = r.shipmentStates[0]
	}
	if len(r.lotStates) > 0 {
		req.LotStatus = r.lotStates[0]
	}
	return req
}

func reasonOf(err error) policy.Reason {
	var d *policy.DenialError
	if errors.As(err, &d) {
		return d.Reason
	}
	return ""
}

func TestEveryActionHasARow(t *testing.T) {
	for _, a := range policy.Actions() {
		if _, ok := matrix[a]; !ok {
			t.Errorf("action %s has a rule but no row in the test matrix", a)
		}
	}
	if len(policy.Actions()) != len(matrix) {
		t.Errorf("%d actions, %d matrix rows", len(policy.Actions()), len(matrix))
	}
}

func TestPartiesAndRoles(t *testing.T) {
	for a, r := range matrix {
		for _, party := range allParties {
			for _, role := range allRoles {
				for _, assigned := range []bool{false, true} {
					req := satisfying(a, r)
					req.Parties, req.Role, req.AssignedDriver = []policy.Party{party}, role, assigned

					wantParty := slices.Contains(r.parties, party)
					wantRole := slices.Contains(r.roles, role) &&
						(role != identity.RoleDriver || !r.assignedDriverOnly || assigned)
					err := policy.Evaluate(req)
					switch {
					case wantParty && wantRole && err != nil:
						t.Errorf("%s by %s as %s (assigned %t): %v, want allowed", a, role, party, assigned, err)
					case !wantParty && reasonOf(err) != policy.ReasonParty:
						t.Errorf("%s by %s as %s: %v, want a party denial", a, role, party, err)
					case wantParty && !wantRole && reasonOf(err) != policy.ReasonRole:
						t.Errorf("%s by %s as %s (assigned %t): %v, want a role denial", a, role, party, assigned, err)
					}
				}
			}
		}
	}
}

func TestShipmentStates(t *testing.T) {
	for a, r := range matrix {
		if len(r.shipmentStates) == 0 && !r.command {
			continue
		}
		for _, status := range allShipmentStates {
			req := satisfying(a, r)
			req.Parties, req.Role, req.AssignedDriver = r.parties[:1], r.roles[0], true
			req.ShipmentStatus = status

			err := policy.Evaluate(req)
			switch {
			case r.command && status == policy.ShipmentRecalled:
				if reasonOf(err) != policy.ReasonShipmentRecalled {
					t.Errorf("%s on a recalled shipment: %v, want SHIPMENT_RECALLED_LOCKED", a, err)
				}
			case slices.Contains(r.shipmentStates, status):
				if err != nil {
					t.Errorf("%s in %s: %v, want allowed", a, status, err)
				}
			default:
				if reasonOf(err) != policy.ReasonInvalidState {
					t.Errorf("%s in %s: %v, want INVALID_STATE_TRANSITION", a, status, err)
				}
			}
		}
	}
}

func TestLotStates(t *testing.T) {
	createsFromLot := map[policy.Action]bool{policy.CreateShipment: true, policy.IssueLabelSeries: true}
	for a, r := range matrix {
		if len(r.lotStates) == 0 {
			continue
		}
		for _, status := range allLotStates {
			req := satisfying(a, r)
			req.Parties, req.Role, req.LotStatus = r.parties[:1], r.roles[0], status

			err := policy.Evaluate(req)
			switch {
			case slices.Contains(r.lotStates, status):
				if err != nil {
					t.Errorf("%s with a %s lot: %v, want allowed", a, status, err)
				}
			case createsFromLot[a]:
				if reasonOf(err) != policy.ReasonLotRecalled {
					t.Errorf("%s with a %s lot: %v, want LOT_RECALLED", a, status, err)
				}
			default:
				if reasonOf(err) != policy.ReasonInvalidState {
					t.Errorf("%s with a %s lot: %v, want INVALID_STATE_TRANSITION", a, status, err)
				}
			}
		}
	}
}

func TestChecksRunInOrderAndStopAtTheFirstFailure(t *testing.T) {
	for a, r := range matrix {
		if !slices.Equal(policy.Checks(a), r.checks) {
			t.Errorf("Checks(%s) = %v, want %v", a, policy.Checks(a), r.checks)
		}
		for failAt := range r.checks {
			var asked []policy.Check
			req := satisfying(a, r)
			req.Parties, req.Role, req.AssignedDriver = r.parties[:1], r.roles[0], true
			req.Facts = func(c policy.Check) (bool, error) {
				asked = append(asked, c)
				return c != r.checks[failAt], nil
			}
			err := policy.Evaluate(req)
			var d *policy.DenialError
			if !errors.As(err, &d) || d.Reason != policy.ReasonCheck || d.Check != r.checks[failAt] {
				t.Errorf("%s failing %s: %v", a, r.checks[failAt], err)
			}
			if !slices.Equal(asked, r.checks[:failAt+1]) {
				t.Errorf("%s failing %s asked %v, want %v", a, r.checks[failAt], asked, r.checks[:failAt+1])
			}
		}
	}
}

func TestChecksAreNotAskedWhenAnEarlierConditionFails(t *testing.T) {
	asked := false
	err := policy.Evaluate(policy.Request{
		Action: policy.ConfirmPickup, Role: identity.RoleDriver, Parties: []policy.Party{policy.Carrier},
		ShipmentStatus: policy.ShipmentCreated, AssignedDriver: false,
		Facts: func(policy.Check) (bool, error) { asked = true; return true, nil },
	})
	if reasonOf(err) != policy.ReasonRole || asked {
		t.Errorf("unassigned driver: %v, checks asked = %t; want a role denial before any check", err, asked)
	}
}

func TestFactErrorsAreReturned(t *testing.T) {
	boom := errors.New("database unavailable")
	err := policy.Evaluate(policy.Request{
		Action: policy.ChangeOwnPassword, Role: identity.RoleDriver, Parties: []policy.Party{policy.Self},
		Facts: func(policy.Check) (bool, error) { return false, boom },
	})
	var d *policy.DenialError
	if !errors.Is(err, boom) || errors.As(err, &d) {
		t.Errorf("error = %v, want the fact error and no denial", err)
	}
}

func TestMissingFactsFailClosed(t *testing.T) {
	err := policy.Evaluate(policy.Request{
		Action: policy.ManageLocations, Role: identity.RoleAdmin, Parties: []policy.Party{policy.OwnTenant},
	})
	var d *policy.DenialError
	if !errors.As(err, &d) || d.Reason != policy.ReasonCheck || d.Check != policy.GLNPrefix {
		t.Errorf("error = %v, want the GLN prefix check to fail", err)
	}
	err = policy.Evaluate(policy.Request{
		Action: policy.ManageLocations, Role: identity.RoleAdmin, Parties: []policy.Party{policy.OwnTenant},
		Facts: policy.Known(map[policy.Check]bool{}),
	})
	if reasonOf(err) != policy.ReasonCheck {
		t.Errorf("unknown check result: %v, want a denial", err)
	}
	if err := policy.Evaluate(policy.Request{
		Action: policy.ManageLocations, Role: identity.RoleAdmin, Parties: []policy.Party{policy.OwnTenant},
		Facts: policy.Known(map[policy.Check]bool{policy.GLNPrefix: true}),
	}); err != nil {
		t.Errorf("known passing check: %v", err)
	}
}

func TestUnknownActionsAndPartiesAreDenied(t *testing.T) {
	if reasonOf(policy.Evaluate(policy.Request{Action: "shipments.teleport", Role: identity.RoleAdmin})) != policy.ReasonUnknownAction {
		t.Error("an action without a rule was not denied")
	}
	if reasonOf(policy.Evaluate(policy.Request{Action: policy.ManageUsers, Role: identity.RoleAdmin})) != policy.ReasonParty {
		t.Error("a request without parties was not denied")
	}
	if reasonOf(policy.Evaluate(policy.Request{Action: policy.ManageUsers, Role: "SUPERUSER", Parties: []policy.Party{policy.OwnTenant}})) != policy.ReasonRole {
		t.Error("an unknown role was not denied")
	}
}

func TestDenialMessages(t *testing.T) {
	check := &policy.DenialError{Action: policy.ConfirmPickup, Reason: policy.ReasonCheck, Check: policy.SSCCMatch}
	role := &policy.DenialError{Action: policy.ManageUsers, Reason: policy.ReasonRole}
	if check.Error() != "shipments.confirm_pickup denied: check SSCC_MATCH failed" || role.Error() != "users.manage denied: ROLE" {
		t.Errorf("messages = %q, %q", check.Error(), role.Error())
	}
}

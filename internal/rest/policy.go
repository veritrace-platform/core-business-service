package rest

import (
	"errors"
	"net/http"

	"github.com/veritrace-platform/core-business-service/internal/platform/httpx"
	"github.com/veritrace-platform/core-business-service/internal/policy"
)

// Problem codes for denied state changes (rest-api.md §1.1).
const (
	CodeInvalidStateTransition = "INVALID_STATE_TRANSITION"
	CodeShipmentRecalledLocked = "SHIPMENT_RECALLED_LOCKED"
	CodeLotRecalled            = "LOT_RECALLED"
)

// Denied answers a policy denial. A resource in the wrong state answers 409 with the state's code; a caller
// without the party or role answers 403 FORBIDDEN, because the resource is visible but the action is not
// permitted. It reports false when err is not a denial.
//
// Failed checks carry details that only the domain handler knows, such as a geo-fence distance, so domain
// handlers map the checks they expect before calling Denied; any other failed check answers 403.
func Denied(w http.ResponseWriter, r *http.Request, err error) bool {
	var d *policy.DenialError
	if !errors.As(err, &d) {
		return false
	}
	var p httpx.Problem
	switch d.Reason {
	case policy.ReasonShipmentRecalled:
		p = httpx.NewProblem(http.StatusConflict, CodeShipmentRecalledLocked, "the shipment is recalled and accepts no further commands")
	case policy.ReasonInvalidState:
		p = httpx.NewProblem(http.StatusConflict, CodeInvalidStateTransition, "the action is not allowed in the resource's current state")
	case policy.ReasonLotRecalled:
		p = httpx.NewProblem(http.StatusConflict, CodeLotRecalled, "the lot is recalled")
	case policy.ReasonUnknownAction, policy.ReasonParty, policy.ReasonRole, policy.ReasonCheck:
		p = httpx.NewProblem(http.StatusForbidden, httpx.CodeForbidden, "you are not permitted to perform this action")
	}
	httpx.WriteProblem(w, r, p)
	return true
}

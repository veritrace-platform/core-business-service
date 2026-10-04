package lot

import (
	"math"
	"strings"

	"github.com/veritrace-platform/core-business-service/internal/gs1"
	"github.com/veritrace-platform/core-business-service/internal/platform/httpx"
	"github.com/veritrace-platform/core-business-service/internal/rest"
)

// commissionRequest is the body of POST /api/v1/lots.
type commissionRequest struct {
	ProductID              string `json:"product_id"`
	LotNumber              string `json:"lot_number"`
	ProductionDate         string `json:"production_date"`
	ExpirationDate         string `json:"expiration_date"`
	QuantityCommissioned   *int   `json:"quantity_commissioned"`
	CommissionedLocationID string `json:"commissioned_location_id"`
}

// validate checks the request, recording errors in v, and returns the lot it describes; the result is
// meaningful only when v holds no errors. The service checks that the product and location are the tenant's.
func (req *commissionRequest) validate(v *rest.Validator) NewLot {
	var nl NewLot
	nl.ProductID, _ = v.UUID("product_id", strings.TrimSpace(req.ProductID))
	nl.LotNumber = strings.TrimSpace(req.LotNumber)
	switch {
	case nl.LotNumber == "":
		v.Add("lot_number", httpx.FieldRequired, "is required")
	case gs1.ValidateLotNumber(nl.LotNumber) != nil:
		v.Add("lot_number", httpx.FieldInvalidFormat, "must be 1 to 20 characters from 0-9, A-Z, a-z, '.', '_', and '-'")
	}
	var produced, expires bool
	nl.ProductionDate, produced = v.Date("production_date", strings.TrimSpace(req.ProductionDate))
	nl.ExpirationDate, expires = v.Date("expiration_date", strings.TrimSpace(req.ExpirationDate))
	if produced && expires && nl.ExpirationDate.Before(nl.ProductionDate) {
		v.Add("expiration_date", httpx.FieldOutOfRange, "must not be earlier than production_date")
	}
	if req.QuantityCommissioned == nil {
		v.Add("quantity_commissioned", httpx.FieldRequired, "is required")
	} else if v.Int("quantity_commissioned", *req.QuantityCommissioned, 1, math.MaxInt32) {
		nl.Quantity = *req.QuantityCommissioned
	}
	nl.LocationID, _ = v.UUID("commissioned_location_id", strings.TrimSpace(req.CommissionedLocationID))
	return nl
}

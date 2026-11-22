package product

import (
	"strings"

	"github.com/veritrace-platform/core-business-service/internal/gs1"
	"github.com/veritrace-platform/core-business-service/internal/platform/httpx"
	"github.com/veritrace-platform/core-business-service/internal/rest"
)

// Field length limits, as the table checks them.
const (
	maxNameLength        = 255
	maxDescriptionLength = 1000
	maxSearchLength      = 100
)

const (
	minBelowMax = "must be lower than max_temp_celsius"
	maxAboveMin = "must be higher than min_temp_celsius"
)

// createRequest is the body of POST /api/v1/products.
type createRequest struct {
	GTIN           string   `json:"gtin"`
	Name           string   `json:"name"`
	Description    *string  `json:"description"`
	MinTempCelsius *float64 `json:"min_temp_celsius"`
	MaxTempCelsius *float64 `json:"max_temp_celsius"`
	// ShelfLifeQ10 is optional; DefaultShelfLifeQ10 applies without it.
	ShelfLifeQ10 *float64 `json:"shelf_life_q10"`
}

// temperature checks one bound of the temperature range.
func temperature(v *rest.Validator, field string, value *float64) bool {
	return v.Decimal(field, value, MinTemperatureCelsius, MaxTemperatureCelsius, TemperatureDecimals)
}

// shelfLifeQ10 checks a shelf-life Q10.
func shelfLifeQ10(v *rest.Validator, value *float64) bool {
	return v.Decimal("shelf_life_q10", value, MinShelfLifeQ10, MaxShelfLifeQ10, ShelfLifeQ10Decimals)
}

// validate checks the request, recording errors in v, and returns the product it describes. The service
// checks the GTIN's company prefix, which only the tenant's record holds.
func (req *createRequest) validate(v *rest.Validator) NewProduct {
	req.GTIN = strings.TrimSpace(req.GTIN)
	if req.GTIN == "" {
		v.Add("gtin", httpx.FieldRequired, "is required")
	} else {
		v.Key("gtin", gs1.Validate(gs1.GTIN14, req.GTIN))
	}
	v.Text("name", &req.Name, 1, maxNameLength)
	v.OptionalText("description", &req.Description, 1, maxDescriptionLength)
	np := NewProduct{GTIN: req.GTIN, Name: req.Name, Description: req.Description, ShelfLifeQ10: DefaultShelfLifeQ10}
	if req.ShelfLifeQ10 != nil && shelfLifeQ10(v, req.ShelfLifeQ10) {
		np.ShelfLifeQ10 = *req.ShelfLifeQ10
	}
	minValid := temperature(v, "min_temp_celsius", req.MinTempCelsius)
	maxValid := temperature(v, "max_temp_celsius", req.MaxTempCelsius)
	if minValid && maxValid {
		np.MinTempCelsius, np.MaxTempCelsius = *req.MinTempCelsius, *req.MaxTempCelsius
		if np.MinTempCelsius >= np.MaxTempCelsius {
			v.Add("min_temp_celsius", httpx.FieldOutOfRange, minBelowMax)
		}
	}
	return np
}

// patchable lists the members of a product merge patch. The GTIN identifies the product and never changes.
var patchable = []string{"name", "description", "min_temp_celsius", "max_temp_celsius", "shelf_life_q10", "is_active"}

// readPatch checks a merge patch of a product, recording errors in v, and returns the changes it describes. A
// patch that moves one bound past the other's stored value is caught when it is applied.
func readPatch(v *rest.Validator, patch rest.Patch) Patch {
	var changes Patch
	if name := rest.PatchField[string](v, patch, "name"); name.Set {
		if name.Null {
			v.Add("name", httpx.FieldRequired, "cannot be null")
		} else if v.Text("name", &name.Value, 1, maxNameLength) {
			changes.Name = &name.Value
		}
	}
	if description := rest.PatchField[string](v, patch, "description"); description.Set {
		value := &description.Value
		if description.Null {
			value = nil
		}
		if v.OptionalText("description", &value, 1, maxDescriptionLength) {
			changes.Description, changes.ClearDescription = value, value == nil
		}
	}
	bound := func(field string) *float64 {
		value := rest.PatchField[float64](v, patch, field)
		switch {
		case !value.Set:
		case value.Null:
			v.Add(field, httpx.FieldRequired, "cannot be null")
		case temperature(v, field, &value.Value):
			return &value.Value
		}
		return nil
	}
	changes.MinTempCelsius = bound("min_temp_celsius")
	changes.MaxTempCelsius = bound("max_temp_celsius")
	if changes.MinTempCelsius != nil && changes.MaxTempCelsius != nil && *changes.MinTempCelsius >= *changes.MaxTempCelsius {
		v.Add("min_temp_celsius", httpx.FieldOutOfRange, minBelowMax)
	}
	if q10 := rest.PatchField[float64](v, patch, "shelf_life_q10"); q10.Set {
		if q10.Null {
			v.Add("shelf_life_q10", httpx.FieldRequired, "cannot be null")
		} else if shelfLifeQ10(v, &q10.Value) {
			changes.ShelfLifeQ10 = &q10.Value
		}
	}
	if active := rest.PatchField[bool](v, patch, "is_active"); active.Set {
		if active.Null {
			v.Add("is_active", httpx.FieldRequired, "cannot be null")
		} else {
			changes.IsActive = &active.Value
		}
	}
	v.OnlyFields(patch, patchable...)
	return changes
}

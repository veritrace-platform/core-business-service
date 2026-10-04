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
}

// temperature checks one bound of the temperature range.
func temperature(v *rest.Validator, field string, value *float64) bool {
	return v.Decimal(field, value, MinTemperatureCelsius, MaxTemperatureCelsius, TemperatureDecimals)
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
	np := NewProduct{GTIN: req.GTIN, Name: req.Name, Description: req.Description}
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
var patchable = []string{"name", "description", "min_temp_celsius", "max_temp_celsius", "is_active"}

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

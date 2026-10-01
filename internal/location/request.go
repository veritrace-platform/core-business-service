package location

import (
	"regexp"
	"strings"

	"github.com/veritrace-platform/core-business-service/internal/platform/httpx"
	"github.com/veritrace-platform/core-business-service/internal/rest"
)

// Field length limits, as the table checks them.
const (
	maxNameLength    = 255
	maxAddressLength = 500
	maxCityLength    = 100
)

var countryCodePattern = regexp.MustCompile(`^[A-Z]{2}$`)

const countryCodeFormat = "must be an ISO 3166-1 alpha-2 code such as VN"

// Request is a new location as requests carry it: the body of POST /api/v1/locations, and the headquarters
// of a tenant registration.
type Request struct {
	GLN                  string   `json:"gln"`
	Name                 string   `json:"name"`
	Address              string   `json:"address"`
	City                 string   `json:"city"`
	CountryCode          *string  `json:"country_code"`
	Latitude             *float64 `json:"latitude"`
	Longitude            *float64 `json:"longitude"`
	GeoFenceRadiusMeters *int     `json:"geo_fence_radius_meters"`
}

// Validate checks the request, recording errors in v under field names that start with prefix, such as
// "headquarters.". checkGLN checks a GLN that is present; the caller knows which company prefix it must
// start with, if any. The result is the location the request describes, with rounded coordinates; it is
// meaningful only when v holds no errors.
func (req *Request) Validate(v *rest.Validator, prefix string, checkGLN func(gln string) error) NewLocation {
	req.GLN = strings.TrimSpace(req.GLN)
	if req.GLN == "" {
		v.Add(prefix+"gln", httpx.FieldRequired, "is required")
	} else {
		v.Key(prefix+"gln", checkGLN(req.GLN))
	}
	v.Text(prefix+"name", &req.Name, 1, maxNameLength)
	v.Text(prefix+"address", &req.Address, 1, maxAddressLength)
	v.Text(prefix+"city", &req.City, 1, maxCityLength)
	nl := NewLocation{
		GLN: req.GLN, Name: req.Name, Address: req.Address, City: req.City,
		CountryCode: DefaultCountryCode, GeoFenceRadiusMeters: DefaultGeoFenceRadiusMeters,
	}
	if req.CountryCode != nil {
		nl.CountryCode = strings.TrimSpace(*req.CountryCode)
		v.Matches(prefix+"country_code", nl.CountryCode, countryCodePattern, countryCodeFormat)
	}
	if v.Float(prefix+"latitude", req.Latitude, -90, 90) {
		nl.Latitude = RoundCoordinate(*req.Latitude)
	}
	if v.Float(prefix+"longitude", req.Longitude, -180, 180) {
		nl.Longitude = RoundCoordinate(*req.Longitude)
	}
	if req.GeoFenceRadiusMeters != nil {
		nl.GeoFenceRadiusMeters = *req.GeoFenceRadiusMeters
		v.Int(prefix+"geo_fence_radius_meters", nl.GeoFenceRadiusMeters, MinGeoFenceRadiusMeters, MaxGeoFenceRadiusMeters)
	}
	return nl
}

// patchable lists the members of a location merge patch. The GLN identifies the location, and the
// headquarters flag is set at registration; neither changes.
var patchable = []string{
	"name", "address", "city", "country_code", "latitude", "longitude", "geo_fence_radius_meters", "is_active",
}

// readPatch checks a merge patch of a location, recording errors in v, and returns the changes it describes.
func readPatch(v *rest.Validator, patch rest.Patch) Patch {
	var changes Patch
	text := func(field string, maxLen int) *string {
		value := rest.PatchField[string](v, patch, field)
		switch {
		case !value.Set:
		case value.Null:
			v.Add(field, httpx.FieldRequired, "cannot be null")
		case v.Text(field, &value.Value, 1, maxLen):
			return &value.Value
		}
		return nil
	}
	changes.Name = text("name", maxNameLength)
	changes.Address = text("address", maxAddressLength)
	changes.City = text("city", maxCityLength)
	if country := rest.PatchField[string](v, patch, "country_code"); country.Set {
		code := strings.TrimSpace(country.Value)
		if country.Null {
			v.Add("country_code", httpx.FieldRequired, "cannot be null")
		} else if v.Matches("country_code", code, countryCodePattern, countryCodeFormat) {
			changes.CountryCode = &code
		}
	}
	coordinate := func(field string, limit float64) *float64 {
		value := rest.PatchField[float64](v, patch, field)
		switch {
		case !value.Set:
		case value.Null:
			v.Add(field, httpx.FieldRequired, "cannot be null")
		case v.Float(field, &value.Value, -limit, limit):
			rounded := RoundCoordinate(value.Value)
			return &rounded
		}
		return nil
	}
	changes.Latitude = coordinate("latitude", 90)
	changes.Longitude = coordinate("longitude", 180)
	if radius := rest.PatchField[int](v, patch, "geo_fence_radius_meters"); radius.Set {
		if radius.Null {
			v.Add("geo_fence_radius_meters", httpx.FieldRequired, "cannot be null")
		} else if v.Int("geo_fence_radius_meters", radius.Value, MinGeoFenceRadiusMeters, MaxGeoFenceRadiusMeters) {
			changes.GeoFenceRadiusMeters = &radius.Value
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

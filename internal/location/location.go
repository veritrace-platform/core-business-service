// Package location manages a tenant's physical locations, identified by GLN (data-model.md §3.2).
package location

import (
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
)

// Geo-fence radius limits and default, in meters (shipment-lifecycle.md §6.4).
const (
	MinGeoFenceRadiusMeters     = 50
	MaxGeoFenceRadiusMeters     = 5000
	DefaultGeoFenceRadiusMeters = 200
)

// DefaultCountryCode applies when a location gives no ISO 3166-1 alpha-2 country code.
const DefaultCountryCode = "VN"

// CoordinateDecimals is the precision of latitudes and longitudes, as stored in numeric(9, 6).
const CoordinateDecimals = 6

// RoundCoordinate rounds degrees to CoordinateDecimals decimals, the value the database keeps.
func RoundCoordinate(degrees float64) float64 {
	rounded, err := strconv.ParseFloat(strconv.FormatFloat(degrees, 'f', CoordinateDecimals, 64), 64)
	if err != nil {
		// A fixed-point decimal always parses.
		return degrees
	}
	return rounded
}

// Location is a warehouse, hub, or headquarters of one tenant.
type Location struct {
	ID                   uuid.UUID `json:"id"`
	GLN                  string    `json:"gln"`
	Name                 string    `json:"name"`
	Address              string    `json:"address"`
	City                 string    `json:"city"`
	CountryCode          string    `json:"country_code"`
	Latitude             float64   `json:"latitude"`
	Longitude            float64   `json:"longitude"`
	GeoFenceRadiusMeters int       `json:"geo_fence_radius_meters"`
	IsHeadquarters       bool      `json:"is_headquarters"`
	IsActive             bool      `json:"is_active"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

// NewLocation is a validated location to add to a tenant.
type NewLocation struct {
	GLN                  string
	Name                 string
	Address              string
	City                 string
	CountryCode          string
	Latitude             float64
	Longitude            float64
	GeoFenceRadiusMeters int
}

// Filter selects a page of locations, newest first.
type Filter struct {
	IsActive *bool
	// After is the last location of the previous page; uuid.Nil starts at the newest location.
	After uuid.UUID
	Limit int
}

// Patch lists the changes to a location; nil fields stay as they are. The GLN identifies the location and
// never changes.
type Patch struct {
	Name                 *string
	Address              *string
	City                 *string
	CountryCode          *string
	Latitude             *float64
	Longitude            *float64
	GeoFenceRadiusMeters *int
	IsActive             *bool
}

// Location errors.
var (
	// ErrNotFound reports a location that does not exist or belongs to another tenant.
	ErrNotFound = errors.New("location not found")
	// ErrGLNTaken reports a GLN that another location, of any tenant, already has.
	ErrGLNTaken = errors.New("GLN already registered")
)

// Package location models a tenant's physical locations, identified by GLN (data-model.md §3.2).
package location

import (
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
	rounded, err := strconv.ParseFloat(FormatCoordinate(degrees), 64)
	if err != nil {
		// FormatCoordinate always produces a parseable number.
		return degrees
	}
	return rounded
}

// FormatCoordinate renders degrees as a fixed-point decimal with CoordinateDecimals decimals.
func FormatCoordinate(degrees float64) string {
	return strconv.FormatFloat(degrees, 'f', CoordinateDecimals, 64)
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

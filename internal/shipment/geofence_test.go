package shipment

import (
	"math"
	"testing"
)

func TestDistanceMeters(t *testing.T) {
	// One degree along the equator is 2πR/360 for R = 6 371 000 m.
	if got, want := distanceMeters(0, 0, 0, 1), 2*math.Pi*earthRadiusMeters/360; math.Abs(got-want) > 0.001 {
		t.Errorf("one degree of longitude = %f m, want %f", got, want)
	}
	if got := distanceMeters(10.762622, 106.742801, 10.762622, 106.742801); got != 0 {
		t.Errorf("distance to itself = %f", got)
	}
	// Ho Chi Minh City to Hanoi, about 1 140 km as the crow flies.
	if got := distanceMeters(10.776889, 106.700806, 21.028511, 105.804817); got < 1_130_000 || got > 1_150_000 {
		t.Errorf("Ho Chi Minh City to Hanoi = %f m", got)
	}
}

func TestReach(t *testing.T) {
	dock := Facility{Latitude: 10.762622, Longitude: 106.742801, GeoFenceRadiusMeters: 200}
	// 0.001 degrees of latitude is about 111 m.
	tests := []struct {
		name           string
		position       Position
		allowed        float64
		inside         bool
		approxDistance float64
	}{
		{"at the dock", Position{Latitude: 10.762622, Longitude: 106.742801, AccuracyMeters: 5}, 205, true, 0},
		{"inside the radius", Position{Latitude: 10.764422, Longitude: 106.742801, AccuracyMeters: 12}, 212, true, 200.2},
		{"inside thanks to accuracy", Position{Latitude: 10.764522, Longitude: 106.742801, AccuracyMeters: 20}, 220, true, 211.3},
		{"accuracy counts up to 50 m", Position{Latitude: 10.765222, Longitude: 106.742801, AccuracyMeters: 500}, 250, false, 289.1},
	}
	for _, tt := range tests {
		distance, allowed, inside := dock.reach(tt.position)
		if inside != tt.inside || allowed != tt.allowed || math.Abs(distance-tt.approxDistance) > 0.2 {
			t.Errorf("%s: reach() = %.1f, %.1f, %v; want ≈%.1f, %.1f, %v", tt.name, distance, allowed, inside,
				tt.approxDistance, tt.allowed, tt.inside)
		}
	}
}

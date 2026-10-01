package calendar_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/veritrace-platform/core-business-service/internal/calendar"
)

func TestParse(t *testing.T) {
	d, err := calendar.Parse("2026-02-28")
	if err != nil || d != calendar.New(2026, time.February, 28) || d.String() != "2026-02-28" {
		t.Errorf("Parse() = %v, %v", d, err)
	}
	for _, s := range []string{"2026-02-30", "2026-2-3", "28/02/2026", "2026-02-28T00:00:00Z", ""} {
		if _, err := calendar.Parse(s); err == nil {
			t.Errorf("Parse(%q) succeeded", s)
		}
	}
}

func TestOrderAndJSON(t *testing.T) {
	produced, expires := calendar.New(2026, time.September, 30), calendar.New(2026, time.October, 14)
	if !produced.Before(expires) || expires.Before(produced) || produced.Before(produced) {
		t.Error("Before() orders days wrongly")
	}
	b, err := json.Marshal(map[string]calendar.Date{"expiration_date": expires})
	if err != nil || string(b) != `{"expiration_date":"2026-10-14"}` {
		t.Errorf("Marshal() = %s, %v", b, err)
	}
	var back map[string]calendar.Date
	if err := json.Unmarshal(b, &back); err != nil || back["expiration_date"] != expires {
		t.Errorf("Unmarshal() = %v, %v", back, err)
	}
	for _, bad := range []string{`{"d":"2026-13-01"}`, `{"d":20261014}`} {
		if err := json.Unmarshal([]byte(bad), &back); err == nil {
			t.Errorf("Unmarshal(%s) succeeded", bad)
		}
	}
}

func TestPostgresDate(t *testing.T) {
	d := calendar.New(2026, time.December, 31)
	v, err := d.DateValue()
	if err != nil || !v.Valid || v.Time.Format(time.DateOnly) != "2026-12-31" {
		t.Errorf("DateValue() = %+v, %v", v, err)
	}

	var got calendar.Date
	// pgx reads dates at midnight UTC; any time of day is dropped.
	if err := got.ScanDate(pgtype.Date{Time: time.Date(2026, 12, 31, 13, 0, 0, 0, time.UTC), Valid: true}); err != nil || got != d {
		t.Errorf("ScanDate() = %v, %v", got, err)
	}
	for _, v := range []pgtype.Date{{}, {InfinityModifier: pgtype.Infinity, Valid: true}} {
		if err := got.ScanDate(v); !errors.Is(err, calendar.ErrNotFinite) {
			t.Errorf("ScanDate(%+v) error = %v, want ErrNotFinite", v, err)
		}
	}
}

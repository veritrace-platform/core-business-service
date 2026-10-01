// Package calendar holds calendar dates, such as the production and expiration dates of a lot: a day without a
// time or a time zone (data-model.md §1), exchanged as YYYY-MM-DD (rest-api.md §1).
package calendar

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// Date is a calendar date. pgx reads and writes it as a PostgreSQL date.
type Date struct {
	day time.Time // midnight UTC
}

// ErrNotFinite reports a NULL or infinite PostgreSQL date, which no column of the schema holds.
var ErrNotFinite = errors.New("date is NULL or infinite")

// New returns the date of year, month, and day, which must name a valid day.
func New(year int, month time.Month, day int) Date {
	return Date{day: time.Date(year, month, day, 0, 0, 0, 0, time.UTC)}
}

// Parse reads a date written as YYYY-MM-DD.
func Parse(s string) (Date, error) {
	day, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return Date{}, fmt.Errorf("parse date: %w", err)
	}
	return Date{day: day}, nil
}

// String returns the date as YYYY-MM-DD.
func (d Date) String() string {
	return d.day.Format(time.DateOnly)
}

// Before reports whether d is an earlier day than other.
func (d Date) Before(other Date) bool {
	return d.day.Before(other.day)
}

// MarshalJSON writes the date as a YYYY-MM-DD string.
func (d Date) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.String())
}

// UnmarshalJSON reads a YYYY-MM-DD string.
func (d *Date) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("read date: %w", err)
	}
	parsed, err := Parse(s)
	if err != nil {
		return err
	}
	*d = parsed
	return nil
}

// ScanDate reads a PostgreSQL date.
func (d *Date) ScanDate(v pgtype.Date) error {
	if !v.Valid || v.InfinityModifier != pgtype.Finite {
		return ErrNotFinite
	}
	*d = New(v.Time.Year(), v.Time.Month(), v.Time.Day())
	return nil
}

// DateValue writes the date as a PostgreSQL date.
func (d Date) DateValue() (pgtype.Date, error) {
	return pgtype.Date{Time: d.day, Valid: true}, nil
}

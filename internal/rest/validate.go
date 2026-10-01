package rest

import (
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/veritrace-platform/core-business-service/internal/gs1"
	"github.com/veritrace-platform/core-business-service/internal/password"
	"github.com/veritrace-platform/core-business-service/internal/platform/httpx"
)

// Limits of contact fields.
const (
	MaxEmailLength = 254 // RFC 5321 path limit
	MinPhoneLength = 6
	MaxPhoneLength = 32
)

// phonePattern accepts digits and common separators, such as "+84 28 3822 1234" or "(028) 3822-1234".
var phonePattern = regexp.MustCompile(`^\+?[0-9(][0-9 ().-]*[0-9]$`)

// Validator collects field errors while a handler checks a request. Its methods report whether the field is
// valid, so callers can skip checks that depend on it.
type Validator struct {
	errs      []httpx.FieldError
	keyErrors int
}

// Add records a field error.
func (v *Validator) Add(field, code, message string) {
	v.errs = append(v.errs, httpx.FieldError{Field: field, Code: code, Message: message})
}

// Text trims *value and checks that it is present and has min to max characters.
func (v *Validator) Text(field string, value *string, minLen, maxLen int) bool {
	*value = strings.TrimSpace(*value)
	if *value == "" {
		v.Add(field, httpx.FieldRequired, "is required")
		return false
	}
	return v.length(field, *value, minLen, maxLen)
}

// OptionalText trims **value when present. An empty value becomes nil, which clears an optional field.
func (v *Validator) OptionalText(field string, value **string, minLen, maxLen int) bool {
	if *value == nil {
		return true
	}
	trimmed := strings.TrimSpace(**value)
	if trimmed == "" {
		*value = nil
		return true
	}
	*value = &trimmed
	return v.length(field, trimmed, minLen, maxLen)
}

func (v *Validator) length(field, value string, minLen, maxLen int) bool {
	switch n := utf8.RuneCountInString(value); {
	case n < minLen:
		v.Add(field, httpx.FieldTooShort, fmt.Sprintf("must be at least %d characters", minLen))
		return false
	case n > maxLen:
		v.Add(field, httpx.FieldTooLong, fmt.Sprintf("must be at most %d characters", maxLen))
		return false
	}
	return true
}

// Matches checks value against pattern; message describes the expected format.
func (v *Validator) Matches(field, value string, pattern *regexp.Regexp, message string) bool {
	if !pattern.MatchString(value) {
		v.Add(field, httpx.FieldInvalidFormat, message)
		return false
	}
	return true
}

// Email trims *value and checks that it is a plain email address.
func (v *Validator) Email(field string, value *string) bool {
	if !v.Text(field, value, 3, MaxEmailLength) {
		return false
	}
	if addr, err := mail.ParseAddress(*value); err != nil || addr.Address != *value || addr.Name != "" {
		v.Add(field, httpx.FieldInvalidFormat, "must be an email address")
		return false
	}
	return true
}

// Phone trims an optional phone number and checks its length and characters.
func (v *Validator) Phone(field string, value **string) bool {
	if !v.OptionalText(field, value, MinPhoneLength, MaxPhoneLength) {
		return false
	}
	return *value == nil || v.Matches(field, **value, phonePattern, "may contain only digits, spaces, and + ( ) . -")
}

// Password checks a new password against the password rules. Passwords are not trimmed.
func (v *Validator) Password(field, value string) bool {
	switch err := password.Validate(value); {
	case errors.Is(err, password.ErrTooShort):
		v.Add(field, httpx.FieldTooShort, err.Error())
		return false
	case errors.Is(err, password.ErrTooLong):
		v.Add(field, httpx.FieldTooLong, err.Error())
		return false
	}
	return true
}

// Float checks that *value is present and within [minValue, maxValue].
func (v *Validator) Float(field string, value *float64, minValue, maxValue float64) bool {
	if value == nil {
		v.Add(field, httpx.FieldRequired, "is required")
		return false
	}
	if *value < minValue || *value > maxValue {
		v.Add(field, httpx.FieldOutOfRange, fmt.Sprintf("must be between %g and %g", minValue, maxValue))
		return false
	}
	return true
}

// Int checks that value is within [minValue, maxValue].
func (v *Validator) Int(field string, value, minValue, maxValue int) bool {
	if value < minValue || value > maxValue {
		v.Add(field, httpx.FieldOutOfRange, fmt.Sprintf("must be between %d and %d", minValue, maxValue))
		return false
	}
	return true
}

// Key records the result of a GS1 key check; a nil err means the key is valid.
func (v *Validator) Key(field string, err error) bool {
	if err == nil {
		return true
	}
	var invalid *gs1.InvalidKeyError
	if !errors.As(err, &invalid) {
		v.Add(field, httpx.FieldInvalidFormat, err.Error())
		return false
	}
	v.Add(field, string(invalid.Reason), invalid.Message())
	v.keyErrors++
	return false
}

// Valid reports whether no field error was recorded.
func (v *Validator) Valid() bool {
	return len(v.errs) == 0
}

// Problem returns nil when the request is valid. Otherwise it is 422 INVALID_GS1_IDENTIFIER when every error
// concerns a GS1 key, and 400 VALIDATION_FAILED listing every error when any other field is invalid.
func (v *Validator) Problem() *httpx.Problem {
	if v.Valid() {
		return nil
	}
	if v.keyErrors == len(v.errs) {
		p := httpx.NewProblem(http.StatusUnprocessableEntity, CodeInvalidGS1Identifier, v.errs[0].Field+" "+v.errs[0].Message)
		p.Errors = v.errs
		return &p
	}
	p := httpx.ValidationProblem(v.errs)
	return &p
}

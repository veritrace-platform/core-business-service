package rest_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/veritrace-platform/core-business-service/internal/gs1"
	"github.com/veritrace-platform/core-business-service/internal/platform/httpx"
	"github.com/veritrace-platform/core-business-service/internal/rest"
)

func codes(p *httpx.Problem) map[string]string {
	out := map[string]string{}
	if p != nil {
		for _, e := range p.Errors {
			out[e.Field] = e.Code
		}
	}
	return out
}

func TestValidatorText(t *testing.T) {
	var v rest.Validator
	padded, blank, long := "  name  ", "   ", strings.Repeat("é", 11)
	if !v.Text("padded", &padded, 1, 10) || padded != "name" {
		t.Errorf("Text trimmed to %q", padded)
	}
	v.Text("blank", &blank, 1, 10)
	v.Text("long", &long, 1, 10)
	short := "ab"
	v.Text("short", &short, 3, 10)

	got := codes(v.Problem())
	want := map[string]string{"blank": httpx.FieldRequired, "long": httpx.FieldTooLong, "short": httpx.FieldTooShort}
	for field, code := range want {
		if got[field] != code {
			t.Errorf("%s: code = %q, want %q", field, got[field], code)
		}
	}
}

func TestValidatorOptionalText(t *testing.T) {
	var v rest.Validator
	var missing *string
	empty, spaced := "  ", " +84 90 "
	emptyPtr, spacedPtr := &empty, &spaced
	v.OptionalText("missing", &missing, 1, 10)
	v.OptionalText("empty", &emptyPtr, 1, 10)
	v.OptionalText("spaced", &spacedPtr, 1, 10)
	if missing != nil || emptyPtr != nil || spacedPtr == nil || *spacedPtr != "+84 90" || !v.Valid() {
		t.Errorf("got %v, %v, %v; valid = %t", missing, emptyPtr, spacedPtr, v.Valid())
	}
}

func TestValidatorFormats(t *testing.T) {
	var v rest.Validator
	emails := map[string]string{
		"ok@example.com": "", "An <an@example.com>": httpx.FieldInvalidFormat, "no-at": httpx.FieldInvalidFormat,
		"": httpx.FieldRequired,
	}
	for email, want := range emails {
		e := email
		v = rest.Validator{}
		v.Email("email", &e)
		if got := codes(v.Problem())["email"]; got != want {
			t.Errorf("Email(%q) = %q, want %q", email, got, want)
		}
	}

	phones := map[string]string{
		"+84 28 3822 1234": "", "(028) 3822-1234": "", "0901234567": "",
		"call me": httpx.FieldInvalidFormat, "12345": httpx.FieldTooShort, "+84-": httpx.FieldTooShort,
	}
	for phone, want := range phones {
		p := phone
		pp := &p
		v = rest.Validator{}
		v.Phone("phone", &pp)
		if got := codes(v.Problem())["phone"]; got != want {
			t.Errorf("Phone(%q) = %q, want %q", phone, got, want)
		}
	}

	v = rest.Validator{}
	v.Password("short", "too short")
	v.Password("long", strings.Repeat("p", 129))
	v.Password("ok", "  twelve chars  ")
	v.Matches("pattern", "abc", regexp.MustCompile(`^[0-9]+$`), "must be digits")
	got := codes(v.Problem())
	if got["short"] != httpx.FieldTooShort || got["long"] != httpx.FieldTooLong || got["ok"] != "" ||
		got["pattern"] != httpx.FieldInvalidFormat {
		t.Errorf("codes = %v", got)
	}
}

func TestValidatorNumbers(t *testing.T) {
	var v rest.Validator
	inside, outside := 10.0, 91.0
	v.Float("inside", &inside, -90, 90)
	v.Float("outside", &outside, -90, 90)
	v.Float("missing", nil, -90, 90)
	v.Int("radius", 49, 50, 5000)
	got := codes(v.Problem())
	if got["inside"] != "" || got["outside"] != httpx.FieldOutOfRange || got["missing"] != httpx.FieldRequired ||
		got["radius"] != httpx.FieldOutOfRange {
		t.Errorf("codes = %v", got)
	}
}

func TestValidatorDecimal(t *testing.T) {
	tests := []struct {
		value float64
		code  string
	}{
		{2.5, ""},
		{-18, ""},
		{2.15, ""},
		{-0.05, ""},
		{8.125, httpx.FieldInvalidFormat},
		{0.001, httpx.FieldInvalidFormat},
		{80.01, httpx.FieldOutOfRange},
	}
	for _, tt := range tests {
		var v rest.Validator
		ok := v.Decimal("t", &tt.value, -50, 80, 2)
		if got := codes(v.Problem())["t"]; got != tt.code || ok != (tt.code == "") {
			t.Errorf("Decimal(%v) = %v with code %q, want code %q", tt.value, ok, got, tt.code)
		}
	}
	var v rest.Validator
	if v.Decimal("t", nil, -50, 80, 2) || codes(v.Problem())["t"] != httpx.FieldRequired {
		t.Error("a missing value was accepted")
	}
}

func TestValidatorProblemKinds(t *testing.T) {
	var v rest.Validator
	if v.Problem() != nil || !v.Valid() {
		t.Fatal("an empty validator reported a problem")
	}

	v.Key("gln", gs1.Validate(gs1.GLN, "8930001001016"))
	v.Key("gcp", gs1.ValidateCompanyPrefix("89300"))
	p := v.Problem()
	if p.Status != http.StatusUnprocessableEntity || p.Code != rest.CodeInvalidGS1Identifier ||
		codes(p)["gln"] != "CHECK_DIGIT" || codes(p)["gcp"] != "LENGTH" {
		t.Errorf("GS1-only problem = %+v", p)
	}

	v.Key("other", errors.New("not a gs1 error"))
	p = v.Problem()
	if p.Status != http.StatusBadRequest || p.Code != httpx.CodeValidationFailed || len(p.Errors) != 3 {
		t.Errorf("mixed problem = %+v, want 400 listing every error", p)
	}
	if !v.Key("fine", nil) {
		t.Error("Key(nil) reported an error")
	}
}

func TestInternalErrorLogsAndHidesDetails(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	rec := httptest.NewRecorder()
	rest.InternalError(rec, httptest.NewRequest(http.MethodGet, "/api/v1/x", nil), logger, errors.New("disk on fire"))

	var p httpx.Problem
	_ = json.NewDecoder(rec.Body).Decode(&p)
	if rec.Code != http.StatusInternalServerError || strings.Contains(p.Detail, "disk") {
		t.Errorf("status = %d, detail = %q; want 500 without the cause", rec.Code, p.Detail)
	}
	if !strings.Contains(logs.String(), "disk on fire") {
		t.Errorf("log = %s, want the cause", logs.String())
	}
}

func TestInternalErrorAfterTheClientLeft(t *testing.T) {
	var logs bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/x", nil)
	rest.InternalError(rec, req, slog.New(slog.NewJSONHandler(&logs, nil)), context.Canceled)

	if rec.Code != http.StatusServiceUnavailable || logs.Len() != 0 {
		t.Errorf("status = %d, logs = %q; want 503 and no error log", rec.Code, logs.String())
	}
}

func TestConflict(t *testing.T) {
	rec := httptest.NewRecorder()
	rest.Conflict(rec, httptest.NewRequest(http.MethodPost, "/api/v1/x", nil), "admin.email", "is already registered")
	var p httpx.Problem
	_ = json.NewDecoder(rec.Body).Decode(&p)
	if rec.Code != http.StatusConflict || p.Code != rest.CodeIdentifierAlreadyRegistered || len(p.Errors) != 1 ||
		p.Errors[0].Field != "admin.email" || p.Errors[0].Code != rest.FieldAlreadyRegistered {
		t.Errorf("status = %d, problem = %+v", rec.Code, p)
	}
}

func TestValidatorUUIDAndDate(t *testing.T) {
	var v rest.Validator
	id, idOK := v.UUID("product_id", "0192f7a4-7c3e-7d2a-9b1e-3f4a5b6c7d8e")
	day, dayOK := v.Date("expiration_date", "2026-12-31")
	if !idOK || id.String() != "0192f7a4-7c3e-7d2a-9b1e-3f4a5b6c7d8e" || !dayOK || day.String() != "2026-12-31" || !v.Valid() {
		t.Errorf("valid values: %s %v, %s %v; errors %v", id, idOK, day, dayOK, codes(v.Problem()))
	}

	v = rest.Validator{}
	v.UUID("missing_id", "")
	v.UUID("bad_id", "42")
	v.Date("missing_date", "")
	v.Date("bad_date", "31/12/2026")
	got := codes(v.Problem())
	want := map[string]string{
		"missing_id": httpx.FieldRequired, "bad_id": httpx.FieldInvalidFormat,
		"missing_date": httpx.FieldRequired, "bad_date": httpx.FieldInvalidFormat,
	}
	for field, code := range want {
		if got[field] != code {
			t.Errorf("%s: %q, want %q", field, got[field], code)
		}
	}
}

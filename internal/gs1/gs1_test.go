package gs1_test

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/veritrace-platform/core-business-service/internal/gs1"
)

// vectors mirrors testdata/gs1-check-digit.json, a copy of veritrace/docs/contracts/test-vectors.
type vectors struct {
	Valid           []vector `json:"valid"`
	Invalid         []vector `json:"invalid"`
	PrefixOwnership struct {
		Valid   []vector `json:"valid"`
		Invalid []vector `json:"invalid"`
	} `json:"prefix_ownership"`
}

type vector struct {
	Key    string `json:"key"`
	Type   string `json:"type"`
	GCP    string `json:"gcp"`
	Reason string `json:"reason"`
}

func loadVectors(t *testing.T) vectors {
	t.Helper()
	raw, err := os.ReadFile("testdata/gs1-check-digit.json")
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}
	var v vectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode vectors: %v", err)
	}
	return v
}

// kinds maps vector types to key types. GTIN-13 exercises only the check digit.
var kinds = map[string]gs1.Kind{"GLN": gs1.GLN, "GTIN-14": gs1.GTIN14, "SSCC": gs1.SSCC}

func TestCheckDigitVectors(t *testing.T) {
	for _, v := range loadVectors(t).Valid {
		payload, want := v.Key[:len(v.Key)-1], int(v.Key[len(v.Key)-1]-'0')
		if got := gs1.CheckDigit(payload); got != want {
			t.Errorf("CheckDigit(%s) = %d, want %d", payload, got, want)
		}
		if kind, ok := kinds[v.Type]; ok {
			if err := gs1.Validate(kind, v.Key); err != nil {
				t.Errorf("Validate(%s, %s) error = %v", kind, v.Key, err)
			}
		}
	}
}

func TestInvalidKeyVectors(t *testing.T) {
	for _, v := range loadVectors(t).Invalid {
		assertReason(t, gs1.Validate(kinds[v.Type], v.Key), v)
	}
}

func TestPrefixOwnershipVectors(t *testing.T) {
	validate := map[string]func(key, gcp string) error{"GLN": gs1.ValidateGLN, "GTIN-14": gs1.ValidateGTIN14}
	v := loadVectors(t)
	for _, vec := range v.PrefixOwnership.Valid {
		if err := validate[vec.Type](vec.Key, vec.GCP); err != nil {
			t.Errorf("%s %s with prefix %s: error = %v", vec.Type, vec.Key, vec.GCP, err)
		}
	}
	for _, vec := range v.PrefixOwnership.Invalid {
		assertReason(t, validate[vec.Type](vec.Key, vec.GCP), vec)
	}
}

func assertReason(t *testing.T, err error, v vector) {
	t.Helper()
	var invalid *gs1.InvalidKeyError
	if !errors.As(err, &invalid) {
		t.Errorf("%s %q: error = %v, want %s", v.Type, v.Key, err, v.Reason)
		return
	}
	if string(invalid.Reason) != v.Reason || invalid.Kind != kinds[v.Type] {
		t.Errorf("%s %q: got %s %s, want %s", v.Type, v.Key, invalid.Kind, invalid.Reason, v.Reason)
	}
	if invalid.Message() == "" || !strings.Contains(invalid.Error(), invalid.Message()) {
		t.Errorf("%s %q: message %q not part of error %q", v.Type, v.Key, invalid.Message(), invalid.Error())
	}
}

func TestInvalidKeyMessages(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{gs1.Validate(gs1.GLN, "893"), "must be 13 digits"},
		{gs1.Validate(gs1.SSCC, "08930001000000001X"), "must contain only digits"},
		{gs1.Validate(gs1.GTIN14, "08930001000015"), "expected check digit 8"},
		{gs1.ValidateGLN("8934567000017", "8930001"), "must start with the company prefix 8930001"},
	}
	for _, tt := range tests {
		var invalid *gs1.InvalidKeyError
		if !errors.As(tt.err, &invalid) || invalid.Message() != tt.want {
			t.Errorf("error = %v, want message %q", tt.err, tt.want)
		}
	}
}

func TestValidateRejectsUnknownKind(t *testing.T) {
	if err := gs1.Validate("GRAI", "123"); err == nil {
		t.Error("Validate() accepted an unknown key type")
	}
}

func TestValidateCompanyPrefix(t *testing.T) {
	for gcp, valid := range map[string]bool{
		"893000": true, "8930001": true, "8930001234": true,
		"89300": false, "89300012345": false, "89300A1": false, "": false,
	} {
		err := gs1.ValidateCompanyPrefix(gcp)
		if valid != (err == nil) || (err != nil && !errors.Is(err, gs1.ErrInvalidCompanyPrefix)) {
			t.Errorf("ValidateCompanyPrefix(%q) error = %v, want valid = %t", gcp, err, valid)
		}
	}
}

func TestPrefixesOverlap(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"8930001", "8930001", true},
		{"893000", "8930001", true},
		{"8930001", "893000", true},
		{"8930001", "8930002", false},
		{"8934567", "893456", true},
	}
	for _, tt := range tests {
		if got := gs1.PrefixesOverlap(tt.a, tt.b); got != tt.want {
			t.Errorf("PrefixesOverlap(%s, %s) = %t, want %t", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestValidateLotNumber(t *testing.T) {
	for lot, valid := range map[string]bool{
		"L2026-09.A_1": true, "1": true, strings.Repeat("9", 20): true,
		"": false, strings.Repeat("9", 21): false, "LOT 1": false, "LOT/1": false, "lô1": false,
	} {
		if err := gs1.ValidateLotNumber(lot); valid != (err == nil) {
			t.Errorf("ValidateLotNumber(%q) error = %v, want valid = %t", lot, err, valid)
		}
	}
}

func TestBuildSSCC(t *testing.T) {
	tests := []struct {
		extension int
		gcp       string
		serial    int64
		want      string
		wantErr   error
	}{
		{0, "8930001", 1, "089300010000000018", nil},
		{0, "8934567", 1, "089345670000000017", nil},
		{0, "8930001", 999_999_999, "", nil},
		{3, "8930001234", 42, "", nil},
		{0, "8930001", 1_000_000_000, "", gs1.ErrSerialSpaceExhausted},
		{0, "8930001234", 1_000_000, "", gs1.ErrSerialSpaceExhausted},
		{0, "893", 1, "", gs1.ErrInvalidCompanyPrefix},
	}
	for _, tt := range tests {
		got, err := gs1.BuildSSCC(tt.extension, tt.gcp, tt.serial)
		if tt.wantErr != nil {
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("BuildSSCC(%d, %s, %d) error = %v, want %v", tt.extension, tt.gcp, tt.serial, err, tt.wantErr)
			}
			continue
		}
		if err != nil {
			t.Fatalf("BuildSSCC(%d, %s, %d) error = %v", tt.extension, tt.gcp, tt.serial, err)
		}
		if tt.want != "" && got != tt.want {
			t.Errorf("BuildSSCC(%d, %s, %d) = %s, want %s", tt.extension, tt.gcp, tt.serial, got, tt.want)
		}
		if err := gs1.ValidateSSCC(got); err != nil || got[0] != byte('0'+tt.extension) || got[1:1+len(tt.gcp)] != tt.gcp {
			t.Errorf("BuildSSCC(%d, %s, %d) = %s: not a valid SSCC for the prefix (%v)", tt.extension, tt.gcp, tt.serial, got, err)
		}
	}
	if _, err := gs1.BuildSSCC(10, "8930001", 1); err == nil {
		t.Error("BuildSSCC() accepted a two-digit extension digit")
	}
}

func TestSerialSpace(t *testing.T) {
	if got := gs1.SerialSpace("893000"); got != 10_000_000_000 {
		t.Errorf("SerialSpace(6 digits) = %d, want 1e10", got)
	}
	if got := gs1.SerialSpace("8930001234"); got != 1_000_000 {
		t.Errorf("SerialSpace(10 digits) = %d, want 1e6", got)
	}
}

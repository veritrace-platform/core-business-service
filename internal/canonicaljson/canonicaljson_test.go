package canonicaljson_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"strconv"
	"testing"

	"github.com/veritrace-platform/core-business-service/internal/canonicaljson"
)

// vectors is testdata/canonical-json.json, a copy of veritrace/docs/contracts/test-vectors/canonical-json.json.
type vectors struct {
	Numbers []struct {
		IEEE754   string `json:"ieee754"`
		Canonical string `json:"canonical"`
	} `json:"numbers"`
	InvalidNumbers []struct {
		IEEE754 string `json:"ieee754"`
	} `json:"invalid_numbers"`
	Documents []struct {
		Name      string `json:"name"`
		Input     string `json:"input"`
		Canonical string `json:"canonical"`
		SHA256    string `json:"sha256"`
	} `json:"documents"`
}

func load(t *testing.T) vectors {
	t.Helper()
	b, err := os.ReadFile("testdata/canonical-json.json")
	if err != nil {
		t.Fatal(err)
	}
	var v vectors
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func float(t *testing.T, bits string) float64 {
	t.Helper()
	u, err := strconv.ParseUint(bits, 16, 64)
	if err != nil {
		t.Fatal(err)
	}
	return math.Float64frombits(u)
}

func TestNumbers(t *testing.T) {
	v := load(t)
	for _, n := range v.Numbers {
		if got, err := canonicaljson.FormatNumber(float(t, n.IEEE754)); err != nil || got != n.Canonical {
			t.Errorf("FormatNumber(%s) = %q, %v; want %q", n.IEEE754, got, err, n.Canonical)
		}
	}
	for _, n := range v.InvalidNumbers {
		if _, err := canonicaljson.FormatNumber(float(t, n.IEEE754)); !errors.Is(err, canonicaljson.ErrNotFinite) {
			t.Errorf("FormatNumber(%s) error = %v, want ErrNotFinite", n.IEEE754, err)
		}
	}
}

func TestDocuments(t *testing.T) {
	for _, d := range load(t).Documents {
		got, err := canonicaljson.Transform([]byte(d.Input))
		if err != nil || string(got) != d.Canonical {
			t.Errorf("%s: Transform() = %s, %v; want %s", d.Name, got, err, d.Canonical)
			continue
		}
		if sum := sha256.Sum256(got); hex.EncodeToString(sum[:]) != d.SHA256 {
			t.Errorf("%s: SHA-256 = %x, want %s", d.Name, sum, d.SHA256)
		}
	}
}

func TestMarshal(t *testing.T) {
	type position struct {
		Longitude float64 `json:"longitude"`
		Latitude  float64 `json:"latitude"`
	}
	got, err := canonicaljson.Marshal(map[string]any{"z": position{106.742801, 10.762622}, "a": "<b>&", "n": nil})
	if err != nil || string(got) != `{"a":"<b>&","n":null,"z":{"latitude":10.762622,"longitude":106.742801}}` {
		t.Errorf("Marshal() = %s, %v", got, err)
	}
	if _, err := canonicaljson.Marshal(math.Inf(1)); err == nil {
		t.Error("an infinite number was marshaled")
	}
	for _, bad := range []string{`{"a":1}{}`, `{"a":`, `1e400`} {
		if _, err := canonicaljson.Transform([]byte(bad)); err == nil {
			t.Errorf("Transform(%s) succeeded", bad)
		}
	}
}

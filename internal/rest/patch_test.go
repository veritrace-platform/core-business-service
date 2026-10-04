package rest_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/veritrace-platform/core-business-service/internal/platform/httpx"
	"github.com/veritrace-platform/core-business-service/internal/rest"
)

func decodePatch(t *testing.T, body string) (rest.Patch, *httpx.Problem) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/users/x", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/merge-patch+json")
	var p rest.Patch
	return p, httpx.DecodeJSON(httptest.NewRecorder(), req, &p)
}

func TestPatchFields(t *testing.T) {
	patch, problem := decodePatch(t, `{"name":"An","phone":null,"is_active":"yes","email":"a@b.example","zone":1}`)
	if problem != nil {
		t.Fatalf("DecodeJSON() = %+v", problem)
	}
	var v rest.Validator
	name := rest.PatchField[string](&v, patch, "name")
	phone := rest.PatchField[*string](&v, patch, "phone")
	active := rest.PatchField[bool](&v, patch, "is_active")
	role := rest.PatchField[string](&v, patch, "role")
	v.OnlyFields(patch, "name", "phone", "is_active", "role")

	if !name.Set || name.Null || name.Value != "An" {
		t.Errorf("name = %+v, want set to An", name)
	}
	if !phone.Set || !phone.Null {
		t.Errorf("phone = %+v, want set to null", phone)
	}
	if role.Set {
		t.Errorf("role = %+v, want absent", role)
	}
	if active.Set {
		t.Errorf("is_active = %+v, want skipped after its type error", active)
	}
	got := codes(v.Problem())
	want := map[string]string{"is_active": httpx.FieldInvalidType, "email": httpx.FieldUnknown, "zone": httpx.FieldUnknown}
	if len(got) != len(want) {
		t.Errorf("errors = %v, want %v", got, want)
	}
	for field, code := range want {
		if got[field] != code {
			t.Errorf("%s: %q, want %q", field, got[field], code)
		}
	}

	if _, problem := decodePatch(t, `[1]`); problem == nil {
		t.Error("an array patch was accepted")
	}
}

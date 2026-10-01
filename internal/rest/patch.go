package rest

import (
	"bytes"
	"encoding/json"
	"slices"
	"sort"

	"github.com/veritrace-platform/core-business-service/internal/platform/httpx"
)

// Patch is a decoded JSON merge patch (RFC 7396): the raw value of every member present.
type Patch map[string]json.RawMessage

// Optional is one field of a merge patch: Set reports that the field was present, and Null that it was null,
// which clears an optional field.
type Optional[T any] struct {
	Set   bool
	Null  bool
	Value T
}

// PatchField reads one member of patch. A member of the wrong JSON type is recorded in v.
func PatchField[T any](v *Validator, patch Patch, field string) Optional[T] {
	raw, ok := patch[field]
	if !ok {
		return Optional[T]{}
	}
	o := Optional[T]{Set: true}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		o.Null = true
		return o
	}
	if err := json.Unmarshal(raw, &o.Value); err != nil {
		v.Add(field, httpx.FieldInvalidType, "has the wrong JSON type")
	}
	return o
}

// OnlyFields records UNKNOWN_FIELD for every member of patch that is not one of the patchable fields.
func (v *Validator) OnlyFields(patch Patch, patchable ...string) {
	members := make([]string, 0, len(patch))
	for member := range patch {
		members = append(members, member)
	}
	sort.Strings(members)
	for _, member := range members {
		if !slices.Contains(patchable, member) {
			v.Add(member, httpx.FieldUnknown, "cannot be changed")
		}
	}
}

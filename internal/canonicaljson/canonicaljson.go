// Package canonicaljson writes JSON in the JSON Canonicalization Scheme of RFC 8785, the form in which shipment
// events are hashed (ADR-0006): object members sorted by the UTF-16 code units of their names, no insignificant
// whitespace, ECMAScript number serialization, and minimal string escaping.
package canonicaljson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
)

// ErrNotFinite reports a NaN or infinite number, which JSON cannot represent.
var ErrNotFinite = errors.New("number is not finite")

// Marshal returns the canonical JSON of v, which encoding/json must be able to marshal.
func Marshal(v any) ([]byte, error) {
	text, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	return Transform(text)
}

// Transform returns the canonical form of one JSON value. Names must be unique within each object (I-JSON,
// RFC 7493); of duplicated names the last one is kept.
func Transform(text []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(text))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("parse JSON: data after the value")
	}
	var buf bytes.Buffer
	if err := write(&buf, value); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func write(buf *bytes.Buffer, value any) error {
	switch v := value.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		buf.WriteString(strconv.FormatBool(v))
	case json.Number:
		f, err := strconv.ParseFloat(string(v), 64)
		if err != nil {
			return fmt.Errorf("number %s: %w", v, err)
		}
		n, err := FormatNumber(f)
		if err != nil {
			return err
		}
		buf.WriteString(n)
	case string:
		writeString(buf, v)
	case []any:
		buf.WriteByte('[')
		for i, element := range v {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := write(buf, element); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case map[string]any:
		return writeObject(buf, v)
	default:
		return fmt.Errorf("unexpected JSON value of type %T", value)
	}
	return nil
}

func writeObject(buf *bytes.Buffer, object map[string]any) error {
	type member struct {
		name  string
		units []uint16
	}
	members := make([]member, 0, len(object))
	for name := range object {
		members = append(members, member{name: name, units: utf16.Encode([]rune(name))})
	}
	slices.SortFunc(members, func(a, b member) int { return slices.Compare(a.units, b.units) })

	buf.WriteByte('{')
	for i, m := range members {
		if i > 0 {
			buf.WriteByte(',')
		}
		writeString(buf, m.name)
		buf.WriteByte(':')
		if err := write(buf, object[m.name]); err != nil {
			return err
		}
	}
	buf.WriteByte('}')
	return nil
}

// writeString escapes only what JSON requires, as ECMAScript's JSON.stringify does: quotation marks,
// backslashes, and control characters, the common ones in short form.
func writeString(buf *bytes.Buffer, s string) {
	buf.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			buf.WriteString(`\"`)
		case '\\':
			buf.WriteString(`\\`)
		case '\b':
			buf.WriteString(`\b`)
		case '\f':
			buf.WriteString(`\f`)
		case '\n':
			buf.WriteString(`\n`)
		case '\r':
			buf.WriteString(`\r`)
		case '\t':
			buf.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(buf, `\u%04x`, r)
			} else {
				buf.WriteRune(r)
			}
		}
	}
	buf.WriteByte('"')
}

// FormatNumber writes f as ECMAScript's Number.prototype.toString does: the shortest digits that read back as
// f, in plain notation from 1e-6 up to 1e21 and in exponent notation otherwise. Negative zero is written as 0.
func FormatNumber(f float64) (string, error) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "", ErrNotFinite
	}
	if f == 0 {
		return "0", nil
	}
	if abs := math.Abs(f); abs >= 1e-6 && abs < 1e21 {
		return strconv.FormatFloat(f, 'f', -1, 64), nil
	}
	// Go writes the exponent with at least two digits (1e-07); ECMAScript writes it without padding (1e-7).
	mantissa, exponent, _ := strings.Cut(strconv.FormatFloat(f, 'e', -1, 64), "e")
	return mantissa + "e" + exponent[:1] + strings.TrimLeft(exponent[1:], "0"), nil
}

// Package gs1 validates GS1 keys and builds SSCCs, following veritrace/docs/domain/gs1-identifiers.md.
//
// A key is checked for length, digits, check digit, and prefix ownership, in that order; the first failure is
// reported. The same order is used by the frontends, and the shared test vectors pin it.
package gs1

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Kind is a GS1 key type.
type Kind string

// Key types in use.
const (
	GLN    Kind = "GLN"
	GTIN14 Kind = "GTIN-14"
	SSCC   Kind = "SSCC"
)

// lengths of each key type, check digit included.
var lengths = map[Kind]int{GLN: 13, GTIN14: 14, SSCC: 18}

// Reason is why a key is invalid. The values are the field error codes of INVALID_GS1_IDENTIFIER problems.
type Reason string

// Reasons, in the order they are checked.
const (
	ReasonLength         Reason = "LENGTH"
	ReasonNonNumeric     Reason = "NON_NUMERIC"
	ReasonCheckDigit     Reason = "CHECK_DIGIT"
	ReasonPrefixMismatch Reason = "PREFIX_MISMATCH"
)

// InvalidKeyError reports an invalid key and why.
type InvalidKeyError struct {
	Kind   Kind
	Reason Reason
	msg    string
}

func (e *InvalidKeyError) Error() string {
	return fmt.Sprintf("invalid %s: %s", e.Kind, e.msg)
}

// Message describes the problem for an API client, without repeating the key type.
func (e *InvalidKeyError) Message() string {
	return e.msg
}

func invalid(kind Kind, reason Reason, format string, args ...any) *InvalidKeyError {
	return &InvalidKeyError{Kind: kind, Reason: reason, msg: fmt.Sprintf(format, args...)}
}

// CheckDigit returns the GS1 Modulo 10 check digit of payload, a key without its check digit. Weights 3 and 1
// alternate from the rightmost digit. payload must contain only ASCII digits.
func CheckDigit(payload string) int {
	sum := 0
	weight := 3
	for i := len(payload) - 1; i >= 0; i-- {
		sum += int(payload[i]-'0') * weight
		weight = 4 - weight
	}
	return (10 - sum%10) % 10
}

// Validate checks a key's length, digits, and check digit.
func Validate(kind Kind, key string) error {
	length, ok := lengths[kind]
	if !ok {
		return fmt.Errorf("unknown GS1 key type %q", kind)
	}
	if len(key) != length {
		return invalid(kind, ReasonLength, "must be %d digits", length)
	}
	if !isDigits(key) {
		return invalid(kind, ReasonNonNumeric, "must contain only digits")
	}
	if want := CheckDigit(key[:length-1]); int(key[length-1]-'0') != want {
		return invalid(kind, ReasonCheckDigit, "expected check digit %d", want)
	}
	return nil
}

// ValidateGLN checks a GLN and that it starts with the company prefix gcp.
func ValidateGLN(gln, gcp string) error {
	if err := Validate(GLN, gln); err != nil {
		return err
	}
	if !strings.HasPrefix(gln, gcp) {
		return invalid(GLN, ReasonPrefixMismatch, "must start with the company prefix %s", gcp)
	}
	return nil
}

// ValidateGTIN14 checks a GTIN-14 and that the company prefix gcp follows its indicator digit.
func ValidateGTIN14(gtin, gcp string) error {
	if err := Validate(GTIN14, gtin); err != nil {
		return err
	}
	if !strings.HasPrefix(gtin[1:], gcp) {
		return invalid(GTIN14, ReasonPrefixMismatch, "must contain the company prefix %s after the indicator digit", gcp)
	}
	return nil
}

// ValidateSSCC checks an SSCC.
func ValidateSSCC(sscc string) error {
	return Validate(SSCC, sscc)
}

// ErrInvalidCompanyPrefix reports a GS1 company prefix that is not 6 to 10 digits.
var ErrInvalidCompanyPrefix = errors.New("company prefix must be 6 to 10 digits")

// ValidateCompanyPrefix checks the format of a GS1 company prefix (GCP). GS1 member organizations assign
// prefixes; VeriTrace does not verify the assignment.
func ValidateCompanyPrefix(gcp string) error {
	if len(gcp) < 6 || len(gcp) > 10 || !isDigits(gcp) {
		return ErrInvalidCompanyPrefix
	}
	return nil
}

// PrefixesOverlap reports whether one company prefix starts with the other. GS1 assigns prefixes so that none
// extends another; overlapping prefixes would let two companies claim the same keys.
func PrefixesOverlap(a, b string) bool {
	return strings.HasPrefix(a, b) || strings.HasPrefix(b, a)
}

var lotNumberPattern = regexp.MustCompile(`^[0-9A-Za-z._-]{1,20}$`)

// ErrInvalidLotNumber reports a lot number outside the URL-safe subset of GS1 AI 10.
var ErrInvalidLotNumber = errors.New("lot number must be 1 to 20 characters from 0-9, A-Z, a-z, '.', '_', and '-'")

// ValidateLotNumber checks a lot number (AI 10).
func ValidateLotNumber(lot string) error {
	if !lotNumberPattern.MatchString(lot) {
		return ErrInvalidLotNumber
	}
	return nil
}

// ErrSerialSpaceExhausted reports that a company prefix has no serial references left for an extension digit.
var ErrSerialSpaceExhausted = errors.New("SSCC serial space exhausted")

// BuildSSCC returns the SSCC for a tenant's extension digit, company prefix, and serial number:
// extension digit, prefix, serial reference zero-padded to 16 - len(gcp) digits, and check digit.
func BuildSSCC(extensionDigit int, gcp string, serial int64) (string, error) {
	if extensionDigit < 0 || extensionDigit > 9 {
		return "", fmt.Errorf("extension digit %d is not a single digit", extensionDigit)
	}
	if err := ValidateCompanyPrefix(gcp); err != nil {
		return "", err
	}
	width := 16 - len(gcp)
	if serial < 0 || serial >= SerialSpace(gcp) {
		return "", fmt.Errorf("serial %d for prefix %s: %w", serial, gcp, ErrSerialSpaceExhausted)
	}
	payload := strconv.Itoa(extensionDigit) + gcp + fmt.Sprintf("%0*d", width, serial)
	return payload + strconv.Itoa(CheckDigit(payload)), nil
}

// SerialSpace is the number of serial references available to a company prefix: 10^(16 - len(gcp)).
func SerialSpace(gcp string) int64 {
	space := int64(1)
	for range 16 - len(gcp) {
		space *= 10
	}
	return space
}

func isDigits(s string) bool {
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

package tenant

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/veritrace-platform/core-business-service/internal/gs1"
	"github.com/veritrace-platform/core-business-service/internal/identity"
	"github.com/veritrace-platform/core-business-service/internal/location"
	"github.com/veritrace-platform/core-business-service/internal/platform/httpx"
	"github.com/veritrace-platform/core-business-service/internal/rest"
)

// Registerer registers tenants.
type Registerer interface {
	Register(ctx context.Context, r Registration) (Registered, error)
}

// ProfileService reads and changes the caller's tenant.
type ProfileService interface {
	Profile(ctx context.Context, p identity.Principal) (Tenant, error)
	UpdateProfile(ctx context.Context, p identity.Principal, patch ProfilePatch) (Tenant, error)
}

// Handler serves the tenant endpoints.
type Handler struct {
	registerer Registerer
	profiles   ProfileService
	// throttle limits registrations per client address (rest-api.md §1).
	throttle func(http.Handler) http.Handler
	// authenticate requires a valid access token.
	authenticate func(http.Handler) http.Handler
	logger       *slog.Logger
}

// NewHandler returns a Handler. throttle wraps the public registration endpoint, and authenticate the profile.
func NewHandler(registerer Registerer, profiles ProfileService, throttle, authenticate func(http.Handler) http.Handler,
	logger *slog.Logger,
) *Handler {
	return &Handler{registerer: registerer, profiles: profiles, throttle: throttle, authenticate: authenticate, logger: logger}
}

// Routes registers the tenant endpoints under /api/v1.
func (h *Handler) Routes(r chi.Router) {
	r.With(h.throttle).Post("/tenants", h.register)
	r.Group(func(r chi.Router) {
		r.Use(h.authenticate)
		r.Get("/tenant", h.profile)
		r.Patch("/tenant", h.updateProfile)
	})
}

type registrationRequest struct {
	Tenant struct {
		Code             string `json:"code"`
		LegalName        string `json:"legal_name"`
		TaxCode          string `json:"tax_code"`
		GS1CompanyPrefix string `json:"gs1_company_prefix"`
	} `json:"tenant"`
	Headquarters struct {
		GLN                  string   `json:"gln"`
		Name                 string   `json:"name"`
		Address              string   `json:"address"`
		City                 string   `json:"city"`
		CountryCode          *string  `json:"country_code"`
		Latitude             *float64 `json:"latitude"`
		Longitude            *float64 `json:"longitude"`
		GeoFenceRadiusMeters *int     `json:"geo_fence_radius_meters"`
	} `json:"headquarters"`
	Admin struct {
		Email    string  `json:"email"`
		Password string  `json:"password"`
		FullName string  `json:"full_name"`
		Phone    *string `json:"phone"`
	} `json:"admin"`
}

var (
	codePattern        = regexp.MustCompile(`^[A-Z0-9_]{3,32}$`)
	taxCodePattern     = regexp.MustCompile(`^[0-9]{10}(-[0-9]{3})?$`)
	countryCodePattern = regexp.MustCompile(`^[A-Z]{2}$`)
)

// validate checks the request and returns the registration it describes, or the problem to answer.
func (req *registrationRequest) validate() (Registration, *httpx.Problem) {
	var v rest.Validator

	t := &req.Tenant
	if v.Text("tenant.code", &t.Code, 3, 32) {
		v.Matches("tenant.code", t.Code, codePattern, "must be 3 to 32 characters from A-Z, 0-9, and _")
	}
	v.Text("tenant.legal_name", &t.LegalName, 1, 255)
	if v.Text("tenant.tax_code", &t.TaxCode, 10, 14) {
		v.Matches("tenant.tax_code", t.TaxCode, taxCodePattern, "must be 10 digits, optionally followed by - and 3 digits")
	}
	t.GS1CompanyPrefix = strings.TrimSpace(t.GS1CompanyPrefix)
	gcpValid := false
	if t.GS1CompanyPrefix == "" {
		v.Add("tenant.gs1_company_prefix", httpx.FieldRequired, "is required")
	} else {
		gcpValid = v.Key("tenant.gs1_company_prefix", gs1.ValidateCompanyPrefix(t.GS1CompanyPrefix))
	}

	hq := &req.Headquarters
	hq.GLN = strings.TrimSpace(hq.GLN)
	switch {
	case hq.GLN == "":
		v.Add("headquarters.gln", httpx.FieldRequired, "is required")
	case gcpValid:
		v.Key("headquarters.gln", gs1.ValidateGLN(hq.GLN, t.GS1CompanyPrefix))
	default:
		v.Key("headquarters.gln", gs1.Validate(gs1.GLN, hq.GLN))
	}
	v.Text("headquarters.name", &hq.Name, 1, 255)
	v.Text("headquarters.address", &hq.Address, 1, 500)
	v.Text("headquarters.city", &hq.City, 1, 100)
	country := location.DefaultCountryCode
	if hq.CountryCode != nil {
		country = strings.TrimSpace(*hq.CountryCode)
		v.Matches("headquarters.country_code", country, countryCodePattern, "must be an ISO 3166-1 alpha-2 code such as VN")
	}
	v.Float("headquarters.latitude", hq.Latitude, -90, 90)
	v.Float("headquarters.longitude", hq.Longitude, -180, 180)
	radius := location.DefaultGeoFenceRadiusMeters
	if hq.GeoFenceRadiusMeters != nil {
		radius = *hq.GeoFenceRadiusMeters
		v.Int("headquarters.geo_fence_radius_meters", radius, location.MinGeoFenceRadiusMeters, location.MaxGeoFenceRadiusMeters)
	}

	a := &req.Admin
	v.Email("admin.email", &a.Email)
	v.Password("admin.password", a.Password)
	v.Text("admin.full_name", &a.FullName, 1, 255)
	v.Phone("admin.phone", &a.Phone)

	if p := v.Problem(); p != nil {
		return Registration{}, p
	}
	return Registration{
		Code:             t.Code,
		LegalName:        t.LegalName,
		TaxCode:          t.TaxCode,
		GS1CompanyPrefix: t.GS1CompanyPrefix,
		Headquarters: Headquarters{
			GLN:                  hq.GLN,
			Name:                 hq.Name,
			Address:              hq.Address,
			City:                 hq.City,
			CountryCode:          country,
			Latitude:             location.RoundCoordinate(*hq.Latitude),
			Longitude:            location.RoundCoordinate(*hq.Longitude),
			GeoFenceRadiusMeters: radius,
		},
		Admin: Admin{Email: a.Email, Password: a.Password, FullName: a.FullName, Phone: a.Phone},
	}, nil
}

// conflictFields names the request field of each unique key, and explains the conflict.
var conflictFields = map[Key]struct{ field, message string }{
	KeyCode:            {"tenant.code", "is already registered"},
	KeyTaxCode:         {"tenant.tax_code", "is already registered"},
	KeyCompanyPrefix:   {"tenant.gs1_company_prefix", "is already registered or overlaps a registered company prefix"},
	KeyHeadquartersGLN: {"headquarters.gln", "is already registered"},
	KeyAdminEmail:      {"admin.email", "is already registered"},
}

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	var req registrationRequest
	if p := httpx.DecodeJSON(w, r, &req); p != nil {
		httpx.WriteProblem(w, r, *p)
		return
	}
	registration, p := req.validate()
	if p != nil {
		httpx.WriteProblem(w, r, *p)
		return
	}

	registered, err := h.registerer.Register(r.Context(), registration)
	var conflict *ConflictError
	switch {
	case errors.As(err, &conflict):
		c := conflictFields[conflict.Key]
		rest.Conflict(w, r, c.field, c.message)
		return
	case err != nil:
		rest.InternalError(w, r, h.logger, err)
		return
	}

	h.logger.InfoContext(r.Context(), "tenant registered",
		slog.String("tenant_id", registered.Tenant.ID.String()),
		slog.String("tenant_code", registered.Tenant.Code))
	w.Header().Set("Location", "/api/v1/tenant")
	httpx.WriteJSON(w, r, http.StatusCreated, registered)
}

func (h *Handler) profile(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Principal(w, r)
	if !ok {
		return
	}
	t, err := h.profiles.Profile(r.Context(), p)
	if err != nil {
		h.profileFailed(w, r, err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, t)
}

func (h *Handler) updateProfile(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Principal(w, r)
	if !ok {
		return
	}
	var patch rest.Patch
	if problem := httpx.DecodeJSON(w, r, &patch); problem != nil {
		httpx.WriteProblem(w, r, *problem)
		return
	}
	var v rest.Validator
	var changes ProfilePatch
	if name := rest.PatchField[string](&v, patch, "legal_name"); name.Set {
		if name.Null {
			v.Add("legal_name", httpx.FieldRequired, "cannot be null")
		} else if v.Text("legal_name", &name.Value, 1, 255) {
			changes.LegalName = &name.Value
		}
	}
	if digit := rest.PatchField[int](&v, patch, "sscc_extension_digit"); digit.Set {
		if digit.Null {
			v.Add("sscc_extension_digit", httpx.FieldRequired, "cannot be null")
		} else if v.Int("sscc_extension_digit", digit.Value, 0, 9) {
			changes.SSCCExtensionDigit = &digit.Value
		}
	}
	v.OnlyFields(patch, "legal_name", "sscc_extension_digit")
	if problem := v.Problem(); problem != nil {
		httpx.WriteProblem(w, r, *problem)
		return
	}

	t, err := h.profiles.UpdateProfile(r.Context(), p, changes)
	if err != nil {
		h.profileFailed(w, r, err)
		return
	}
	h.logger.InfoContext(r.Context(), "tenant profile updated")
	httpx.WriteJSON(w, r, http.StatusOK, t)
}

func (h *Handler) profileFailed(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case rest.Denied(w, r, err):
	case errors.Is(err, ErrNotFound):
		httpx.NotFound(w, r)
	default:
		rest.InternalError(w, r, h.logger, err)
	}
}

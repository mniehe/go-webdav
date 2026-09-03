package caldav

import (
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"regexp"

	"github.com/mniehe/davkit/internal"
)

// handleMkcalendar creates a calendar (RFC 4791 §5.3.1). internal.Handler does
// not know the method, so Handler.ServeHTTP routes it here.
//
// The target names a calendar that must not exist, so resolution differs from
// every other method: the governing permission is the account's CreateCalendars
// — a calendar that is not there cannot carry permissions of its own.
func (a *adapter) handleMkcalendar(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()

	res, err := a.cfg.Routes.Parse(ctx, r.URL.EscapedPath())
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return errNoSuchResource()
		}
		return fmt.Errorf("caldav: routing request path: %w", err)
	}
	if res.Kind != KindCalendar {
		return errUnsupportedMethod(r.Method)
	}
	if !a.caps.createsCalendars {
		return errUnsupportedMethod(r.Method)
	}

	perms, err := a.backend.AccountPermissions(ctx, actorFrom(ctx), res.Account)
	if err != nil {
		return fmt.Errorf("caldav: reading account permissions: %w", err)
	}
	if !perms.Any() {
		return a.denyUnknown()
	}
	if !perms.CreateCalendars {
		return denyOperation("create calendars in this account")
	}

	req := CreateCalendarRequest{Name: res.Calendar}
	if !internal.IsRequestBodyEmpty(r) {
		var body mkcalendarReq
		if err := internal.DecodeXMLRequest(r, &body); err != nil {
			return err
		}
		if err := internal.BoundPropNames(&body.Prop); err != nil {
			return err
		}

		updates := internal.PropUpdatesFromProp(&body.Prop)
		var patch calendarSettings
		rejected := make(map[xml.Name]int)
		for i := range updates {
			if code := applyCalendarSetting(&patch, &updates[i], true); code != http.StatusOK {
				rejected[updates[i].Name] = code
			}
		}
		if len(rejected) > 0 {
			// RFC 4791 §5.3.1.1: a property that cannot be set means the
			// calendar is not created at all, each property reporting its own
			// outcome.
			resp, err := internal.NewPropPatchFailure(r.URL.Path, updates, rejected)
			if err != nil {
				return err
			}
			w.Header().Set("Cache-Control", "no-cache")
			return internal.ServeMultiStatus(w, internal.NewMultiStatus(*resp))
		}
		patch.applyToCreate(&req)
	}

	creator := a.backend.(CalendarCreator) //nolint:errcheck // caps.createsCalendars was resolved from this assertion at construction
	if _, err := creator.CompareAndCreateCalendar(ctx, res.Account, req, Unconditional()); err != nil {
		if errors.Is(err, ErrAlreadyExists) {
			// RFC 4791 §5.3.1.1: the request URI must be unmapped.
			return internal.NewPreconditionError(http.StatusForbidden, internal.ResourceMustBeNullName)
		}
		return backendError(err)
	}

	// RFC 4791 §5.3.1 requires the response to be uncacheable.
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusCreated)
	return nil
}

func (a *adapter) PropPatch(r *http.Request, pu *internal.PropertyUpdate) (*internal.Response, error) {
	acc, err := a.resolve(r)
	if err != nil {
		return nil, err
	}
	if acc.Kind != KindCalendar || !a.caps.updatesCalendars {
		return nil, errUnsupportedMethod(r.Method)
	}
	if !acc.calendar.UpdateSettings {
		return nil, denyOperation("change this calendar's settings")
	}

	updates := pu.Updates()
	if len(updates) == 0 {
		// RFC 4918 §9.2 requires at least one instruction; a no-op write would
		// emit a <response> carrying neither status nor propstat, which §14.24
		// forbids.
		return nil, internal.HTTPErrorf(http.StatusBadRequest, "caldav: PROPPATCH requested no property changes")
	}

	var settings calendarSettings
	rejected := make(map[xml.Name]int)
	for i := range updates {
		if code := applyCalendarSetting(&settings, &updates[i], false); code != http.StatusOK {
			rejected[updates[i].Name] = code
		}
	}
	if len(rejected) > 0 {
		return internal.NewPropPatchFailure(r.URL.Path, updates, rejected)
	}

	updater := a.backend.(CalendarUpdater) //nolint:errcheck // caps.updatesCalendars was resolved from this assertion at construction
	if _, err := updater.CompareAndUpdateCalendar(r.Context(), acc.CalendarRef(), settings.patch(), Unconditional()); err != nil {
		return nil, backendError(err)
	}
	return internal.NewPropPatchSuccess(r.URL.Path, updates)
}

// calendarSettings accumulates the writable properties of a request before any
// of them is applied, because RFC 4918 §9.2 makes the application atomic.
type calendarSettings struct {
	displayName *string
	description *string
	color       *string
	sortOrder   ValuePatch[int]
	timezone    *Timezone
	accepts     *ItemKinds
}

func (s *calendarSettings) patch() CalendarPatch {
	return CalendarPatch{
		DisplayName: s.displayName,
		Description: s.description,
		Color:       s.color,
		SortOrder:   s.sortOrder,
		Timezone:    s.timezone,
	}
}

func (s *calendarSettings) applyToCreate(req *CreateCalendarRequest) {
	if s.displayName != nil {
		req.DisplayName = *s.displayName
	}
	if s.description != nil {
		req.Description = *s.description
	}
	if s.color != nil {
		req.Color = *s.color
	}
	if order, ok := s.sortOrder.Value(); ok {
		req.SortOrder = &order
	}
	if s.timezone != nil {
		req.Timezone = *s.timezone
	}
	if s.accepts != nil {
		req.Accepts = *s.accepts
	}
}

// calendarColorPattern matches the two colour forms clients write. An empty
// value is the removal case.
var calendarColorPattern = regexp.MustCompile(`^#[0-9A-Fa-f]{6}([0-9A-Fa-f]{2})?$`)

// applyCalendarSetting records one requested property change and returns that
// property's status. Anything not recognised as writable — an unknown dead
// property, or a protected live one — is 403, which fails the whole request.
func applyCalendarSetting(s *calendarSettings, prop *internal.PropUpdate, creating bool) int {
	switch prop.Name {
	case internal.DisplayNameName:
		var v internal.DisplayName
		if !internal.DecodePropUpdate(prop, &v) || len(v.Name) > internal.MaxPropValueSize {
			return http.StatusConflict
		}
		s.displayName = &v.Name
	case calendarDescriptionName:
		var v calendarDescription
		if !internal.DecodePropUpdate(prop, &v) || len(v.Description) > internal.MaxPropValueSize {
			return http.StatusConflict
		}
		s.description = &v.Description
	case calendarColorName:
		var v calendarColor
		if !internal.DecodePropUpdate(prop, &v) {
			return http.StatusConflict
		}
		if v.Color != "" && !calendarColorPattern.MatchString(v.Color) {
			return http.StatusConflict
		}
		s.color = &v.Color
	case calendarOrderName:
		if prop.Remove {
			s.sortOrder = ClearValue[int]()
			return http.StatusOK
		}
		var v calendarOrder
		if !internal.DecodePropUpdate(prop, &v) {
			return http.StatusConflict
		}
		s.sortOrder = SetValue(v.Order)
	case calendarTimezoneName:
		var v calendarTimezone
		if !internal.DecodePropUpdate(prop, &v) {
			return http.StatusConflict
		}
		tz := Timezone{}
		if v.Timezone != "" {
			parsed, err := ParseTimezone([]byte(v.Timezone))
			if err != nil {
				return http.StatusConflict
			}
			tz = parsed
		}
		s.timezone = &tz
	case supportedCalendarComponentSetName:
		// CalendarPatch cannot change what a calendar accepts after creation,
		// so the property is writable only through MKCALENDAR.
		if !creating {
			return http.StatusForbidden
		}
		var v supportedCalendarComponentSet
		if !internal.DecodePropUpdate(prop, &v) || prop.Remove {
			return http.StatusConflict
		}
		kinds := make([]ItemKind, 0, len(v.Comp))
		for _, comp := range v.Comp {
			kind, known := kindByComponent[comp.Name]
			if !known {
				return http.StatusConflict
			}
			kinds = append(kinds, kind)
		}
		accepts := OnlyItemKinds(kinds...)
		s.accepts = &accepts
	default:
		return http.StatusForbidden
	}
	return http.StatusOK
}

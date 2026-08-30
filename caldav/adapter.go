package caldav

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/mniehe/davkit/internal"
)

// adapter serves internal.Handler's dispatch over a Backend. Everything
// protocol-shaped lives on this side of it — URLs, privileges, entity tags,
// XML — so that nothing below it has to know any of them.
type adapter struct {
	backend Backend
	cfg     Config
	caps    capabilities
}

var _ internal.Backend = (*adapter)(nil)

// access is a resolved request: what the URL named, and what the actor may do
// with it. Every method begins by producing one, which is what makes it
// impossible to reach the backend without a permission having been consulted.
type access struct {
	Resource

	// Only one of these is populated, chosen by Resource.Kind. An item is
	// governed by the permissions on the calendar holding it, because
	// permissions are per-calendar and there is no per-item grant.
	account  AccountPermissions
	calendar CalendarPermissions
}

func (a *adapter) resolve(r *http.Request) (access, error) {
	ctx := r.Context()

	res, err := a.cfg.Routes.Parse(ctx, r.URL.EscapedPath())
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return access{}, errNoSuchResource()
		}
		return access{}, fmt.Errorf("caldav: routing request path: %w", err)
	}

	actor := actorFrom(ctx)
	if res.Kind == KindAccount {
		var accountPerms AccountPermissions
		if accountPerms, err = a.backend.AccountPermissions(ctx, actor, res.Account); err != nil {
			return access{}, fmt.Errorf("caldav: reading account permissions: %w", err)
		}
		if !accountPerms.Any() {
			return access{}, a.denyUnknown()
		}
		return access{Resource: res, account: accountPerms}, nil
	}

	var perms CalendarPermissions
	if perms, err = a.backend.CalendarPermissions(ctx, actor, res.CalendarRef()); err != nil {
		return access{}, fmt.Errorf("caldav: reading calendar permissions: %w", err)
	}
	if perms = perms.Normalised(); !perms.Any() {
		return access{}, a.denyUnknown()
	}
	return access{Resource: res, calendar: perms}, nil
}

// denyUnknown answers an actor who may do nothing at all with a resource.
//
// Under ConcealDenied that is a 404, and deliberately the same 404 a resource
// that does not exist gets: the handler has not asked whether it exists, and an
// actor who cannot see it has no business finding out by probing URLs.
func (a *adapter) denyUnknown() error {
	if a.cfg.Denial == RevealDenied {
		return internal.HTTPErrorf(http.StatusForbidden, "caldav: forbidden")
	}
	return errNoSuchResource()
}

func errNoSuchResource() error {
	return internal.HTTPErrorf(http.StatusNotFound, "caldav: no such resource")
}

// denyOperation answers an actor who may see the resource but not do this to
// it. It is never concealed: the client can already tell the resource is there,
// so a 404 would say nothing except something untrue.
func denyOperation(what string) error {
	return internal.HTTPErrorf(http.StatusForbidden, "caldav: not permitted to %s", what)
}

func errUnsupportedMethod(method string) error {
	return internal.HTTPErrorf(http.StatusMethodNotAllowed, "caldav: %s is not supported on this resource", method)
}

// backendError maps a backend's sentinel onto what it means over HTTP. It is
// deliberately narrow: an error it does not recognise is a 500, because an
// unrecognised storage failure is exactly that.
func backendError(err error) error {
	// An error the library already shaped into an HTTP response — a scan-budget
	// 507, a precondition — passes through untouched: only a backend's own
	// storage sentinel needs translating, and a backend never speaks HTTP.
	var httpErr *internal.HTTPError
	if errors.As(err, &httpErr) {
		return err
	}
	switch {
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrParentNotFound):
		return errNoSuchResource()
	case errors.Is(err, ErrForbidden):
		return internal.HTTPErrorf(http.StatusForbidden, "caldav: forbidden")
	case errors.Is(err, ErrPreconditionFailed):
		return internal.HTTPErrorf(http.StatusPreconditionFailed, "caldav: precondition failed")
	default:
		return fmt.Errorf("caldav: backend: %w", err)
	}
}

// allowedMethods is the Allow header for a resource: exactly the methods this
// handler answers with something other than a 405.
//
// It is derived from the kind of resource and the capabilities the backend
// implements, and from nothing else. Not from whether the resource exists,
// which would make OPTIONS a way to map out what is there; and not from the
// actor's permissions, which RFC 7231 §7.4.1 does not ask Allow to reflect —
// a method the actor may not use is refused with a 403, not omitted here.
func (a *adapter) allowedMethods(res Resource) []string {
	allow := []string{http.MethodOptions, "PROPFIND"}
	if res.Kind == KindCalendar {
		allow = append(allow, "REPORT")
		if a.caps.updatesCalendars {
			allow = append(allow, "PROPPATCH")
		}
		if a.caps.deletesCalendars {
			allow = append(allow, http.MethodDelete)
		}
		// MKCALENDAR is listed even though it can only fail here: the resource
		// exists, so the method is supported but its resource-must-be-null
		// precondition is unsatisfiable. Allow describes support, and a URL
		// where the method would succeed has no resource to answer OPTIONS.
		if a.caps.createsCalendars {
			allow = append(allow, "MKCALENDAR")
		}
	}
	if res.Kind == KindItem {
		allow = append(allow, http.MethodGet, http.MethodHead)
		if a.caps.writesItems {
			allow = append(allow, http.MethodPut, http.MethodDelete)
		}
		if a.caps.transfersItems {
			allow = append(allow, "COPY", "MOVE")
		}
	}
	return allow
}

func (a *adapter) Options(r *http.Request) (caps, allow []string, err error) {
	acc, err := a.resolve(r)
	if err != nil {
		return nil, nil, err
	}
	return []string{"calendar-access"}, a.allowedMethods(acc.Resource), nil
}

func (a *adapter) HeadGet(w http.ResponseWriter, r *http.Request) error {
	acc, err := a.resolve(r)
	if err != nil {
		return err
	}
	if acc.Kind != KindItem {
		return errUnsupportedMethod(r.Method)
	}
	if !acc.calendar.ViewDetails {
		return denyOperation("read the items in this calendar")
	}

	ctx := r.Context()
	item, err := a.getItem(ctx, acc.ItemRef())
	if err != nil {
		return backendError(err)
	}
	cal, err := a.getCalendar(ctx, acc.CalendarRef())
	if err != nil {
		return backendError(err)
	}

	w.Header().Set("Content-Type", calendarMediaType)
	w.Header().Set("ETag", etagFor(calendarScope(cal.ID), item).String())
	// ServeContent answers the conditional and range parts of RFC 9110 against
	// the ETag just set. The zero time leaves Last-Modified off: a revision is
	// not a clock, and inventing a timestamp would invite If-Modified-Since to
	// be trusted.
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(item.Content))
	return nil
}

func (a *adapter) Mkcol(r *http.Request) error {
	if _, err := a.resolve(r); err != nil {
		return err
	}
	return errUnsupportedMethod(r.Method)
}

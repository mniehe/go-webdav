package caldav

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"

	"github.com/mniehe/davkit/internal"
)

func (a *adapter) PropFind(r *http.Request, pf *internal.PropFind, depth internal.Depth, emit func(*internal.Response) error) error {
	acc, err := a.resolve(r)
	if err != nil {
		return err
	}

	ctx := r.Context()
	switch acc.Kind {
	case KindAccount:
		return a.propFindAccount(ctx, acc, pf, depth, emit)
	case KindCalendar:
		return a.propFindCalendar(ctx, acc, pf, depth, emit)
	default:
		return a.propFindItem(ctx, acc, pf, emit)
	}
}

func (a *adapter) propFindAccount(ctx context.Context, acc access, pf *internal.PropFind, depth internal.Depth, emit func(*internal.Response) error) error {
	resp, err := a.accountResponse(ctx, acc.Account, pf)
	if err != nil {
		return err
	}

	// The membership is resolved before the account's own row goes out: once
	// anything is emitted the document has started, and a refusal or a backend
	// contract violation could no longer fail the request outright.
	var cals []Calendar
	if depth != internal.DepthZero {
		if !acc.account.ListCalendars {
			return denyOperation("list this account's calendars")
		}
		if cals, err = a.listCalendars(ctx, acc.Account); err != nil {
			return backendError(err)
		}
	}

	if writeErr := emit(resp); writeErr != nil {
		return writeErr
	}
	actor := actorFrom(ctx)
	for i := range cals {
		ref := CalendarRef{Account: acc.Account, Calendar: cals[i].Name}
		perms, err := a.backend.CalendarPermissions(ctx, actor, ref)
		if err != nil {
			return fmt.Errorf("caldav: reading calendar permissions: %w", err)
		}
		// A grant over the account's calendar list is not a grant over each
		// calendar in it, so a member the actor cannot see is left out rather
		// than reported. Omission is the only concealment a multistatus has.
		if perms = perms.Normalised(); !perms.Any() {
			continue
		}
		resp, err := a.calendarResponse(ctx, ref, &cals[i], perms, pf)
		if err != nil {
			return err
		}
		if err := emit(resp); err != nil {
			return err
		}
	}
	return nil
}

func (a *adapter) propFindCalendar(ctx context.Context, acc access, pf *internal.PropFind, depth internal.Depth, emit func(*internal.Response) error) error {
	ref := acc.CalendarRef()
	cal, err := a.getCalendar(ctx, ref)
	if err != nil {
		return backendError(err)
	}

	resp, err := a.calendarResponse(ctx, ref, &cal, acc.calendar, pf)
	if err != nil {
		return err
	}

	// Membership before the calendar's own row, for the same reason as the
	// account listing: nothing may be emitted until the whole answer can be.
	var items []Item
	if depth != internal.DepthZero {
		if !acc.calendar.ViewDetails {
			return denyOperation("list the items in this calendar")
		}
		if items, _, err = a.listItems(ctx, ref); err != nil {
			return backendError(err)
		}
	}

	if writeErr := emit(resp); writeErr != nil {
		return writeErr
	}
	scope := calendarScope(cal.ID)
	for i := range items {
		resp, err := a.itemResponse(ctx, ItemRef{Calendar: ref, Item: items[i].Name}, items[i], scope, pf)
		if err != nil {
			return err
		}
		if err := emit(resp); err != nil {
			return err
		}
	}
	return nil
}

func (a *adapter) propFindItem(ctx context.Context, acc access, pf *internal.PropFind, emit func(*internal.Response) error) error {
	if !acc.calendar.ViewDetails {
		return denyOperation("read the items in this calendar")
	}

	ref := acc.ItemRef()
	item, err := a.getItem(ctx, ref)
	if err != nil {
		return backendError(err)
	}
	cal, err := a.getCalendar(ctx, ref.Calendar)
	if err != nil {
		return backendError(err)
	}

	resp, err := a.itemResponse(ctx, ref, item, calendarScope(cal.ID), pf)
	if err != nil {
		return err
	}
	return emit(resp)
}

func (a *adapter) accountResponse(ctx context.Context, account AccountID, pf *internal.PropFind) (*internal.Response, error) {
	self, err := a.cfg.Routes.Href(ctx, AccountResource(account))
	if err != nil {
		return nil, fmt.Errorf("caldav: rendering account href: %w", err)
	}
	principal, err := a.principalHref(ctx)
	if err != nil {
		return nil, err
	}

	// The account is both the principal and its calendar home. Discovery starts
	// at current-user-principal, reads calendar-home-set from it, and enumerates
	// that collection — so all three have to agree, and here they are one URL.
	props := map[xml.Name]internal.PropFindFunc{
		internal.ResourceTypeName: internal.PropFindValue(
			internal.NewResourceType(internal.CollectionName, internal.PrincipalName)),
		internal.DisplayNameName:          internal.PropFindValue(&internal.DisplayName{Name: string(account)}),
		internal.PrincipalURLName:         internal.PropFindValue(&internal.PrincipalURL{Href: internal.Href{Path: self}}),
		internal.CurrentUserPrincipalName: internal.PropFindValue(&internal.CurrentUserPrincipal{Href: internal.Href{Path: principal}}),
		calendarHomeSetName:               internal.PropFindValue(&calendarHomeSet{Href: internal.Href{Path: self}}),
	}
	return internal.NewPropFindResponse(self, pf, props)
}

func (a *adapter) calendarResponse(ctx context.Context, ref CalendarRef, cal *Calendar, perms CalendarPermissions, pf *internal.PropFind) (*internal.Response, error) {
	href, err := a.cfg.Routes.Href(ctx, CalendarResource(ref))
	if err != nil {
		return nil, fmt.Errorf("caldav: rendering calendar href: %w", err)
	}
	owner, err := a.cfg.Routes.Href(ctx, AccountResource(ref.Account))
	if err != nil {
		return nil, fmt.Errorf("caldav: rendering owner href: %w", err)
	}
	principal, err := a.principalHref(ctx)
	if err != nil {
		return nil, err
	}

	props := map[xml.Name]internal.PropFindFunc{
		internal.ResourceTypeName: internal.PropFindValue(
			internal.NewResourceType(internal.CollectionName, calendarName)),
		internal.DisplayNameName:             internal.PropFindValue(&internal.DisplayName{Name: cal.DisplayName}),
		internal.CurrentUserPrincipalName:    internal.PropFindValue(&internal.CurrentUserPrincipal{Href: internal.Href{Path: principal}}),
		internal.OwnerName:                   internal.PropFindValue(&internal.Owner{Href: &internal.Href{Path: owner}}),
		internal.SupportedPrivilegeSetName:   internal.PropFindValue(internal.NewSupportedPrivilegeSet()),
		internal.CurrentUserPrivilegeSetName: internal.PropFindValue(privilegeSet(privilegesFor(perms, a.caps))),
		internal.SupportedReportSetName:      internal.PropFindValue(internal.NewSupportedReportSet(a.supportedReports()...)),
		supportedCalendarDataName: internal.PropFindValue(&supportedCalendarData{
			Types: []calendarDataType{{ContentType: "text/calendar", Version: "2.0"}},
		}),
		supportedCalendarComponentSetName: internal.PropFindValue(
			&supportedCalendarComponentSet{Comp: componentNames(cal.Accepts)}),
	}

	if cal.Description != "" {
		props[calendarDescriptionName] = internal.PropFindValue(&calendarDescription{Description: cal.Description})
	}
	if !cal.Timezone.IsZero() {
		props[calendarTimezoneName] = internal.PropFindValue(&calendarTimezone{Timezone: string(cal.Timezone.Bytes())})
	}
	if cal.Color != "" {
		props[calendarColorName] = internal.PropFindValue(&calendarColor{Color: cal.Color})
	}
	if cal.SortOrder != nil {
		props[calendarOrderName] = internal.PropFindValue(&calendarOrder{Order: *cal.SortOrder})
	}
	if cal.MaxItemSize > 0 {
		props[maxResourceSizeName] = internal.PropFindValue(&maxResourceSize{Size: cal.MaxItemSize})
	}
	// Both properties answer the same question — has anything in here changed —
	// and both are a client's sync position, so they are only meaningful when
	// the backend can serve the delta they lead to.
	if a.caps.syncs {
		token := syncTokenFor(calendarScope(cal.ID), cal.Revision)
		props[internal.SyncTokenName] = internal.PropFindValue(&internal.SyncToken{Token: token})
		props[internal.GetCTagName] = internal.PropFindValue(&internal.GetCTag{CTag: token})
	}

	// RFC 6578 §4 keeps sync-token out of allprop. The rest are kept out for the
	// same reason it is: each is a fixed tree or a server-wide constant that says
	// nothing about this collection, repeated once per member of a listing.
	internal.RemoveFromAllProp(pf, props,
		internal.SyncTokenName,
		internal.SupportedPrivilegeSetName,
		internal.CurrentUserPrivilegeSetName,
		internal.OwnerName,
		maxResourceSizeName,
	)
	return internal.NewPropFindResponse(href, pf, props)
}

func (a *adapter) itemResponse(ctx context.Context, ref ItemRef, item Item, scope uint64, pf *internal.PropFind) (*internal.Response, error) {
	href, err := a.cfg.Routes.Href(ctx, ItemResource(ref))
	if err != nil {
		return nil, fmt.Errorf("caldav: rendering item href: %w", err)
	}

	props := map[xml.Name]internal.PropFindFunc{
		internal.ResourceTypeName:     internal.PropFindValue(internal.NewResourceType()),
		internal.GetETagName:          internal.PropFindValue(&internal.GetETag{ETag: etagFor(scope, item)}),
		internal.GetContentTypeName:   internal.PropFindValue(&internal.GetContentType{Type: calendarMediaType}),
		internal.GetContentLengthName: internal.PropFindValue(&internal.GetContentLength{Length: int64(len(item.Content))}),
		calendarDataName:              internal.PropFindValue(&calendarData{Data: item.Content}),
	}

	// RFC 4791 §9.6: allprop must not carry calendar-data, or a request for a
	// collection's metadata answers with the whole collection's contents.
	internal.RemoveFromAllProp(pf, props, calendarDataName)
	return internal.NewPropFindResponse(href, pf, props)
}

// principalHref is where the actor's own principal lives, which is what
// DAV:current-user-principal reports whatever resource is being described.
func (a *adapter) principalHref(ctx context.Context) (string, error) {
	href, err := a.cfg.Routes.Href(ctx, AccountResource(actorFrom(ctx).Account))
	if err != nil {
		return "", fmt.Errorf("caldav: rendering principal href: %w", err)
	}
	return href, nil
}

// supportedReports is the DAV:supported-report-set of a calendar: the REPORTs
// this handler dispatches, never the ones it merely knows the name of.
func (a *adapter) supportedReports() []xml.Name {
	reports := []xml.Name{calendarQueryName, calendarMultigetName, freeBusyQueryName}
	if a.caps.syncs {
		reports = append(reports, syncCollectionName)
	}
	return reports
}

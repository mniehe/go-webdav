package caldav

import "context"

// CalendarPermissions is what an actor may do with one calendar. The zero value
// denies everything, so a case you forget to write is a case that is refused.
type CalendarPermissions struct {
	ViewDetails      bool // see the items themselves; implies ViewAvailability
	ViewAvailability bool // see only busy times, not what the items are
	CreateItems      bool
	ReplaceItems     bool
	DeleteItems      bool
	UpdateSettings   bool // apply any CalendarPatch
	DeleteCalendar   bool
}

// Normalised applies the implications between permissions: ViewDetails implies
// ViewAvailability. The library calls it on everything an Authorizer returns,
// so you never have to set both.
func (p CalendarPermissions) Normalised() CalendarPermissions {
	if p.ViewDetails {
		p.ViewAvailability = true
	}
	return p
}

// Any reports whether the actor may do anything at all with the calendar. An
// actor with none of these cannot be told the calendar exists.
func (p CalendarPermissions) Any() bool {
	return p != CalendarPermissions{}
}

// ViewOnlyPermissions can read the calendar and everything in it, and change
// nothing.
func ViewOnlyPermissions() CalendarPermissions {
	return CalendarPermissions{ViewDetails: true, ViewAvailability: true}
}

// AvailabilityOnlyPermissions can see busy times but not what the items are.
func AvailabilityOnlyPermissions() CalendarPermissions {
	return CalendarPermissions{ViewAvailability: true}
}

// EditPermissions can read and change the items, but not the calendar itself.
func EditPermissions() CalendarPermissions {
	return CalendarPermissions{
		ViewDetails:      true,
		ViewAvailability: true,
		CreateItems:      true,
		ReplaceItems:     true,
		DeleteItems:      true,
	}
}

// OwnerPermissions can do everything, including deleting the calendar.
func OwnerPermissions() CalendarPermissions {
	p := EditPermissions()
	p.UpdateSettings = true
	p.DeleteCalendar = true
	return p
}

// AccountPermissions is what an actor may do with an account's calendar list.
// The zero value denies everything.
type AccountPermissions struct {
	ListCalendars   bool
	CreateCalendars bool
}

// Any reports whether the actor may do anything at all with the account.
func (p AccountPermissions) Any() bool {
	return p != AccountPermissions{}
}

// Authorizer answers what an actor may do. Both questions are in domain
// language; the library maps them to protocol privileges itself.
//
// An actor with no permission at all is not an error: return the zero value.
// Reserve errors for a lookup that failed, which becomes a 500.
type Authorizer interface {
	CalendarPermissions(ctx context.Context, actor Actor, ref CalendarRef) (CalendarPermissions, error)
	AccountPermissions(ctx context.Context, actor Actor, account AccountID) (AccountPermissions, error)
}

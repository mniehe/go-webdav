package caldav

import (
	"time"

	"github.com/emersion/go-ical"
)

// These types are the CalDAV query vocabulary (RFC 4791 §9.6–§9.9), decoded
// from REPORT bodies and consumed by the matching engine. None of it is
// exported: a backend never sees a query, because matching is the library's
// job over the bytes the backend stores.

// calendarCompRequest is a calendar-data projection request (RFC 4791 §9.6).
//
// The zero value means the client did not ask for calendar data at all. A bare
// <calendar-data/> decodes to AllProps and AllComps instead, which requests the
// complete object.
type calendarCompRequest struct {
	// Name is the iCalendar component this projection applies to, e.g.
	// "VCALENDAR" at the root and "VEVENT" for a nested request. It is empty
	// when no component was named.
	Name string

	// AllProps requests every property of the component; Props names an
	// explicit subset. RFC 4791 §9.6.1 makes the two mutually exclusive.
	AllProps bool
	Props    []calendarPropRequest

	// AllComps requests every child component; Comps names an explicit subset
	// with its own nested projection. Also mutually exclusive.
	AllComps bool
	Comps    []calendarCompRequest

	// Expand, when set, asks for recurrence instances within a bounded window
	// rather than the recurrence rule itself. See CalendarExpandRequest.
	Expand *calendarExpandRequest

	// LimitRecurrence, when set, keeps the master component and its recurrence
	// rule but drops overridden instances that impact the window in neither
	// their current nor their original time (RFC 4791 §9.6.6). Mutually
	// exclusive with Expand.
	LimitRecurrence *calendarTimeWindow

	// LimitFreeBusy, when set, keeps only the FREEBUSY property values that
	// intersect the window (RFC 4791 §9.6.7).
	LimitFreeBusy *calendarTimeWindow
}

// calendarTimeWindow is a [Start, End) window in UTC, with Start always before
// End: the server rejects a request that omits or inverts either bound.
type calendarTimeWindow struct {
	Start, End time.Time
}

// calendarPropRequest names one property of a calendar-data projection
// (RFC 4791 §9.6.4). NoValue asks for the property name and parameters with
// the value data stripped.
type calendarPropRequest struct {
	Name    string
	NoValue bool
}

// calendarExpandRequest is the CALDAV:expand window of a calendar-data request
// (RFC 4791 §9.6.5). Both bounds are always set and Start always precedes End:
// the server rejects a request that omits or inverts either.
//
// Expansion replaces a recurring component with one instance per occurrence
// that intersects the window, so it turns a small request into work
// proportional to the recurrence frequency. Treat the window as untrusted.
type calendarExpandRequest struct {
	Start, End time.Time
}

type compFilter struct {
	Name         string
	IsNotDefined bool
	Start, End   time.Time
	Props        []propFilter
	Comps        []compFilter
}

type paramFilter struct {
	Name         string
	IsNotDefined bool
	TextMatch    *textMatch
}

type propFilter struct {
	Name         string
	IsNotDefined bool
	Start, End   time.Time
	TextMatch    *textMatch
	ParamFilter  []paramFilter
}

type textMatch struct {
	Text            string
	NegateCondition bool

	// Collation names the comparison rule. An empty value means the RFC 4791
	// §7.5.1 default, i;ascii-casemap, which is case-insensitive.
	Collation string
}

type calendarQuery struct {
	CompRequest calendarCompRequest
	CompFilter  compFilter
}

// calendarObject is a parsed calendar object as the matching engine consumes
// it. The handler builds these from items; no backend ever supplies one.
type calendarObject struct {
	Path          string
	ModTime       time.Time
	ContentLength int64
	ETag          string
	Data          *ical.Calendar
}

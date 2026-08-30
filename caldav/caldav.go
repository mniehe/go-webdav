// Package caldav is the ref-based CalDAV backend contract.
//
// Nothing here is a protocol concept. There are no ETags, privileges, sync
// tokens, hrefs or XML in this package's vocabulary — those belong to the
// handler, which never asks a backend about them. What it asks about is
// storage: list these, fetch that, write this if the current state still
// matches.
//
// Start at [Backend]. It is the whole of a read-only calendar server. Writing,
// incremental sync, calendar management and sharing are separate interfaces a
// backend may also implement; see the capability list on [Backend].
package caldav

// calendarMediaType is what a calendar object is served and accepted as
// (RFC 5545 §8.1).
const calendarMediaType = "text/calendar; charset=utf-8"

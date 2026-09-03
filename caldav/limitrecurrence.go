package caldav

import (
	"net/http"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/mniehe/davkit/internal"
)

// limitRecurrenceCalendar returns cal without the overridden recurrence
// instances that impact the window in neither their current nor their original
// time (RFC 4791 §9.6.6). Everything else — masters, their recurrence rules,
// VTIMEZONEs, non-recurring components — is kept as stored. The input is never
// mutated: a Backend commonly returns pointers into its own cache.
func limitRecurrenceCalendar(cal *ical.Calendar, window *calendarTimeWindow) (*ical.Calendar, error) {
	// Overlap is judged on times resolved to UTC; resolveObjectTimes clones,
	// preserving child order, so resolved children align with cal's by index.
	resolved, err := resolveObjectTimes(cal, nil)
	if err != nil {
		return nil, err
	}

	// An override's original interval starts at its RECURRENCE-ID and spans its
	// master's duration.
	masterDurations := make(map[string]time.Duration)
	for _, child := range resolved.Children {
		if child.Name == ical.CompTimezone || child.Props.Get(ical.PropRecurrenceID) != nil {
			continue
		}
		start, end, err := compInterval(child, time.UTC)
		if err != nil {
			continue
		}
		masterDurations[propValue(child, ical.PropUID)] = end.Sub(start)
	}

	root := cloneComponent(cal.Component)
	root.Children = nil
	out := &ical.Calendar{Component: root}
	for i, child := range cal.Children {
		if child.Props.Get(ical.PropRecurrenceID) == nil {
			out.Children = append(out.Children, child)
			continue
		}
		impacts, err := overrideImpacts(resolved.Children[i], masterDurations, window)
		if err != nil {
			return nil, err
		}
		if impacts {
			out.Children = append(out.Children, child)
		}
	}
	return out, nil
}

func overrideImpacts(ov *ical.Component, masterDurations map[string]time.Duration, window *calendarTimeWindow) (bool, error) {
	curStart, curEnd, err := compInterval(ov, time.UTC)
	if err != nil {
		return false, internal.HTTPErrorf(http.StatusForbidden, "caldav: cannot limit recurrence set: invalid override interval")
	}
	if intervalOverlaps(window.Start, window.End, curStart, curEnd) {
		return true, nil
	}

	ridProp := ov.Props.Get(ical.PropRecurrenceID)
	rid, err := ridProp.DateTime(time.UTC)
	if err != nil {
		return false, internal.HTTPErrorf(http.StatusForbidden, "caldav: cannot limit recurrence set: invalid RECURRENCE-ID")
	}
	// RANGE=THISANDFUTURE redefines every instance from the RECURRENCE-ID on,
	// so the override matters whenever the window reaches past that point.
	if strings.EqualFold(ridProp.Params.Get(ical.ParamRange), "THISANDFUTURE") && rid.Before(window.End) {
		return true, nil
	}

	duration, known := masterDurations[propValue(ov, ical.PropUID)]
	if !known {
		duration = curEnd.Sub(curStart)
	}
	return intervalOverlaps(window.Start, window.End, rid, rid.Add(duration)), nil
}

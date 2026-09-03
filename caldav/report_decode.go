package caldav

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/mniehe/davkit/internal"
)

func decodeParamFilter(el *paramFilterReq) (*paramFilter, error) {
	pf := &paramFilter{Name: el.Name}
	if el.IsNotDefined != nil {
		if el.TextMatch != nil {
			return nil, fmt.Errorf("caldav: failed to parse param-filter: if is-not-defined is provided, text-match can't be provided")
		}
		pf.IsNotDefined = true
	}
	if el.TextMatch != nil {
		pf.TextMatch = &textMatch{Text: el.TextMatch.Text, Collation: el.TextMatch.Collation, NegateCondition: bool(el.TextMatch.NegateCondition)}
	}
	return pf, nil
}

func decodePropFilter(el *propFilterReq) (*propFilter, error) {
	pf := &propFilter{Name: el.Name}
	if el.IsNotDefined != nil {
		if el.TextMatch != nil || el.TimeRange != nil || len(el.ParamFilter) > 0 {
			return nil, fmt.Errorf("caldav: failed to parse prop-filter: if is-not-defined is provided, text-match, time-range, or param-filter can't be provided")
		}
		pf.IsNotDefined = true
	}
	if el.TextMatch != nil {
		pf.TextMatch = &textMatch{Text: el.TextMatch.Text, Collation: el.TextMatch.Collation, NegateCondition: bool(el.TextMatch.NegateCondition)}
	}
	if el.TimeRange != nil {
		pf.Start = time.Time(el.TimeRange.Start)
		pf.End = time.Time(el.TimeRange.End)
	}
	for i := range el.ParamFilter {
		paramFi, err := decodeParamFilter(&el.ParamFilter[i])
		if err != nil {
			return nil, err
		}
		pf.ParamFilter = append(pf.ParamFilter, *paramFi)
	}
	return pf, nil
}

func decodeCompFilter(el *compFilterReq) (*compFilter, error) {
	cf := &compFilter{Name: el.Name}
	if el.IsNotDefined != nil {
		if el.TimeRange != nil || len(el.PropFilters) > 0 || len(el.CompFilters) > 0 {
			return nil, fmt.Errorf("caldav: failed to parse comp-filter: if is-not-defined is provided, time-range, prop-filter, or comp-filter can't be provided")
		}
		cf.IsNotDefined = true
	}
	if el.TimeRange != nil {
		cf.Start = time.Time(el.TimeRange.Start)
		cf.End = time.Time(el.TimeRange.End)
	}
	for i := range el.PropFilters {
		pf, err := decodePropFilter(&el.PropFilters[i])
		if err != nil {
			return nil, err
		}
		cf.Props = append(cf.Props, *pf)
	}
	for i := range el.CompFilters {
		child, err := decodeCompFilter(&el.CompFilters[i])
		if err != nil {
			return nil, err
		}
		cf.Comps = append(cf.Comps, *child)
	}
	return cf, nil
}

// maxCompRequestNodes bounds the component tree one calendar-data element may
// describe. Every node becomes a CalendarCompRequest that a backend typically
// turns into a projection or a map key, and the XML body limit alone allows far
// more nodes than any real client sends.
const maxCompRequestNodes = 256

func decodeComp(comp *comp) (*calendarCompRequest, error) {
	nodes := 0
	return decodeCompNode(comp, &nodes)
}

func decodeCompNode(comp *comp, nodes *int) (*calendarCompRequest, error) {
	if comp == nil {
		return nil, internal.HTTPErrorf(http.StatusBadRequest, "caldav: unexpected empty calendar-data in request")
	}
	if comp.Allprop != nil && len(comp.Prop) > 0 {
		return nil, internal.HTTPErrorf(http.StatusBadRequest, "caldav: only one of allprop or prop can be specified in calendar-data")
	}
	if comp.Allcomp != nil && len(comp.Comp) > 0 {
		return nil, internal.HTTPErrorf(http.StatusBadRequest, "caldav: only one of allcomp or comp can be specified in calendar-data")
	}
	if err := countCompRequestNode(nodes, comp.Name); err != nil {
		return nil, err
	}

	req := &calendarCompRequest{
		Name:     comp.Name,
		AllProps: comp.Allprop != nil,
		AllComps: comp.Allcomp != nil,
	}
	for _, p := range comp.Prop {
		if err := countCompRequestNode(nodes, p.Name); err != nil {
			return nil, err
		}
		req.Props = append(req.Props, calendarPropRequest{Name: p.Name, NoValue: bool(p.NoValue)})
	}
	for i := range comp.Comp {
		decoded, err := decodeCompNode(&comp.Comp[i], nodes)
		if err != nil {
			return nil, err
		}
		req.Comps = append(req.Comps, *decoded)
	}
	return req, nil
}

func countCompRequestNode(nodes *int, name string) error {
	*nodes++
	if *nodes > maxCompRequestNodes {
		return internal.HTTPErrorf(http.StatusBadRequest, "caldav: calendar-data names more than %d components and properties", maxCompRequestNodes)
	}
	if len(name) > internal.MaxPropNameSize {
		return internal.HTTPErrorf(http.StatusBadRequest, "caldav: calendar-data name exceeds %d bytes", internal.MaxPropNameSize)
	}
	return nil
}

// decodeExpand reads the RFC 4791 §9.6.5 expand window. Both attributes are
// required: a missing one decodes as the zero time, which would otherwise ask
// for an unbounded expansion.
func decodeExpand(el *expand) (*calendarExpandRequest, error) {
	if el == nil {
		return nil, nil
	}
	start, end := time.Time(el.Start), time.Time(el.End)
	if start.IsZero() || end.IsZero() {
		return nil, internal.HTTPErrorf(http.StatusBadRequest, "caldav: expand requires both a start and an end")
	}
	if !start.Before(end) {
		return nil, internal.HTTPErrorf(http.StatusBadRequest, "caldav: expand start must precede end")
	}
	return &calendarExpandRequest{Start: start, End: end}, nil
}

// decodeWindow reads a [start, end) UTC window whose two attributes are both
// required: a missing one decodes as the zero time, which would otherwise ask
// for an unbounded window.
func decodeWindow(element string, rawStart, rawEnd dateWithUTCTime) (*calendarTimeWindow, error) {
	start, end := time.Time(rawStart), time.Time(rawEnd)
	if start.IsZero() || end.IsZero() {
		return nil, internal.HTTPErrorf(http.StatusBadRequest, "caldav: %s requires both a start and an end", element)
	}
	if !start.Before(end) {
		return nil, internal.HTTPErrorf(http.StatusBadRequest, "caldav: %s start must precede end", element)
	}
	return &calendarTimeWindow{Start: start, End: end}, nil
}

func decodeCalendarDataReq(calendarData *calendarDataReq) (*calendarCompRequest, error) {
	req := &calendarCompRequest{AllProps: true, AllComps: true}
	if calendarData.Comp != nil {
		decoded, err := decodeComp(calendarData.Comp)
		if err != nil {
			return nil, err
		}
		// RFC 4791 §9.6.1 roots the component tree at VCALENDAR. Accepting another
		// name would leave the projection with nothing to match, and the object
		// would be returned whole.
		if !strings.EqualFold(decoded.Name, ical.CompCalendar) {
			return nil, internal.HTTPErrorf(http.StatusBadRequest, "caldav: calendar-data must be rooted at %s, got %q", ical.CompCalendar, decoded.Name)
		}
		req = decoded
	}

	expand, err := decodeExpand(calendarData.Expand)
	if err != nil {
		return nil, err
	}
	req.Expand = expand

	if calendarData.LimitRecurrenceSet != nil {
		// RFC 4791 §9.6: calendar-data allows (expand | limit-recurrence-set)?,
		// never both.
		if req.Expand != nil {
			return nil, internal.HTTPErrorf(http.StatusBadRequest, "caldav: expand and limit-recurrence-set are mutually exclusive")
		}
		window, err := decodeWindow("limit-recurrence-set", calendarData.LimitRecurrenceSet.Start, calendarData.LimitRecurrenceSet.End)
		if err != nil {
			return nil, err
		}
		req.LimitRecurrence = window
	}

	if calendarData.LimitFreeBusySet != nil {
		window, err := decodeWindow("limit-freebusy-set", calendarData.LimitFreeBusySet.Start, calendarData.LimitFreeBusySet.End)
		if err != nil {
			return nil, err
		}
		req.LimitFreeBusy = window
	}
	return req, nil
}

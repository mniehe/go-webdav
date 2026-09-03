package caldav

import (
	"strings"

	"github.com/emersion/go-ical"
)

// requiredProps are the properties a component cannot be valid without. RFC 4791
// §9.6 requires the projection to still yield a valid iCalendar object, so these
// survive even when the client did not name them.
func requiredProps(compName string) []string {
	switch compName {
	case ical.CompCalendar:
		return []string{ical.PropVersion, ical.PropProductID}
	case ical.CompEvent, ical.CompToDo, ical.CompJournal, ical.CompFreeBusy:
		return []string{ical.PropUID, ical.PropDateTimeStamp}
	}
	return nil
}

// projectionRequested reports whether req asks for anything. The zero value means
// no calendar-data was requested, which is not the same as requesting nothing.
func projectionRequested(req *calendarCompRequest) bool {
	return req.Name != "" || req.AllProps || req.AllComps ||
		len(req.Props) > 0 || len(req.Comps) > 0
}

func compRequestFor(reqs []calendarCompRequest, name string) *calendarCompRequest {
	for i := range reqs {
		if strings.EqualFold(reqs[i].Name, name) {
			return &reqs[i]
		}
	}
	return nil
}

// recurrenceProps generate the instances an expansion walks. A projection that
// dropped them would leave the server nothing to expand, so they survive a
// narrowing request whenever expansion was also asked for; the instances are
// projected again afterwards, which removes them.
func isRecurrenceProp(name string) bool {
	switch strings.ToUpper(name) {
	case ical.PropRecurrenceRule, ical.PropRecurrenceDates, ical.PropExceptionDates, ical.PropRecurrenceID, "EXRULE":
		return true
	}
	return false
}

// projectComponent returns comp reduced to what req names (RFC 4791 §9.6.1).
func projectComponent(comp *ical.Component, req *calendarCompRequest, keepRecurrence bool) *ical.Component {
	if req.AllProps && req.AllComps {
		return cloneComponent(comp)
	}

	out := &ical.Component{Name: comp.Name, Props: make(ical.Props)}

	keep := func(name string) (noValue, ok bool) {
		for i := range req.Props {
			if strings.EqualFold(req.Props[i].Name, name) {
				return req.Props[i].NoValue, true
			}
		}
		if req.AllProps {
			return false, true
		}
		if keepRecurrence && isRecurrenceProp(name) {
			return false, true
		}
		for _, want := range requiredProps(comp.Name) {
			if strings.EqualFold(want, name) {
				return false, true
			}
		}
		return false, false
	}
	for name, props := range comp.Props {
		noValue, ok := keep(name)
		if !ok {
			continue
		}
		cloned := cloneProps(props)
		if noValue {
			for i := range cloned {
				cloned[i].Value = ""
			}
		}
		out.Props[name] = cloned
	}

	for _, child := range comp.Children {
		if req.AllComps {
			out.Children = append(out.Children, cloneComponent(child))
			continue
		}
		if sub := compRequestFor(req.Comps, child.Name); sub != nil {
			out.Children = append(out.Children, projectComponent(child, sub, keepRecurrence))
		}
	}
	return out
}

// projectCalendar applies req to cal, returning cal unchanged when no projection
// was requested. The input is never mutated: a Backend commonly returns slices
// into its own cache.
func projectCalendar(cal *ical.Calendar, req *calendarCompRequest) *ical.Calendar {
	if cal == nil || cal.Component == nil || !projectionRequested(req) {
		return cal
	}
	if !strings.EqualFold(req.Name, cal.Name) {
		return cal
	}
	return &ical.Calendar{Component: projectComponent(cal.Component, req, req.Expand != nil)}
}

func projectObject(co calendarObject, req *calendarCompRequest) calendarObject {
	co.Data = projectCalendar(co.Data, req)
	return co
}

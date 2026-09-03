package caldav

import (
	"net/http"
	"time"

	"github.com/emersion/go-ical"
	"github.com/mniehe/davkit/internal"
)

// maxExpandedInstances bounds how many components one expansion may emit. A
// window and a recurrence frequency are both client-controlled, so a fixed-size
// request otherwise buys work proportional to their product.
const maxExpandedInstances = 10_000

// maxExpandedBytes bounds what those instances may weigh. The count alone does
// not: each one is a full clone of its master, so ten thousand of a
// multi-megabyte component is tens of gigabytes — built whole, in memory, before
// the response encoder sees any of it.
const maxExpandedBytes = 32 << 20

// expansionBudget is spent across a whole REPORT, so a request expanding many
// objects is bounded by its total rather than once per object.
type expansionBudget struct {
	instances int
	bytes     int
}

func newExpansionBudget() *expansionBudget {
	return &expansionBudget{instances: maxExpandedInstances, bytes: maxExpandedBytes}
}

func (b *expansionBudget) spend(comp *ical.Component) error {
	if b.instances <= 0 {
		return internal.HTTPErrorf(http.StatusForbidden, "caldav: expanding this request would yield more than %d instances", maxExpandedInstances)
	}
	size := componentSize(comp)
	if b.bytes < size {
		return internal.HTTPErrorf(http.StatusForbidden, "caldav: expanding this request would yield more than %d bytes", maxExpandedBytes)
	}
	b.instances--
	b.bytes -= size
	return nil
}

// componentSize approximates a component's serialised size — close enough to
// price an expansion that clones it many times over.
func componentSize(comp *ical.Component) int {
	n := len(comp.Name)
	for name, props := range comp.Props {
		for i := range props {
			n += len(name) + len(props[i].Value)
		}
	}
	for _, child := range comp.Children {
		n += componentSize(child)
	}
	return n
}

// expandCalendar rewrites cal as the recurrence instances intersecting the
// requested window, per RFC 4791 §9.6.5: each recurring component is replaced by
// one component per occurrence carrying RECURRENCE-ID, the instances drop the
// recurrence properties that generated them, VTIMEZONE components are removed,
// and the times they referenced are written as UTC.
//
// The server performs this rather than the backend, so every backend answers
// expand identically. A Backend must therefore return unexpanded data and leave
// CalendarCompRequest.Expand alone.
func expandCalendar(cal *ical.Calendar, req *calendarExpandRequest) (*ical.Calendar, error) {
	return expandCalendarWithin(cal, req, newExpansionBudget())
}

// expandCalendarWithin draws from a budget the caller owns, so one REPORT
// expanding many objects is bounded by its total rather than per object.
func expandCalendarWithin(cal *ical.Calendar, req *calendarExpandRequest, budget *expansionBudget) (*ical.Calendar, error) {
	// An object may carry its zones as embedded VTIMEZONEs that time.LoadLocation
	// cannot serve; resolving them to UTC up front lets the expansion below read
	// every time the same way. The result is UTC regardless, so normalising the
	// input changes nothing an unexpanded read would have returned.
	cal, resolveErr := resolveObjectTimes(cal, nil)
	if resolveErr != nil {
		return nil, resolveErr
	}

	root := cloneComponent(cal.Component)
	root.Children = nil
	out := &ical.Calendar{Component: root}

	emit := func(comp *ical.Component) error {
		if err := budget.spend(comp); err != nil {
			return err
		}
		out.Children = append(out.Children, comp)
		return nil
	}

	masters, overrides, plain, err := partitionForExpansion(cal)
	if err != nil {
		return nil, err
	}

	byKey := make(map[string]*recurrenceOverride, len(overrides))
	for i := range overrides {
		byKey[overrides[i].key] = &overrides[i]
	}

	consumed := make(map[string]bool, len(overrides))
	for _, master := range masters {
		if err := expandMaster(master, req, byKey, consumed, emit); err != nil {
			return nil, err
		}
	}

	// An override whose RECURRENCE-ID no longer matches an occurrence of the
	// rule still describes a real instance, so report it on its own.
	for _, ov := range overrides {
		if consumed[ov.key] {
			continue
		}
		ovStart, ovEnd, err := compInterval(ov.comp, time.UTC)
		if err != nil {
			return nil, internal.HTTPErrorf(http.StatusForbidden, "caldav: cannot expand recurrence: invalid override interval")
		}
		if !intervalOverlaps(req.Start, req.End, ovStart, ovEnd) {
			continue
		}
		if err := emit(instanceTimesToUTC(cloneComponent(ov.comp))); err != nil {
			return nil, err
		}
	}

	for _, comp := range plain {
		if comp.Name == ical.CompEvent {
			match, err := matchCompTimeRange(req.Start, req.End, comp)
			if err != nil {
				return nil, err
			}
			if !match {
				continue
			}
		}
		if err := emit(instanceTimesToUTC(cloneComponent(comp))); err != nil {
			return nil, err
		}
	}

	return out, nil
}

// recurrenceMaster is a component carrying a recurrence rule, paired with the
// set it generates so the rule is only parsed once.
type recurrenceMaster struct {
	comp *ical.Component
	uid  string
	set  recurrenceIterator
}

type recurrenceIterator interface {
	Iterator() func() (time.Time, bool)
}

// recurrenceOverride is a component that redefines one instance of a recurring
// component, identified by UID plus RECURRENCE-ID.
type recurrenceOverride struct {
	comp         *ical.Component
	key          string
	recurrenceID time.Time
}

func partitionForExpansion(cal *ical.Calendar) (masters []recurrenceMaster, overrides []recurrenceOverride, plain []*ical.Component, err error) {
	for _, child := range cal.Children {
		if child.Name == ical.CompTimezone {
			continue
		}

		if prop := child.Props.Get(ical.PropRecurrenceID); prop != nil {
			recurrenceID, derr := prop.DateTime(time.UTC)
			if derr != nil {
				return nil, nil, nil, internal.HTTPErrorf(http.StatusForbidden, "caldav: cannot expand recurrence: invalid RECURRENCE-ID")
			}
			overrides = append(overrides, recurrenceOverride{
				comp:         child,
				key:          overrideKey(propValue(child, ical.PropUID), recurrenceID),
				recurrenceID: recurrenceID,
			})
			continue
		}

		set, derr := splitDateLists(child).RecurrenceSet(time.UTC)
		if derr != nil {
			return nil, nil, nil, internal.HTTPErrorf(http.StatusForbidden, "caldav: cannot expand recurrence: invalid recurrence rule")
		}
		if set == nil {
			plain = append(plain, child)
			continue
		}
		masters = append(masters, recurrenceMaster{
			comp: child,
			uid:  propValue(child, ical.PropUID),
			set:  set,
		})
	}
	return masters, overrides, plain, nil
}

func expandMaster(master recurrenceMaster, req *calendarExpandRequest, byKey map[string]*recurrenceOverride, consumed map[string]bool, emit func(*ical.Component) error) error {
	compStart, compEnd, err := compInterval(master.comp, time.UTC)
	if err != nil {
		return internal.HTTPErrorf(http.StatusForbidden, "caldav: cannot expand recurrence: invalid component interval")
	}
	duration := compEnd.Sub(compStart)

	iter := master.set.Iterator()
	// Occurrences increase, so the walk ends either when the rule runs out or
	// when their starts pass the window. Reaching the iteration cap instead means
	// the answer would be incomplete, which is worse than refusing. Each
	// occurrence spans the master's duration, so one beginning before the window
	// can still reach into it.
	for i := 0; i < maxRecurrenceIterations; i++ {
		occ, ok := iter()
		if !ok {
			return nil
		}
		if !occ.Before(req.End) {
			return nil
		}
		if !intervalOverlaps(req.Start, req.End, occ, occ.Add(duration)) {
			continue
		}

		key := overrideKey(master.uid, occ.UTC())
		if ov, found := byKey[key]; found {
			consumed[key] = true
			if err := emit(instanceTimesToUTC(cloneComponent(ov.comp))); err != nil {
				return err
			}
			continue
		}

		instance, err := recurrenceInstance(master.comp, occ)
		if err != nil {
			return err
		}
		if err := emit(instance); err != nil {
			return err
		}
	}
	return internal.HTTPErrorf(http.StatusForbidden, "caldav: recurrence expansion exceeded %d iterations", maxRecurrenceIterations)
}

// recurrenceInstance materialises one occurrence of a recurring component: the
// rule that generated it is removed, RECURRENCE-ID identifies it, and the
// master's duration is carried across rather than its absolute end.
func recurrenceInstance(master *ical.Component, occ time.Time) (*ical.Component, error) {
	instance := cloneComponent(master)
	for _, name := range []string{ical.PropRecurrenceRule, ical.PropRecurrenceDates, ical.PropExceptionDates, "EXRULE"} {
		instance.Props.Del(name)
	}

	var duration time.Duration
	if master.Name == ical.CompEvent && master.Props.Get(ical.PropDateTimeEnd) != nil {
		event := &ical.Event{Component: master}
		start, err := event.DateTimeStart(time.UTC)
		if err != nil {
			return nil, internal.HTTPErrorf(http.StatusForbidden, "caldav: cannot expand recurrence: invalid DTSTART")
		}
		end, err := event.DateTimeEnd(time.UTC)
		if err != nil {
			return nil, internal.HTTPErrorf(http.StatusForbidden, "caldav: cannot expand recurrence: invalid DTEND")
		}
		duration = end.Sub(start)
	}

	occUTC := occ.UTC()
	instance.Props.SetDateTime(ical.PropRecurrenceID, occUTC)
	instance.Props.SetDateTime(ical.PropDateTimeStart, occUTC)
	if duration != 0 {
		instance.Props.SetDateTime(ical.PropDateTimeEnd, occUTC.Add(duration))
	}
	return instance, nil
}

// instanceTimesToUTC rewrites the absolute times of an already-materialised
// component, so that a response carrying no VTIMEZONE stays self-describing.
func instanceTimesToUTC(comp *ical.Component) *ical.Component {
	for _, name := range []string{ical.PropDateTimeStart, ical.PropDateTimeEnd, ical.PropRecurrenceID} {
		prop := comp.Props.Get(name)
		if prop == nil {
			continue
		}
		t, err := prop.DateTime(time.UTC)
		if err != nil {
			// Leave a value the decoder could not read as it was rather than
			// replacing it with a zero time.
			continue
		}
		comp.Props.SetDateTime(name, t.UTC())
	}
	return comp
}

func overrideKey(uid string, recurrenceID time.Time) string {
	return uid + "\x00" + recurrenceID.UTC().Format(time.RFC3339)
}

func propValue(comp *ical.Component, name string) string {
	if prop := comp.Props.Get(name); prop != nil {
		return prop.Value
	}
	return ""
}

func cloneProps(props []ical.Prop) []ical.Prop {
	cloned := make([]ical.Prop, len(props))
	for i := range props {
		cloned[i] = ical.Prop{Name: props[i].Name, Value: props[i].Value}
		if props[i].Params != nil {
			cloned[i].Params = make(ical.Params, len(props[i].Params))
			for k, v := range props[i].Params {
				cloned[i].Params[k] = append([]string(nil), v...)
			}
		}
	}
	return cloned
}

func cloneComponent(comp *ical.Component) *ical.Component {
	out := &ical.Component{Name: comp.Name, Props: make(ical.Props, len(comp.Props))}
	for name, props := range comp.Props {
		out.Props[name] = cloneProps(props)
	}
	for _, child := range comp.Children {
		out.Children = append(out.Children, cloneComponent(child))
	}
	return out
}

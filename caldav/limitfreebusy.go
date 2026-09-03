package caldav

import (
	"fmt"
	"strings"
	"time"

	"github.com/emersion/go-ical"
)

// limitFreeBusyCalendar returns cal with every VFREEBUSY reduced to the
// FREEBUSY property values that intersect the window (RFC 4791 §9.6.7). A
// FREEBUSY left without any values is dropped whole; everything else is kept
// as stored. The input is never mutated: a Backend commonly returns pointers
// into its own cache.
func limitFreeBusyCalendar(cal *ical.Calendar, window *calendarTimeWindow) (*ical.Calendar, error) {
	out := &ical.Calendar{Component: cloneComponent(cal.Component)}
	for _, child := range out.Children {
		if child.Name != ical.CompFreeBusy {
			continue
		}
		var kept []ical.Prop
		for _, prop := range child.Props[ical.PropFreeBusy] {
			values := make([]string, 0, 1)
			for _, value := range strings.Split(prop.Value, ",") {
				// FREEBUSY periods are UTC by definition (RFC 5545 §3.8.2.6).
				from, to, err := period(value, time.UTC)
				if err != nil {
					return nil, fmt.Errorf("caldav: stored FREEBUSY holds an unparseable period: %w", err)
				}
				if intervalOverlaps(window.Start, window.End, from, to) {
					values = append(values, value)
				}
			}
			if len(values) == 0 {
				continue
			}
			prop.Value = strings.Join(values, ",")
			kept = append(kept, prop)
		}
		if len(kept) == 0 {
			child.Props.Del(ical.PropFreeBusy)
			continue
		}
		child.Props[ical.PropFreeBusy] = kept
	}
	return out, nil
}

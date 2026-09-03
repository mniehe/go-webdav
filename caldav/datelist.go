package caldav

import (
	"maps"
	"strings"

	"github.com/emersion/go-ical"
)

const dateListSeparator = ","

var dateListProps = []string{ical.PropExceptionDates, ical.PropRecurrenceDates}

// splitDateLists returns a copy of the component whose EXDATE and RDATE
// properties each carry a single value. RFC 5545 §3.8.5 lets one such property
// hold a comma-separated list, which go-ical reads as one date-time and then
// rejects, so callers must split before reading a recurrence.
func splitDateLists(comp *ical.Component) *ical.Component {
	split := *comp
	split.Props = maps.Clone(comp.Props)
	for _, name := range dateListProps {
		props := comp.Props[name]
		if len(props) == 0 {
			continue
		}
		split.Props[name] = splitPropValues(props)
	}
	return &split
}

func splitPropValues(props []ical.Prop) []ical.Prop {
	split := make([]ical.Prop, 0, len(props))
	for _, prop := range props {
		for _, value := range strings.Split(prop.Value, dateListSeparator) {
			single := prop
			single.Value = value
			split = append(split, single)
		}
	}
	return split
}

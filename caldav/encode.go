package caldav

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/emersion/go-ical"
)

// encodeCalendarData writes cal in iCalendar form without go-ical's structural
// validation. RFC 4791 §9.6 says the calendar-data a report returns may be
// invalid per its media type, which is what shaping produces: an expansion or a
// limit that leaves no component behind, a projection that drops a property the
// format requires. Refusing to encode those fails the whole report instead of
// answering the request that was made.
//
// Stored bytes never travel through here — a GET serves them verbatim, and a
// PUT is validated on the way in.
func encodeCalendarData(w io.Writer, cal *ical.Calendar) error {
	if cal == nil || cal.Component == nil {
		return fmt.Errorf("caldav: calendar-data has no root component")
	}
	return encodeComponent(w, cal.Component)
}

func encodeComponent(w io.Writer, comp *ical.Component) error {
	if err := encodeProp(w, &ical.Prop{Name: "BEGIN", Value: comp.Name}); err != nil {
		return err
	}

	names := make([]string, 0, len(comp.Props))
	for name := range comp.Props {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		props := comp.Props[name]
		for i := range props {
			if err := encodeProp(w, &props[i]); err != nil {
				return err
			}
		}
	}

	for _, child := range comp.Children {
		if err := encodeComponent(w, child); err != nil {
			return err
		}
	}
	return encodeProp(w, &ical.Prop{Name: "END", Value: comp.Name})
}

func encodeProp(w io.Writer, prop *ical.Prop) error {
	line := prop.Name

	names := make([]string, 0, len(prop.Params))
	for name := range prop.Params {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		quoted := make([]string, 0, len(prop.Params[name]))
		for _, value := range prop.Params[name] {
			if strings.ContainsRune(value, '"') {
				return fmt.Errorf("caldav: parameter %s of %s holds a double quote", name, prop.Name)
			}
			if strings.ContainsAny(value, ";:,") {
				value = `"` + value + `"`
			}
			quoted = append(quoted, value)
		}
		line += ";" + name + "=" + strings.Join(quoted, ",")
	}

	if strings.ContainsAny(prop.Value, "\r\n") {
		return fmt.Errorf("caldav: property %s holds a line break", prop.Name)
	}
	line += ":" + prop.Value + "\r\n"

	_, err := io.WriteString(w, line)
	return err
}

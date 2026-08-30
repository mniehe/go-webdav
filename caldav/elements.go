package caldav

import (
	"encoding/xml"

	"github.com/mniehe/davkit/internal"
)

// namespace is the CalDAV namespace (RFC 4791 §4).
const namespace = "urn:ietf:params:xml:ns:caldav"

// appleICalNamespace holds calendar-color and calendar-order. Neither is in any
// RFC; both are read by every mainstream client, so a calendar without them
// shows up grey and in the wrong order.
const appleICalNamespace = "http://apple.com/ns/ical/"

var (
	calendarName = xml.Name{Space: namespace, Local: "calendar"}

	calendarHomeSetName               = xml.Name{Space: namespace, Local: "calendar-home-set"}
	calendarDescriptionName           = xml.Name{Space: namespace, Local: "calendar-description"}
	calendarTimezoneName              = xml.Name{Space: namespace, Local: "calendar-timezone"}
	supportedCalendarDataName         = xml.Name{Space: namespace, Local: "supported-calendar-data"}
	supportedCalendarComponentSetName = xml.Name{Space: namespace, Local: "supported-calendar-component-set"}
	maxResourceSizeName               = xml.Name{Space: namespace, Local: "max-resource-size"}
	calendarDataName                  = xml.Name{Space: namespace, Local: "calendar-data"}

	calendarColorName = xml.Name{Space: appleICalNamespace, Local: "calendar-color"}
	calendarOrderName = xml.Name{Space: appleICalNamespace, Local: "calendar-order"}
)

// calendarHomeSet is RFC 4791 §6.2.1.
type calendarHomeSet struct {
	XMLName xml.Name      `xml:"urn:ietf:params:xml:ns:caldav calendar-home-set"`
	Href    internal.Href `xml:"DAV: href"`
}

// calendarDescription is RFC 4791 §5.2.1.
type calendarDescription struct {
	XMLName     xml.Name `xml:"urn:ietf:params:xml:ns:caldav calendar-description"`
	Description string   `xml:",chardata"`
}

// calendarTimezone is RFC 4791 §5.2.2.
type calendarTimezone struct {
	XMLName  xml.Name `xml:"urn:ietf:params:xml:ns:caldav calendar-timezone"`
	Timezone string   `xml:",chardata"`
}

// supportedCalendarComponentSet is RFC 4791 §5.2.3.
type supportedCalendarComponentSet struct {
	XMLName xml.Name        `xml:"urn:ietf:params:xml:ns:caldav supported-calendar-component-set"`
	Comp    []componentName `xml:"comp"`
}

type componentName struct {
	XMLName xml.Name `xml:"urn:ietf:params:xml:ns:caldav comp"`
	Name    string   `xml:"name,attr"`
}

// supportedCalendarData is RFC 4791 §5.2.4.
type supportedCalendarData struct {
	XMLName xml.Name           `xml:"urn:ietf:params:xml:ns:caldav supported-calendar-data"`
	Types   []calendarDataType `xml:"calendar-data"`
}

type calendarDataType struct {
	XMLName     xml.Name `xml:"urn:ietf:params:xml:ns:caldav calendar-data"`
	ContentType string   `xml:"content-type,attr"`
	Version     string   `xml:"version,attr"`
}

// maxResourceSize is RFC 4791 §5.2.5.
type maxResourceSize struct {
	XMLName xml.Name `xml:"urn:ietf:params:xml:ns:caldav max-resource-size"`
	Size    int64    `xml:",chardata"`
}

type calendarColor struct {
	XMLName xml.Name `xml:"http://apple.com/ns/ical/ calendar-color"`
	Color   string   `xml:",chardata"`
}

type calendarOrder struct {
	XMLName xml.Name `xml:"http://apple.com/ns/ical/ calendar-order"`
	Order   int      `xml:",chardata"`
}

// calendarData carries an item's bytes back to the client (RFC 4791 §9.6).
type calendarData struct {
	XMLName xml.Name `xml:"urn:ietf:params:xml:ns:caldav calendar-data"`
	Data    []byte   `xml:",chardata"`
}

// componentNames renders the kinds a calendar accepts as the iCalendar
// component names clients match on.
func componentNames(kinds ItemKinds) []componentName {
	names := make([]componentName, 0, 4)
	for _, kind := range kinds.Kinds() {
		names = append(names, componentName{Name: componentFor(kind)})
	}
	return names
}

func componentFor(kind ItemKind) string {
	switch kind {
	case Event:
		return "VEVENT"
	case Task:
		return "VTODO"
	case Note:
		return "VJOURNAL"
	case Availability:
		return "VFREEBUSY"
	default:
		return ""
	}
}

// PUT preconditions (RFC 4791 §5.3.2.1) and the REPORT vocabulary.
var (
	supportedCollationName = xml.Name{Space: namespace, Local: "supported-collation"}

	validCalendarDataName          = xml.Name{Space: namespace, Local: "valid-calendar-data"}
	validCalendarObjectName        = xml.Name{Space: namespace, Local: "valid-calendar-object-resource"}
	supportedCalendarComponentName = xml.Name{Space: namespace, Local: "supported-calendar-component"}
	noUIDConflictName              = xml.Name{Space: namespace, Local: "no-uid-conflict"}
)

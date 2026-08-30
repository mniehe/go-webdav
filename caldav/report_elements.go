package caldav

import (
	"encoding/xml"
	"fmt"
	"time"

	"github.com/mniehe/davkit/internal"
)

// The REPORT wire vocabulary, transcribed from the previous package.

var (
	calendarQueryName    = xml.Name{Space: namespace, Local: "calendar-query"}
	calendarMultigetName = xml.Name{Space: namespace, Local: "calendar-multiget"}
	freeBusyQueryName    = xml.Name{Space: namespace, Local: "free-busy-query"}
	syncCollectionName   = xml.Name{Space: "DAV:", Local: "sync-collection"}
)

// https://tools.ietf.org/html/rfc4791#section-9.11
type freeBusyQuery struct {
	XMLName   xml.Name   `xml:"urn:ietf:params:xml:ns:caldav free-busy-query"`
	TimeRange *timeRange `xml:"time-range"`
}

// https://tools.ietf.org/html/rfc4791#section-9.5
type calendarQueryReq struct {
	XMLName  xml.Name       `xml:"urn:ietf:params:xml:ns:caldav calendar-query"`
	Prop     *internal.Prop `xml:"DAV: prop,omitempty"`
	AllProp  *struct{}      `xml:"DAV: allprop,omitempty"`
	PropName *struct{}      `xml:"DAV: propname,omitempty"`
	Filter   filter         `xml:"filter"`
	Timezone string         `xml:"urn:ietf:params:xml:ns:caldav timezone"`
}

// https://tools.ietf.org/html/rfc4791#section-9.10
type calendarMultiget struct {
	XMLName  xml.Name        `xml:"urn:ietf:params:xml:ns:caldav calendar-multiget"`
	Hrefs    []internal.Href `xml:"DAV: href"`
	Prop     *internal.Prop  `xml:"DAV: prop,omitempty"`
	AllProp  *struct{}       `xml:"DAV: allprop,omitempty"`
	PropName *struct{}       `xml:"DAV: propname,omitempty"`
}

// https://tools.ietf.org/html/rfc4791#section-9.7
type filter struct {
	XMLName    xml.Name      `xml:"urn:ietf:params:xml:ns:caldav filter"`
	CompFilter compFilterReq `xml:"comp-filter"`
}

// https://tools.ietf.org/html/rfc4791#section-9.7.1
type compFilterReq struct {
	XMLName      xml.Name        `xml:"urn:ietf:params:xml:ns:caldav comp-filter"`
	Name         string          `xml:"name,attr"`
	IsNotDefined *struct{}       `xml:"is-not-defined,omitempty"`
	TimeRange    *timeRange      `xml:"time-range,omitempty"`
	PropFilters  []propFilterReq `xml:"prop-filter,omitempty"`
	CompFilters  []compFilterReq `xml:"comp-filter,omitempty"`
}

// https://tools.ietf.org/html/rfc4791#section-9.7.2
type propFilterReq struct {
	XMLName      xml.Name         `xml:"urn:ietf:params:xml:ns:caldav prop-filter"`
	Name         string           `xml:"name,attr"`
	IsNotDefined *struct{}        `xml:"is-not-defined,omitempty"`
	TimeRange    *timeRange       `xml:"time-range,omitempty"`
	TextMatch    *textMatchReq    `xml:"text-match,omitempty"`
	ParamFilter  []paramFilterReq `xml:"param-filter,omitempty"`
}

// https://tools.ietf.org/html/rfc4791#section-9.7.3
type paramFilterReq struct {
	XMLName      xml.Name      `xml:"urn:ietf:params:xml:ns:caldav param-filter"`
	Name         string        `xml:"name,attr"`
	IsNotDefined *struct{}     `xml:"is-not-defined,omitempty"`
	TextMatch    *textMatchReq `xml:"text-match,omitempty"`
}

// https://tools.ietf.org/html/rfc4791#section-9.7.5
type textMatchReq struct {
	XMLName         xml.Name        `xml:"urn:ietf:params:xml:ns:caldav text-match"`
	Text            string          `xml:",chardata"`
	Collation       string          `xml:"collation,attr,omitempty"`
	NegateCondition negateCondition `xml:"negate-condition,attr,omitempty"`
}

type negateCondition bool

func (nc *negateCondition) UnmarshalText(b []byte) error {
	switch s := string(b); s {
	case "yes":
		*nc = true
	case "no":
		*nc = false
	default:
		return fmt.Errorf("caldav: invalid negate-condition value: %q", s)
	}
	return nil
}

func (nc negateCondition) MarshalText() ([]byte, error) {
	if nc {
		return []byte("yes"), nil
	}
	return nil, nil
}

// https://tools.ietf.org/html/rfc4791#section-9.9
type timeRange struct {
	XMLName xml.Name        `xml:"urn:ietf:params:xml:ns:caldav time-range"`
	Start   dateWithUTCTime `xml:"start,attr,omitempty"`
	End     dateWithUTCTime `xml:"end,attr,omitempty"`
}

const dateWithUTCTimeLayout = "20060102T150405Z"

// dateWithUTCTime is the "date with UTC time" format defined in RFC 5545 page
// 34.
type dateWithUTCTime time.Time

func (t *dateWithUTCTime) UnmarshalText(b []byte) error {
	tt, err := time.Parse(dateWithUTCTimeLayout, string(b))
	if err != nil {
		return err
	}
	*t = dateWithUTCTime(tt)
	return nil
}

func (t *dateWithUTCTime) MarshalText() ([]byte, error) {
	s := time.Time(*t).Format(dateWithUTCTimeLayout)
	return []byte(s), nil
}

// Request variant of https://tools.ietf.org/html/rfc4791#section-9.6
type calendarDataReq struct {
	XMLName xml.Name `xml:"urn:ietf:params:xml:ns:caldav calendar-data"`
	Comp    *comp    `xml:"comp,omitempty"`
	Expand  *expand  `xml:"expand,omitempty"`
	// TODO: limit-recurrence-set, limit-freebusy-set
}

// https://tools.ietf.org/html/rfc4791#section-9.6.1
type comp struct {
	XMLName xml.Name `xml:"urn:ietf:params:xml:ns:caldav comp"`
	Name    string   `xml:"name,attr"`

	Allprop *struct{} `xml:"allprop,omitempty"`
	Prop    []prop    `xml:"prop,omitempty"`

	Allcomp *struct{} `xml:"allcomp,omitempty"`
	Comp    []comp    `xml:"comp,omitempty"`
}

type expand struct {
	XMLName xml.Name        `xml:"urn:ietf:params:xml:ns:caldav expand"`
	Start   dateWithUTCTime `xml:"start,attr"`
	End     dateWithUTCTime `xml:"end,attr"`
}

// https://tools.ietf.org/html/rfc4791#section-9.6.4
type prop struct {
	XMLName xml.Name `xml:"urn:ietf:params:xml:ns:caldav prop"`
	Name    string   `xml:"name,attr"`
	// TODO: novalue
}

type reportReq struct {
	Query          *calendarQueryReq
	Multiget       *calendarMultiget
	FreeBusy       *freeBusyQuery
	SyncCollection *internal.SyncCollectionQuery
}

func (r *reportReq) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	var v interface{}
	switch start.Name {
	case calendarQueryName:
		r.Query = &calendarQueryReq{}
		v = r.Query
	case calendarMultigetName:
		r.Multiget = &calendarMultiget{}
		v = r.Multiget
	case freeBusyQueryName:
		r.FreeBusy = &freeBusyQuery{}
		v = r.FreeBusy
	case syncCollectionName:
		r.SyncCollection = &internal.SyncCollectionQuery{}
		v = r.SyncCollection
	default:
		return fmt.Errorf("caldav: unsupported REPORT root %q %q", start.Name.Space, start.Name.Local)
	}

	return d.DecodeElement(v, &start)
}

// mkcalendarReq is RFC 4791 §5.3.1's request body.
type mkcalendarReq struct {
	XMLName xml.Name      `xml:"urn:ietf:params:xml:ns:caldav mkcalendar"`
	Prop    internal.Prop `xml:"set>prop"`
}

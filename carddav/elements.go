package carddav

import (
	"encoding/xml"
	"fmt"

	"github.com/mniehe/davkit/internal"
)

// namespace is the CardDAV namespace (RFC 6352 §3).
const namespace = "urn:ietf:params:xml:ns:carddav"

// vcardMediaType is what an address object is served and accepted as
// (RFC 6350 §10.1).
const vcardMediaType = "text/vcard; charset=utf-8"

var (
	addressbookName = xml.Name{Space: namespace, Local: "addressbook"}

	addressBookHomeSetName     = xml.Name{Space: namespace, Local: "addressbook-home-set"}
	addressBookDescriptionName = xml.Name{Space: namespace, Local: "addressbook-description"}
	supportedAddressDataName   = xml.Name{Space: namespace, Local: "supported-address-data"}
	maxResourceSizeName        = xml.Name{Space: namespace, Local: "max-resource-size"}
	addressDataName            = xml.Name{Space: namespace, Local: "address-data"}

	// The PUT preconditions of RFC 6352 §6.3.2.1.
	validAddressDataName = xml.Name{Space: namespace, Local: "valid-address-data"}
	noUIDConflictName    = xml.Name{Space: namespace, Local: "no-uid-conflict"}

	addressBookQueryName    = xml.Name{Space: namespace, Local: "addressbook-query"}
	addressBookMultigetName = xml.Name{Space: namespace, Local: "addressbook-multiget"}
	syncCollectionName      = xml.Name{Space: "DAV:", Local: "sync-collection"}

	// RFC 6352 §8.6.2 precondition for an unusable text-match collation.
	supportedCollationName = xml.Name{Space: namespace, Local: "supported-collation"}
)

// addressBookHomeSet is RFC 6352 §7.1.1.
type addressBookHomeSet struct {
	XMLName xml.Name      `xml:"urn:ietf:params:xml:ns:carddav addressbook-home-set"`
	Href    internal.Href `xml:"DAV: href"`
}

// addressBookDescription is RFC 6352 §6.2.1.
type addressBookDescription struct {
	XMLName     xml.Name `xml:"urn:ietf:params:xml:ns:carddav addressbook-description"`
	Description string   `xml:",chardata"`
}

// supportedAddressData is RFC 6352 §6.2.2.
type supportedAddressData struct {
	XMLName xml.Name          `xml:"urn:ietf:params:xml:ns:carddav supported-address-data"`
	Types   []addressDataType `xml:"address-data-type"`
}

type addressDataType struct {
	XMLName     xml.Name `xml:"urn:ietf:params:xml:ns:carddav address-data-type"`
	ContentType string   `xml:"content-type,attr"`
	Version     string   `xml:"version,attr"`
}

// maxResourceSize is RFC 6352 §6.2.3.
type maxResourceSize struct {
	XMLName xml.Name `xml:"urn:ietf:params:xml:ns:carddav max-resource-size"`
	Size    int64    `xml:",chardata"`
}

// addressData is RFC 6352 §10.4, as it appears in responses.
type addressData struct {
	XMLName xml.Name `xml:"urn:ietf:params:xml:ns:carddav address-data"`
	Data    []byte   `xml:",chardata"`
}

// The addressbook-query grammar of RFC 6352 §10. The Req suffix marks these as
// wire shapes; the match engine reads them after validateFilter has run.

// addressbookQueryReq is RFC 6352 §10.3.
type addressbookQueryReq struct {
	XMLName  xml.Name       `xml:"urn:ietf:params:xml:ns:carddav addressbook-query"`
	Prop     *internal.Prop `xml:"DAV: prop,omitempty"`
	AllProp  *struct{}      `xml:"DAV: allprop,omitempty"`
	PropName *struct{}      `xml:"DAV: propname,omitempty"`
	Filter   filterReq      `xml:"filter"`
	Limit    *limitReq      `xml:"limit,omitempty"`
}

// filterReq is RFC 6352 §10.5.
type filterReq struct {
	XMLName xml.Name        `xml:"urn:ietf:params:xml:ns:carddav filter"`
	Test    filterTest      `xml:"test,attr,omitempty"`
	Props   []propFilterReq `xml:"prop-filter"`
}

type filterTest string

const (
	filterAnyOf filterTest = "anyof"
	filterAllOf filterTest = "allof"
)

func (ft *filterTest) UnmarshalText(b []byte) error {
	switch filterTest(b) {
	case filterAnyOf, filterAllOf:
		*ft = filterTest(b)
		return nil
	default:
		return fmt.Errorf("carddav: invalid filter test value: %q", string(b))
	}
}

// propFilterReq is RFC 6352 §10.5.1.
type propFilterReq struct {
	XMLName xml.Name   `xml:"urn:ietf:params:xml:ns:carddav prop-filter"`
	Name    string     `xml:"name,attr"`
	Test    filterTest `xml:"test,attr,omitempty"`

	IsNotDefined *struct{}        `xml:"is-not-defined,omitempty"`
	TextMatches  []textMatchReq   `xml:"text-match,omitempty"`
	Params       []paramFilterReq `xml:"param-filter,omitempty"`
}

// textMatchReq is RFC 6352 §10.5.4.
type textMatchReq struct {
	XMLName         xml.Name  `xml:"urn:ietf:params:xml:ns:carddav text-match"`
	Text            string    `xml:",chardata"`
	Collation       string    `xml:"collation,attr,omitempty"`
	NegateCondition yesNo     `xml:"negate-condition,attr,omitempty"`
	MatchType       matchType `xml:"match-type,attr,omitempty"`
}

// yesNo is the (yes | no) attribute type RFC 6352 uses for negate-condition
// and novalue.
type yesNo bool

func (v *yesNo) UnmarshalText(b []byte) error {
	switch s := string(b); s {
	case "yes":
		*v = true
	case "no":
		*v = false
	default:
		return fmt.Errorf("carddav: invalid yes/no attribute value: %q", s)
	}
	return nil
}

type matchType string

const (
	matchEquals     matchType = "equals"
	matchContains   matchType = "contains"
	matchStartsWith matchType = "starts-with"
	matchEndsWith   matchType = "ends-with"
)

func (mt *matchType) UnmarshalText(b []byte) error {
	switch matchType(b) {
	case matchEquals, matchContains, matchStartsWith, matchEndsWith:
		*mt = matchType(b)
		return nil
	default:
		return fmt.Errorf("carddav: invalid match type value: %q", string(b))
	}
}

// paramFilterReq is RFC 6352 §10.5.2.
type paramFilterReq struct {
	XMLName      xml.Name      `xml:"urn:ietf:params:xml:ns:carddav param-filter"`
	Name         string        `xml:"name,attr"`
	IsNotDefined *struct{}     `xml:"is-not-defined"`
	TextMatch    *textMatchReq `xml:"text-match"`
}

// limitReq is RFC 6352 §10.6.
type limitReq struct {
	XMLName  xml.Name `xml:"urn:ietf:params:xml:ns:carddav limit"`
	NResults uint     `xml:"nresults"`
}

// addressbookMultigetReq is RFC 6352 §8.7.
type addressbookMultigetReq struct {
	XMLName  xml.Name        `xml:"urn:ietf:params:xml:ns:carddav addressbook-multiget"`
	Hrefs    []internal.Href `xml:"DAV: href"`
	Prop     *internal.Prop  `xml:"DAV: prop,omitempty"`
	AllProp  *struct{}       `xml:"DAV: allprop,omitempty"`
	PropName *struct{}       `xml:"DAV: propname,omitempty"`
}

// addressDataReq is the address-data element as a request (RFC 6352 §10.4):
// which properties to project and, through the attributes, which vCard version
// to serve.
type addressDataReq struct {
	XMLName     xml.Name  `xml:"urn:ietf:params:xml:ns:carddav address-data"`
	ContentType string    `xml:"content-type,attr,omitempty"`
	Version     string    `xml:"version,attr,omitempty"`
	Props       []propReq `xml:"prop"`
	Allprop     *struct{} `xml:"allprop"`
}

// propReq is RFC 6352 §10.4.2.
type propReq struct {
	XMLName xml.Name `xml:"urn:ietf:params:xml:ns:carddav prop"`
	Name    string   `xml:"name,attr"`
	NoValue yesNo    `xml:"novalue,attr,omitempty"`
}

type reportReq struct {
	Query          *addressbookQueryReq
	Multiget       *addressbookMultigetReq
	SyncCollection *internal.SyncCollectionQuery
}

func (r *reportReq) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	var v interface{}
	switch start.Name {
	case addressBookQueryName:
		r.Query = &addressbookQueryReq{}
		v = r.Query
	case addressBookMultigetName:
		r.Multiget = &addressbookMultigetReq{}
		v = r.Multiget
	case syncCollectionName:
		r.SyncCollection = &internal.SyncCollectionQuery{}
		v = r.SyncCollection
	default:
		return fmt.Errorf("carddav: unsupported REPORT root %q %q", start.Name.Space, start.Name.Local)
	}
	return d.DecodeElement(v, &start)
}

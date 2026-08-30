package internal

import (
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const Namespace = "DAV:"

// CalendarServerNamespace is the namespace of the CalendarServer extensions
// (https://github.com/apple/ccs-calendarserver/tree/master/doc/Extensions),
// notably the getctag property shared by CalDAV and CardDAV clients.
const CalendarServerNamespace = "http://calendarserver.org/ns/"

var (
	ResourceTypeName     = xml.Name{Space: Namespace, Local: "resourcetype"}
	DisplayNameName      = xml.Name{Space: Namespace, Local: "displayname"}
	GetContentLengthName = xml.Name{Space: Namespace, Local: "getcontentlength"}
	GetContentTypeName   = xml.Name{Space: Namespace, Local: "getcontenttype"}
	GetLastModifiedName  = xml.Name{Space: Namespace, Local: "getlastmodified"}
	GetETagName          = xml.Name{Space: Namespace, Local: "getetag"}

	CollectionName = xml.Name{Space: Namespace, Local: "collection"}
	PrincipalName  = xml.Name{Space: Namespace, Local: "principal"}

	CurrentUserPrincipalName    = xml.Name{Space: Namespace, Local: "current-user-principal"}
	CurrentUserPrivilegeSetName = xml.Name{Space: Namespace, Local: "current-user-privilege-set"}
	OwnerName                   = xml.Name{Space: Namespace, Local: "owner"}
	SupportedPrivilegeSetName   = xml.Name{Space: Namespace, Local: "supported-privilege-set"}
	ACLName                     = xml.Name{Space: Namespace, Local: "acl"}

	SyncTokenName      = xml.Name{Space: Namespace, Local: "sync-token"}
	GetCTagName        = xml.Name{Space: CalendarServerNamespace, Local: "getctag"}
	ValidSyncTokenName = xml.Name{Space: Namespace, Local: "valid-sync-token"}

	SupportedReportSetName = xml.Name{Space: Namespace, Local: "supported-report-set"}

	PrincipalURLName           = xml.Name{Space: Namespace, Local: "principal-URL"}
	PrincipalCollectionSetName = xml.Name{Space: Namespace, Local: "principal-collection-set"}
)

type Status struct {
	Code int
	Text string
}

func (s *Status) MarshalText() ([]byte, error) {
	text := s.Text
	if text == "" {
		text = http.StatusText(s.Code)
	}
	return []byte(fmt.Sprintf("HTTP/1.1 %v %v", s.Code, text)), nil
}

func (s *Status) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		return nil
	}

	parts := strings.SplitN(string(b), " ", 3)
	if len(parts) != 3 {
		return fmt.Errorf("webdav: invalid HTTP status %q: expected 3 fields", string(b))
	}
	code, err := strconv.Atoi(parts[1])
	if err != nil {
		return fmt.Errorf("webdav: invalid HTTP status %q: failed to parse code: %v", string(b), err)
	}

	s.Code = code
	s.Text = parts[2]
	return nil
}

func (s *Status) Err() error {
	if s == nil {
		return nil
	}

	// TODO: handle 2xx, 3xx
	if s.Code != http.StatusOK {
		return &HTTPError{Code: s.Code}
	}
	return nil
}

type Href url.URL

func (h *Href) String() string {
	u := (*url.URL)(h)
	return u.String()
}

func (h *Href) MarshalText() ([]byte, error) {
	return []byte(h.String()), nil
}

func (h *Href) UnmarshalText(b []byte) error {
	u, err := url.Parse(string(b))
	if err != nil {
		return err
	}
	*h = Href(*u)
	return nil
}

// https://tools.ietf.org/html/rfc4918#section-14.16
type MultiStatus struct {
	XMLName             xml.Name   `xml:"DAV: multistatus"`
	Responses           []Response `xml:"response"`
	ResponseDescription string     `xml:"responsedescription,omitempty"`
	SyncToken           string     `xml:"sync-token,omitempty"`
}

func NewMultiStatus(resps ...Response) *MultiStatus {
	return &MultiStatus{Responses: resps}
}

// https://tools.ietf.org/html/rfc4918#section-14.24
type Response struct {
	XMLName             xml.Name   `xml:"DAV: response"`
	Hrefs               []Href     `xml:"href"`
	PropStats           []PropStat `xml:"propstat,omitempty"`
	ResponseDescription string     `xml:"responsedescription,omitempty"`
	Status              *Status    `xml:"status,omitempty"`
	Error               *Error     `xml:"error,omitempty"`
	Location            *Location  `xml:"location,omitempty"`
}

func NewOKResponse(path string) *Response {
	href := Href{Path: path}
	return &Response{
		Hrefs:  []Href{href},
		Status: &Status{Code: http.StatusOK},
	}
}

func NewErrorResponse(path string, err error) *Response {
	code := http.StatusInternalServerError
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		code = httpErr.Code
	}

	var errElt *Error
	errors.As(err, &errElt)

	href := Href{Path: path}
	return &Response{
		Hrefs:               []Href{href},
		Status:              &Status{Code: code},
		ResponseDescription: safeErrorText(code, err),
		Error:               errElt,
	}
}

func (resp *Response) Err() error {
	if resp.Status == nil || resp.Status.Code/100 == 2 {
		return nil
	}

	var err error
	if resp.Error != nil {
		err = resp.Error
	}
	if resp.ResponseDescription != "" {
		if err != nil {
			err = fmt.Errorf("%v (%w)", resp.ResponseDescription, err)
		} else {
			err = fmt.Errorf("%v", resp.ResponseDescription)
		}
	}

	err = &HTTPError{Code: resp.Status.Code, Err: err}
	if len(resp.Hrefs) == 0 {
		return err
	}

	hrefErrs := make([]error, len(resp.Hrefs))
	for i := range resp.Hrefs {
		hrefErrs[i] = &HrefError{Href: url.URL(resp.Hrefs[i]), Err: err}
	}
	return errors.Join(hrefErrs...)
}

func (resp *Response) Path() (string, error) {
	err := resp.Err()
	var path string
	if len(resp.Hrefs) == 1 {
		path = resp.Hrefs[0].Path
	} else if err == nil {
		err = fmt.Errorf("webdav: malformed response: expected exactly one href element, got %v", len(resp.Hrefs))
	}
	return path, err
}

func (resp *Response) DecodeProp(values ...interface{}) error {
	for _, v := range values {
		// TODO wrap errors with more context (XML name)
		name, err := valueXMLName(v)
		if err != nil {
			return err
		}
		if err := resp.Err(); err != nil {
			return newPropError(name, err)
		}
		for i := range resp.PropStats {
			propstat := &resp.PropStats[i]
			raw := propstat.Prop.Get(name)
			if raw == nil {
				continue
			}
			if err := propstat.Status.Err(); err != nil {
				return newPropError(name, err)
			}
			if err := raw.Decode(v); err != nil {
				return newPropError(name, err)
			}
			return nil
		}
		return newPropError(name, &HTTPError{
			Code: http.StatusNotFound,
			Err:  fmt.Errorf("missing property"),
		})
	}

	return nil
}

func newPropError(name xml.Name, err error) error {
	return fmt.Errorf("property <%v %v>: %w", name.Space, name.Local, err)
}

func (resp *Response) EncodeProp(code int, v interface{}) error {
	raw, err := EncodeRawXMLElement(v)
	if err != nil {
		return err
	}

	for i := range resp.PropStats {
		propstat := &resp.PropStats[i]
		if propstat.Status.Code == code {
			propstat.Prop.Raw = append(propstat.Prop.Raw, *raw)
			return nil
		}
	}

	resp.PropStats = append(resp.PropStats, PropStat{
		Status: Status{Code: code},
		Prop:   Prop{Raw: []RawXMLValue{*raw}},
	})
	return nil
}

// https://tools.ietf.org/html/rfc4918#section-14.9
type Location struct {
	XMLName xml.Name `xml:"DAV: location"`
	Href    Href     `xml:"href"`
}

// https://tools.ietf.org/html/rfc4918#section-14.22
type PropStat struct {
	XMLName             xml.Name `xml:"DAV: propstat"`
	Prop                Prop     `xml:"prop"`
	Status              Status   `xml:"status"`
	ResponseDescription string   `xml:"responsedescription,omitempty"`
	Error               *Error   `xml:"error,omitempty"`
}

// https://tools.ietf.org/html/rfc4918#section-14.18
type Prop struct {
	XMLName xml.Name      `xml:"DAV: prop"`
	Raw     []RawXMLValue `xml:",any"`
}

func EncodeProp(values ...interface{}) (*Prop, error) {
	l := make([]RawXMLValue, len(values))
	for i, v := range values {
		raw, err := EncodeRawXMLElement(v)
		if err != nil {
			return nil, err
		}
		l[i] = *raw
	}
	return &Prop{Raw: l}, nil
}

func (p *Prop) Get(name xml.Name) *RawXMLValue {
	for i := range p.Raw {
		raw := &p.Raw[i]
		if n, ok := raw.XMLName(); ok && name == n {
			return raw
		}
	}
	return nil
}

func (p *Prop) Decode(v interface{}) error {
	name, err := valueXMLName(v)
	if err != nil {
		return err
	}

	raw := p.Get(name)
	if raw == nil {
		return HTTPErrorf(http.StatusNotFound, "missing property %s", name)
	}

	return raw.Decode(v)
}

// https://tools.ietf.org/html/rfc4918#section-14.20
type PropFind struct {
	XMLName  xml.Name  `xml:"DAV: propfind"`
	Prop     *Prop     `xml:"prop,omitempty"`
	AllProp  *struct{} `xml:"allprop,omitempty"`
	Include  *Include  `xml:"include,omitempty"`
	PropName *struct{} `xml:"propname,omitempty"`
}

func xmlNamesToRaw(names []xml.Name) []RawXMLValue {
	l := make([]RawXMLValue, len(names))
	for i, name := range names {
		l[i] = *NewRawXMLElement(name, nil, nil)
	}
	return l
}

func NewPropNamePropFind(names ...xml.Name) *PropFind {
	return &PropFind{Prop: &Prop{Raw: xmlNamesToRaw(names)}}
}

// https://tools.ietf.org/html/rfc4918#section-14.8
type Include struct {
	XMLName xml.Name      `xml:"DAV: include"`
	Raw     []RawXMLValue `xml:",any"`
}

// https://tools.ietf.org/html/rfc4918#section-15.9
type ResourceType struct {
	XMLName xml.Name      `xml:"DAV: resourcetype"`
	Raw     []RawXMLValue `xml:",any"`
}

func NewResourceType(names ...xml.Name) *ResourceType {
	return &ResourceType{Raw: xmlNamesToRaw(names)}
}

// https://tools.ietf.org/html/rfc3744#section-4.2
type PrincipalURL struct {
	XMLName xml.Name `xml:"DAV: principal-URL"`
	Href    Href     `xml:"href"`
}

// https://tools.ietf.org/html/rfc3744#section-5.8
type PrincipalCollectionSet struct {
	XMLName xml.Name `xml:"DAV: principal-collection-set"`
	Hrefs   []Href   `xml:"href"`
}

// ParseHref parses a URI reference for use as a DAV href. It accepts absolute
// URIs such as "mailto:user@example.com" as well as plain paths; assigning such
// a URI to Href.Path instead would emit it escaped as a relative path.
func ParseHref(s string) (*Href, error) {
	u, err := url.Parse(s)
	if err != nil {
		return nil, err
	}
	return (*Href)(u), nil
}

// https://tools.ietf.org/html/rfc3253#section-3.1.5
type SupportedReportSet struct {
	XMLName          xml.Name          `xml:"DAV: supported-report-set"`
	SupportedReports []SupportedReport `xml:"DAV: supported-report"`
}

// https://tools.ietf.org/html/rfc3253#section-3.1.5
type SupportedReport struct {
	XMLName xml.Name `xml:"DAV: supported-report"`
	Report  Report   `xml:"DAV: report"`
}

// https://tools.ietf.org/html/rfc3253#section-3.1.5
type Report struct {
	XMLName xml.Name      `xml:"DAV: report"`
	Raw     []RawXMLValue `xml:",any"`
}

// NewSupportedReportSet advertises the named REPORT types, in the order given.
func NewSupportedReportSet(names ...xml.Name) *SupportedReportSet {
	reports := make([]SupportedReport, len(names))
	for i, name := range names {
		reports[i] = SupportedReport{
			Report: Report{Raw: []RawXMLValue{*NewRawXMLElement(name, nil, nil)}},
		}
	}
	return &SupportedReportSet{SupportedReports: reports}
}

func (t *ResourceType) Is(name xml.Name) bool {
	for _, raw := range t.Raw {
		if n, ok := raw.XMLName(); ok && name == n {
			return true
		}
	}
	return false
}

// https://tools.ietf.org/html/rfc4918#section-15.4
type GetContentLength struct {
	XMLName xml.Name `xml:"DAV: getcontentlength"`
	Length  int64    `xml:",chardata"`
}

// https://tools.ietf.org/html/rfc4918#section-15.5
type GetContentType struct {
	XMLName xml.Name `xml:"DAV: getcontenttype"`
	Type    string   `xml:",chardata"`
}

type Time time.Time

func (t *Time) UnmarshalText(b []byte) error {
	tt, err := http.ParseTime(string(b))
	if err != nil {
		return err
	}
	*t = Time(tt)
	return nil
}

func (t *Time) MarshalText() ([]byte, error) {
	s := time.Time(*t).UTC().Format(http.TimeFormat)
	return []byte(s), nil
}

// https://tools.ietf.org/html/rfc4918#section-15.7
type GetLastModified struct {
	XMLName      xml.Name `xml:"DAV: getlastmodified"`
	LastModified Time     `xml:",chardata"`
}

// https://tools.ietf.org/html/rfc4918#section-15.6
type GetETag struct {
	XMLName xml.Name `xml:"DAV: getetag"`
	ETag    ETag     `xml:",chardata"`
}

type ETag string

func (etag *ETag) UnmarshalText(b []byte) error {
	// RFC 9110 §8.8.3: an entity-tag may carry a weak "W/" prefix. Accept it
	// (stripping the indicator) rather than rejecting the whole header.
	s := strings.TrimPrefix(string(b), "W/")
	unquoted, err := strconv.Unquote(s)
	if err != nil {
		return fmt.Errorf("webdav: failed to unquote ETag: %v", err)
	}
	*etag = ETag(unquoted)
	return nil
}

func (etag ETag) MarshalText() ([]byte, error) {
	return []byte(etag.String()), nil
}

func (etag ETag) String() string {
	return fmt.Sprintf("%q", string(etag))
}

// https://tools.ietf.org/html/rfc4918#section-14.5
type Error struct {
	XMLName xml.Name      `xml:"DAV: error"`
	Raw     []RawXMLValue `xml:",any"`
}

func (err *Error) Error() string {
	b, marshalErr := xml.Marshal(err)
	if marshalErr != nil {
		return ""
	}
	return string(b)
}

// https://tools.ietf.org/html/rfc4918#section-15.2
type DisplayName struct {
	XMLName xml.Name `xml:"DAV: displayname"`
	Name    string   `xml:",chardata"`
}

// https://tools.ietf.org/html/rfc5397#section-3
type CurrentUserPrincipal struct {
	XMLName         xml.Name  `xml:"DAV: current-user-principal"`
	Href            Href      `xml:"href,omitempty"`
	Unauthenticated *struct{} `xml:"unauthenticated,omitempty"`
}

// https://tools.ietf.org/html/rfc4918#section-14.19
type PropertyUpdate struct {
	XMLName xml.Name `xml:"DAV: propertyupdate"`
	Remove  []Remove `xml:"remove"`
	Set     []Set    `xml:"set"`

	// instructions records every change in document order, which Remove and Set
	// cannot: encoding/xml fills them field by field, discarding how the two
	// interleaved. RFC 4918 §9.2 makes PROPPATCH an explicit exception to the
	// rule that instruction order does not matter.
	instructions []PropUpdate
}

var (
	removeName = xml.Name{Space: Namespace, Local: "remove"}
	setName    = xml.Name{Space: Namespace, Local: "set"}
)

// UnmarshalXML implements xml.Unmarshaler, decoding the standard fields while
// also recording instruction order.
func (pu *PropertyUpdate) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*pu = PropertyUpdate{XMLName: start.Name}

	for {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		switch tok := tok.(type) {
		case xml.StartElement:
			switch tok.Name {
			case removeName:
				var remove Remove
				if err := d.DecodeElement(&remove, &tok); err != nil {
					return err
				}
				pu.Remove = append(pu.Remove, remove)
				pu.instructions = append(pu.instructions, propUpdates(&remove.Prop, true)...)
			case setName:
				var set Set
				if err := d.DecodeElement(&set, &tok); err != nil {
					return err
				}
				pu.Set = append(pu.Set, set)
				pu.instructions = append(pu.instructions, propUpdates(&set.Prop, false)...)
			default:
				if err := d.Skip(); err != nil {
					return err
				}
			}
		case xml.EndElement:
			return nil
		}
	}
}

// propUpdates lists the property changes one instruction carries. Non-element
// children (the whitespace between properties) are skipped.
func propUpdates(prop *Prop, remove bool) []PropUpdate {
	var updates []PropUpdate
	for i := range prop.Raw {
		name, ok := prop.Raw[i].XMLName()
		if !ok {
			continue
		}
		update := PropUpdate{Name: name, Remove: remove}
		if !remove {
			update.Value = &prop.Raw[i]
		}
		updates = append(updates, update)
	}
	return updates
}

// https://tools.ietf.org/html/rfc4918#section-14.23
type Remove struct {
	XMLName xml.Name `xml:"DAV: remove"`
	Prop    Prop     `xml:"prop"`
}

// https://tools.ietf.org/html/rfc4918#section-14.26
type Set struct {
	XMLName xml.Name `xml:"DAV: set"`
	Prop    Prop     `xml:"prop"`
}

// PropUpdate is one property change requested by a PROPPATCH.
type PropUpdate struct {
	Name xml.Name
	// Value is the requested new value, or nil when Remove is set.
	Value  *RawXMLValue
	Remove bool
}

// PropUpdatesFromProp lists the properties of a <prop> element as set
// instructions. MKCALENDAR and MKCOL carry a bare DAV:set rather than a full
// propertyupdate, but reject properties the same way PROPPATCH does.
func PropUpdatesFromProp(prop *Prop) []PropUpdate {
	return propUpdates(prop, false)
}

// Updates returns the requested property changes in document order.
func (pu *PropertyUpdate) Updates() []PropUpdate {
	return pu.instructions
}

// MaxPropValueSize bounds a collection property a client may set. These values
// are re-served on every PROPFIND of the collection, including the Depth:1 home
// set listing that clients fetch on connect, so an oversized one costs far more
// than the single request that set it.
const MaxPropValueSize = 1024

// DecodePropUpdate reads a <set> value into v, leaving v zeroed for a <remove>.
// It reports whether the value was usable.
func DecodePropUpdate(update *PropUpdate, v interface{}) bool {
	if update.Remove {
		return true
	}
	if update.Value == nil {
		return false
	}
	return update.Value.Decode(v) == nil
}

// NewPropPatchSuccess reports every requested property as applied.
func NewPropPatchSuccess(path string, updates []PropUpdate) (*Response, error) {
	return newPropPatchResponse(path, updates, nil)
}

// NewPropPatchFailure reports a PROPPATCH that was not applied. Properties named
// in rejected carry their own status; RFC 4918 §9.2 requires PROPPATCH to be
// atomic, so every other requested property is reported as 424 Failed
// Dependency rather than as a success.
func NewPropPatchFailure(path string, updates []PropUpdate, rejected map[xml.Name]int) (*Response, error) {
	if len(rejected) == 0 {
		return nil, fmt.Errorf("webdav: a failed PROPPATCH must reject at least one property")
	}
	return newPropPatchResponse(path, updates, rejected)
}

func newPropPatchResponse(path string, updates []PropUpdate, rejected map[xml.Name]int) (*Response, error) {
	// RFC 4918 §14.24: a response carries either a bare status or propstats,
	// never both, so this one is built without a response-level status.
	resp := &Response{Hrefs: []Href{{Path: path}}}
	for _, update := range updates {
		code := http.StatusOK
		if len(rejected) > 0 {
			code = http.StatusFailedDependency
			if rejectedCode, ok := rejected[update.Name]; ok {
				code = rejectedCode
			}
		}
		if err := resp.EncodeProp(code, NewRawXMLElement(update.Name, nil, nil)); err != nil {
			return nil, err
		}
	}
	return resp, nil
}

// https://tools.ietf.org/html/rfc6578#section-6.1
type SyncCollectionQuery struct {
	XMLName   xml.Name `xml:"DAV: sync-collection"`
	SyncToken string   `xml:"sync-token"`
	Limit     *Limit   `xml:"limit,omitempty"`
	SyncLevel string   `xml:"sync-level"`
	Prop      *Prop    `xml:"prop"`
}

// SyncToken is the DAV:sync-token property (RFC 6578 section 3), an opaque token
// naming the current state of a collection. It is distinct from the sync-token
// child of a multistatus, though servers typically report the same value.
type SyncToken struct {
	XMLName xml.Name `xml:"DAV: sync-token"`
	Token   string   `xml:",chardata"`
}

// GetCTag is the CalendarServer getctag property, a collection tag that changes
// whenever any member of the collection changes. Clients poll it to decide
// whether a full synchronisation is needed; it predates and complements
// DAV:sync-token.
type GetCTag struct {
	XMLName xml.Name `xml:"http://calendarserver.org/ns/ getctag"`
	CTag    string   `xml:",chardata"`
}

// NewInvalidSyncTokenError reports the DAV:valid-sync-token precondition (RFC
// 6578 section 3.6): the sync token supplied by the client is unrecognised or
// no longer serviceable, so it must restart from an empty token.
func NewInvalidSyncTokenError() error {
	elem := NewRawXMLElement(ValidSyncTokenName, nil, nil)
	return &HTTPError{
		Code: http.StatusForbidden,
		Err:  &Error{Raw: []RawXMLValue{*elem}},
	}
}

// https://tools.ietf.org/html/rfc5323#section-5.17
type Limit struct {
	XMLName  xml.Name `xml:"DAV: limit"`
	NResults uint     `xml:"nresults"`
}

// https://tools.ietf.org/html/rfc3744#section-5.4
type CurrentUserPrivilegeSet struct {
	XMLName   xml.Name    `xml:"DAV: current-user-privilege-set"`
	Privilege []Privilege `xml:"DAV: privilege"`
}

// Privilege names one privilege. RFC 3744 §5.4 reports each granted privilege
// in a DAV:privilege element of its own rather than listing several inside one,
// and the contents are open-ended so a server can report privileges from its own
// namespace.
type Privilege struct {
	XMLName xml.Name      `xml:"DAV: privilege"`
	Raw     []RawXMLValue `xml:",any"`
}

// NewPrivilege returns a DAV:privilege naming a single privilege in the DAV:
// namespace.
func NewPrivilege(name string) Privilege {
	return Privilege{Raw: []RawXMLValue{
		*NewRawXMLElement(xml.Name{Space: Namespace, Local: name}, nil, nil),
	}}
}

// NewCurrentUserPrivilegeSet returns the property reporting exactly the named
// privileges. Callers pass an already-expanded set: the property is defined as
// the privileges the server grants, and a client tests it for the one it needs.
func NewCurrentUserPrivilegeSet(names []string) *CurrentUserPrivilegeSet {
	set := &CurrentUserPrivilegeSet{Privilege: make([]Privilege, 0, len(names))}
	for _, name := range names {
		set.Privilege = append(set.Privilege, NewPrivilege(name))
	}
	return set
}

// Owner is the DAV:owner property (RFC 3744 §5.1): the principal that owns the
// resource. A sharee's client reads it to show who a shared collection belongs
// to.
type Owner struct {
	XMLName xml.Name `xml:"DAV: owner"`
	Href    *Href    `xml:"DAV: href,omitempty"`
}

// SupportedPrivilegeSet is the DAV:supported-privilege-set property (RFC 3744
// §5.3): the tree of privileges the resource understands, which is what makes
// the privileges reported elsewhere interpretable rather than magic.
type SupportedPrivilegeSet struct {
	XMLName            xml.Name             `xml:"DAV: supported-privilege-set"`
	SupportedPrivilege []SupportedPrivilege `xml:"DAV: supported-privilege"`
}

// SupportedPrivilege is one node of that tree. An abstract privilege cannot be
// granted on its own; it exists so the tree can describe the server's real
// structure (RFC 3744 §5.3).
type SupportedPrivilege struct {
	XMLName            xml.Name             `xml:"DAV: supported-privilege"`
	Privilege          Privilege            `xml:"DAV: privilege"`
	Abstract           *struct{}            `xml:"DAV: abstract,omitempty"`
	Description        Description          `xml:"DAV: description"`
	SupportedPrivilege []SupportedPrivilege `xml:"DAV: supported-privilege,omitempty"`
}

// Description is the human-readable label on a supported privilege.
type Description struct {
	XMLName xml.Name `xml:"DAV: description"`
	Lang    string   `xml:"xml:lang,attr,omitempty"`
	Text    string   `xml:",chardata"`
}

// newSupportedPrivilege builds one node of the DAV:supported-privilege-set tree.
func newSupportedPrivilege(name, description string, children ...SupportedPrivilege) SupportedPrivilege {
	return SupportedPrivilege{
		Privilege:          NewPrivilege(name),
		Description:        Description{Lang: "en", Text: description},
		SupportedPrivilege: children,
	}
}

// NewSupportedPrivilegeSet returns the privilege tree this server understands,
// mirroring the aggregation RFC 3744 §3 requires: DAV:all contains everything,
// and DAV:write contains write-content, write-properties, bind and unbind.
//
// DAV:all is marked abstract because it is never granted on its own; it names
// the whole set rather than a privilege a principal can hold by itself.
func NewSupportedPrivilegeSet() *SupportedPrivilegeSet {
	write := newSupportedPrivilege("write", "Write any object",
		newSupportedPrivilege("write-content", "Write resource content"),
		newSupportedPrivilege("write-properties", "Write properties"),
		newSupportedPrivilege("bind", "Add a member to a collection"),
		newSupportedPrivilege("unbind", "Remove a member from a collection"),
	)
	all := newSupportedPrivilege("all", "Any operation",
		newSupportedPrivilege("read", "Read any object"),
		write,
		newSupportedPrivilege("read-acl", "Read the access control list"),
		newSupportedPrivilege("write-acl", "Write the access control list"),
		newSupportedPrivilege("read-current-user-privilege-set", "Read the current user's privileges"),
	)
	all.Abstract = &struct{}{}
	return &SupportedPrivilegeSet{SupportedPrivilege: []SupportedPrivilege{all}}
}

// ACL is the DAV:acl property (RFC 3744 §5.5): the access control list of a
// resource. Reading it requires the DAV:read-acl privilege, because the list
// names every principal the resource is shared with.
type ACL struct {
	XMLName xml.Name `xml:"DAV: acl"`
	ACE     []ACE    `xml:"DAV: ace"`
}

// ACE is one entry of that list. Only DAV:grant is produced; see the ACE type in
// the root package for why deny entries are not.
type ACE struct {
	XMLName   xml.Name     `xml:"DAV: ace"`
	Principal ACEPrincipal `xml:"DAV: principal"`
	Grant     Grant        `xml:"DAV: grant"`
	Protected *struct{}    `xml:"DAV: protected,omitempty"`
	Inherited *Inherited   `xml:"DAV: inherited,omitempty"`
}

// ACEPrincipal names the principal an entry applies to: either an href, or one
// of the pseudo-principals of RFC 3744 §5.5.1.
type ACEPrincipal struct {
	XMLName xml.Name      `xml:"DAV: principal"`
	Href    *Href         `xml:"DAV: href,omitempty"`
	Raw     []RawXMLValue `xml:",any"`
}

// Grant carries the privileges an entry confers.
type Grant struct {
	XMLName   xml.Name    `xml:"DAV: grant"`
	Privilege []Privilege `xml:"DAV: privilege"`
}

// Inherited names the resource an entry is inherited from.
type Inherited struct {
	XMLName xml.Name `xml:"DAV: inherited"`
	Href    Href     `xml:"DAV: href"`
}

// NewACEPrincipal returns the principal element for an href, or for a
// pseudo-principal when href is empty.
func NewACEPrincipal(href, pseudo string) ACEPrincipal {
	if href != "" {
		return ACEPrincipal{Href: &Href{Path: href}}
	}
	if pseudo == "" {
		return ACEPrincipal{}
	}
	return ACEPrincipal{Raw: []RawXMLValue{
		*NewRawXMLElement(xml.Name{Space: Namespace, Local: pseudo}, nil, nil),
	}}
}

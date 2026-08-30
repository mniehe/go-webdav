package internal

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"
)

// CleanPath returns the canonical form of a request path, preserving a single
// trailing slash (which distinguishes a collection from an object). ok is false
// when p was not already canonical, so callers can reject a request rather than
// act on a path that differs from the one they validated.
func CleanPath(p string) (cleaned string, ok bool) {
	cleaned = path.Clean(p)
	if cleaned != "/" && strings.HasSuffix(p, "/") {
		cleaned += "/"
	}
	return cleaned, cleaned == p
}

// HasEncodedSeparator reports whether any segment of an escaped path decodes to
// text containing a separator.
//
// URL.Path arrives percent-decoded, so "/a/b%2Fc" and "/a/b/c" are the same
// string by the time a handler sees them, though the first names one resource
// and the second two. Rejecting is safer than resolving the ambiguity.
func HasEncodedSeparator(escapedPath string) bool {
	for _, segment := range strings.Split(escapedPath, "/") {
		unescaped, err := url.PathUnescape(segment)
		if err != nil {
			return true
		}
		if strings.Contains(unescaped, "/") {
			return true
		}
	}
	return false
}

// ChildHref cleans a client-supplied href and confirms it names a resource
// within the collection at collectionPath. It returns the canonical href, or
// ok=false when the href escapes the collection or is not an absolute
// same-origin path. Used to confine REPORT multiget hrefs (RFC 4791 §7.9,
// RFC 6352 §8.7).
//
// It takes the parsed href because href.Path alone has already discarded the two
// things the confinement rests on: the authority, and the escaped form.
func ChildHref(r *http.Request, collectionPath string, href *Href) (cleaned string, ok bool) {
	u := (*url.URL)(href)
	if u.Host != "" && !strings.EqualFold(u.Host, r.Host) {
		return "", false
	}
	if HasEncodedSeparator(u.EscapedPath()) {
		return "", false
	}
	if u.Path == "" {
		return "", false
	}
	cleaned = path.Clean(u.Path)
	if !strings.HasPrefix(cleaned, "/") {
		return "", false
	}
	prefix := strings.TrimSuffix(collectionPath, "/")
	if cleaned != prefix && !strings.HasPrefix(cleaned, prefix+"/") {
		return "", false
	}
	return cleaned, true
}

// CheckRequestPath rejects a path the server cannot act on unambiguously: not
// canonical, or hiding a separator once decoded. Every decision downstream is
// made on the decoded path, so this has to run before any of them.
func CheckRequestPath(r *http.Request) error {
	if _, ok := CleanPath(r.URL.Path); !ok {
		return HTTPErrorf(http.StatusBadRequest, "webdav: non-canonical request path")
	}
	if HasEncodedSeparator(r.URL.EscapedPath()) {
		return HTTPErrorf(http.StatusBadRequest, "webdav: encoded separator in request path")
	}
	return nil
}

// CheckDestination applies the same rules to a COPY or MOVE Destination, plus
// the origin check RFC 4918 §9.8.4 answers with 502.
func CheckDestination(r *http.Request, dest *Href) error {
	u := (*url.URL)(dest)
	if u.Host != "" && !strings.EqualFold(u.Host, r.Host) {
		return HTTPErrorf(http.StatusBadGateway, "webdav: Destination names another server")
	}
	// RFC 4918 §10.3: the Destination is an absolute URI or an absolute path.
	// path.Clean keeps a leading .. in a relative one, so canonicality alone would
	// pass a path that escapes the served collection.
	if !strings.HasPrefix(u.Path, "/") {
		return HTTPErrorf(http.StatusBadRequest, "webdav: Destination is not an absolute path")
	}
	if HasEncodedSeparator(u.EscapedPath()) {
		return HTTPErrorf(http.StatusBadRequest, "webdav: encoded separator in Destination")
	}
	if _, ok := CleanPath(u.Path); !ok {
		return HTTPErrorf(http.StatusBadRequest, "webdav: non-canonical Destination path")
	}
	return nil
}

// PrincipalCollectionPath returns the collection holding the principal at
// principalPath, which is where a client looks to enumerate principals.
func PrincipalCollectionPath(principalPath string) string {
	// path.Dir would return the principal itself for a trailing-slash path.
	trimmed := strings.TrimSuffix(principalPath, "/")
	if trimmed == "" {
		return "/"
	}
	dir := path.Dir(trimmed)
	if !strings.HasSuffix(dir, "/") {
		dir += "/"
	}
	return dir
}

func ServeError(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		code = httpErr.Code
	}

	var errElt *Error
	if errors.As(err, &errElt) {
		w.WriteHeader(code)
		ServeXML(w).Encode(errElt) //nolint:errcheck // best-effort response write
		return
	}

	http.Error(w, safeErrorText(code, err), code)
}

// safeErrorText returns a client-safe message. Only text the library wrote is
// returned verbatim; a Backend error at any status, not just 5xx, is reduced to
// its status text, leaving the cause for the server's logs.
func safeErrorText(code int, err error) string {
	if err != nil && hasSafeText(err) {
		return err.Error()
	}
	return http.StatusText(code)
}

func isContentXML(h http.Header) bool {
	t, _, _ := mime.ParseMediaType(h.Get("Content-Type")) //nolint:errcheck // a malformed media type yields an empty t, which is not XML
	return t == "application/xml" || t == "text/xml"
}

// MaxXMLBodySize bounds the request body accepted by DecodeXMLRequest. WebDAV
// request bodies are small; capping them keeps a hostile client from forcing
// unbounded reads and allocations through the XML decoder.
const MaxXMLBodySize = 1 << 20 // 1 MiB

// RequestNodeBudget sizes the body scan for a handler's multiget limit. A
// multiget spends roughly one node per href, so a raised or removed href limit
// has to raise the scan's budget with it, or the scan refuses what the handler
// was configured to accept. Follows the BoundHrefs limit convention.
func RequestNodeBudget(maxHrefs int) int {
	switch {
	case maxHrefs < 0:
		return -1
	case maxHrefs == 0:
		return MaxXMLNodes
	default:
		return MaxXMLNodes + maxHrefs
	}
}

// DecodeXMLRequestWithin decodes a request body whose node budget the caller
// sizes; a negative budget removes the node bound.
func DecodeXMLRequestWithin(r *http.Request, v interface{}, maxNodes int) error {
	return decodeXMLRequest(r, v, maxNodes)
}

func DecodeXMLRequest(r *http.Request, v interface{}) error {
	return decodeXMLRequest(r, v, MaxXMLNodes)
}

func decodeXMLRequest(r *http.Request, v interface{}, maxNodes int) error {
	if !isContentXML(r.Header) {
		return HTTPErrorf(http.StatusBadRequest, "webdav: expected application/xml request")
	}

	// Scanned before decoding: a later bound would reject only after the decoder
	// had already paid for the tree.
	body, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, MaxXMLBodySize))
	if err != nil {
		return SafeHTTPError(http.StatusBadRequest, err)
	}
	if err := scanXMLBody(body, maxNodes); err != nil {
		return err
	}
	if err := xml.NewDecoder(bytes.NewReader(body)).Decode(v); err != nil {
		return SafeHTTPError(http.StatusBadRequest, err)
	}
	return nil
}

// MaxPropsPerRequest bounds how many properties one PROPFIND or PROPPATCH may
// name. The server echoes every requested name back in the multistatus, so the
// property count multiplies the response; MaxXMLBodySize bounds the request but
// not that product.
const MaxPropsPerRequest = 1024

// MaxPropNameSize bounds a property name for the same reason. Go's XML encoder
// re-emits the namespace declaration on every element, so one long namespace
// URI declared once in the request is written back once per echoed property.
const MaxPropNameSize = 512

// MaxHrefsPerMultiget bounds how many resources one multiget REPORT may name.
// Each href is an independent backend read, so a request inside MaxXMLBodySize
// otherwise buys tens of thousands of storage operations.
const MaxHrefsPerMultiget = 1024

// BoundHrefs rejects a multiget naming more resources than the backend should be
// asked to read to answer one request. A zero limit means MaxHrefsPerMultiget; a
// negative one means the caller has taken the bound off.
func BoundHrefs(hrefs []Href, limit int) error {
	if limit == 0 {
		limit = MaxHrefsPerMultiget
	}
	if limit > 0 && len(hrefs) > limit {
		return NewPreconditionError(http.StatusInsufficientStorage, NumberOfMatchesWithinLimitsName)
	}
	return nil
}

// MaxResponsePropBytes bounds the property names one multistatus echoes back.
// The echo is repeated once per resource, so a request within MaxPropsPerRequest
// and MaxPropNameSize still buys a response proportional to the collection size;
// nothing else bounds that product.
const MaxResponsePropBytes = 16 << 20

// propEchoOverhead covers the angle brackets and the xmlns attribute wrapping one
// echoed name.
const propEchoOverhead = 16

// PropEchoSize estimates what one resource's property echo costs. Go's XML
// encoder writes each name as <local xmlns="space"></local>, emitting the local
// part twice and the namespace once, and re-declares the namespace per element
// rather than once per document.
func PropEchoSize(props ...*Prop) int {
	n := 0
	for _, prop := range props {
		if prop == nil {
			continue
		}
		for i := range prop.Raw {
			name, ok := prop.Raw[i].XMLName()
			if !ok {
				continue
			}
			n += 2*len(name.Local) + len(name.Space) + propEchoOverhead
		}
	}
	return n
}

// AllPropEchoSize prices one resource's echo for DAV:allprop and DAV:propname,
// whose property list comes from the Backend rather than the request. A client
// cannot grow it, but it is still repeated once per resource, so pricing it at
// zero would exempt the cheapest request from the budget entirely.
const AllPropEchoSize = 512

// SelectorEchoSize prices one resource's echo for whichever of the three RFC
// 4918 property selectors a request carries.
func SelectorEchoSize(prop *Prop, allProp, propName *struct{}) int {
	if allProp != nil || propName != nil {
		return AllPropEchoSize
	}
	return PropEchoSize(prop)
}

// BoundResponseWork rejects a multistatus that would repeat echoSize bytes over
// more resources than the budget allows. A zero limit means MaxResponsePropBytes;
// a negative one means the caller has taken the bound off.
func BoundResponseWork(echoSize, resources, limit int) error {
	if limit == 0 {
		limit = MaxResponsePropBytes
	}
	if limit < 0 || echoSize <= 0 || resources <= 0 {
		return nil
	}
	// Divide rather than multiply: the product is what overflows.
	if echoSize > limit/resources {
		return NewPreconditionError(http.StatusInsufficientStorage, NumberOfMatchesWithinLimitsName)
	}
	return nil
}

// BoundPropNames rejects a request naming more properties, or a longer property
// name, than the response echo can safely repeat.
func BoundPropNames(props ...*Prop) error {
	n := 0
	for _, prop := range props {
		if prop == nil {
			continue
		}
		for i := range prop.Raw {
			name, ok := prop.Raw[i].XMLName()
			if !ok {
				continue
			}
			n++
			if n > MaxPropsPerRequest {
				return HTTPErrorf(http.StatusBadRequest, "webdav: request names more than %d properties", MaxPropsPerRequest)
			}
			if len(name.Space)+len(name.Local) > MaxPropNameSize {
				return HTTPErrorf(http.StatusBadRequest, "webdav: property name exceeds %d bytes", MaxPropNameSize)
			}
		}
	}
	return nil
}

// BoundReportProp validates the property selector of a REPORT body and bounds
// the names it will echo. RFC 4791 §9.5/§9.10 and RFC 6352 §8.6/§8.7 each define
// their REPORT as carrying exactly one of DAV:allprop, DAV:propname or DAV:prop;
// the selector is otherwise only consulted while building a per-object response,
// so a malformed body matching no objects would answer an empty 207.
//
// The echo runs once per returned object, so a REPORT multiplies a requested
// property list by the collection size. BoundPropNames bounds one factor of that
// product, and has to be applied before the backend is reached.
func BoundReportProp(prop *Prop, allProp, propName *struct{}) error {
	n := 0
	for _, selected := range []bool{prop != nil, allProp != nil, propName != nil} {
		if selected {
			n++
		}
	}
	if n != 1 {
		return HTTPErrorf(http.StatusBadRequest, "webdav: REPORT requires exactly one of DAV:prop, DAV:allprop or DAV:propname")
	}
	return BoundPropNames(prop)
}

func IsRequestBodyEmpty(r *http.Request) bool {
	_, err := r.Body.Read(nil)
	return err == io.EOF
}

func ServeXML(w http.ResponseWriter) *xml.Encoder {
	w.Header().Add("Content-Type", "application/xml; charset=\"utf-8\"")
	w.Write([]byte(xml.Header)) //nolint:errcheck // best-effort response write
	return xml.NewEncoder(w)
}

// ServeMultiStatus writes a multistatus already held in memory. Use it only for
// a document of known, fixed size — a PROPPATCH result or a single response.
// Anything proportional to a collection belongs on MultiStatusWriter.
func ServeMultiStatus(w http.ResponseWriter, ms *MultiStatus) error {
	w.WriteHeader(http.StatusMultiStatus)
	return ServeXML(w).Encode(ms)
}

type Backend interface {
	Options(r *http.Request) (caps []string, allow []string, err error)
	HeadGet(w http.ResponseWriter, r *http.Request) error
	PropFind(r *http.Request, pf *PropFind, depth Depth, emit func(*Response) error) error
	PropPatch(r *http.Request, pu *PropertyUpdate) (*Response, error)
	Put(w http.ResponseWriter, r *http.Request) error
	Delete(r *http.Request) error
	Mkcol(r *http.Request) error
	Copy(r *http.Request, dest *Href, recursive, overwrite bool) (created bool, err error)
	Move(r *http.Request, dest *Href, overwrite bool) (created bool, err error)
}

type Handler struct {
	Backend Backend

	// DisablePathValidation skips CheckRequestPath and CheckDestination, leaving
	// the Backend to vet client-supplied paths itself.
	DisablePathValidation bool

	// MaxResponsePropBytes follows the BoundResponseWork limit convention.
	MaxResponsePropBytes int

	// MaxResponseBytes follows the NewMultiStatusWriter limit convention.
	MaxResponseBytes int
}

// checkPath applies the request-path rules unless the consumer has taken
// responsibility for them.
func (h *Handler) checkPath(r *http.Request) error {
	if h.DisablePathValidation {
		return nil
	}
	return CheckRequestPath(r)
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.Backend == nil {
		ServeError(w, fmt.Errorf("webdav: no backend available"))
		return
	}
	if err := h.checkPath(r); err != nil {
		ServeError(w, err)
		return
	}

	var err error
	switch r.Method {
	case http.MethodOptions:
		err = h.handleOptions(w, r)
	case http.MethodGet, http.MethodHead:
		err = h.Backend.HeadGet(w, r)
	case http.MethodPut:
		err = h.Backend.Put(w, r)
	case http.MethodDelete:
		// TODO: send a multistatus in case of partial failure
		err = h.Backend.Delete(r)
		if err == nil {
			w.WriteHeader(http.StatusNoContent)
		}
	case "PROPFIND":
		err = h.handlePropfind(w, r)
	case "PROPPATCH":
		err = h.handleProppatch(w, r)
	case "MKCOL":
		err = h.Backend.Mkcol(r)
		if err == nil {
			w.WriteHeader(http.StatusCreated)
		}
	case "COPY", "MOVE":
		err = h.handleCopyMove(w, r)
	default:
		err = HTTPErrorf(http.StatusMethodNotAllowed, "webdav: unsupported method")
	}

	if err != nil {
		ServeError(w, err)
	}
}

func (h *Handler) handleOptions(w http.ResponseWriter, r *http.Request) error {
	caps, allow, err := h.Backend.Options(r)
	if err != nil {
		return err
	}
	caps = append([]string{"1", "3"}, caps...)

	w.Header().Add("DAV", strings.Join(caps, ", "))
	w.Header().Add("Allow", strings.Join(allow, ", "))
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (h *Handler) handlePropfind(w http.ResponseWriter, r *http.Request) error {
	var propfind PropFind
	switch {
	case IsRequestBodyEmpty(r):
		// NOTE: properly handle PROPFIND requests without a body,
		// regardless of the "Content-Type" header of the request.
		propfind.AllProp = &struct{}{}
	case isContentXML(r.Header):
		if err := DecodeXMLRequest(r, &propfind); err != nil {
			return err
		}
	default:
		return HTTPErrorf(http.StatusBadRequest, "webdav: unsupported request body")
	}

	if err := BoundPropNames(propfind.Prop); err != nil {
		return err
	}

	depth, err := checkPropFindDepth(r)
	if err != nil {
		return err
	}

	ms := NewMultiStatusWriter(w, r.URL.Path, h.MaxResponseBytes)
	ms.LimitPropEcho(SelectorEchoSize(propfind.Prop, propfind.AllProp, propfind.PropName), h.MaxResponsePropBytes)
	defer ms.Abort() //nolint:errcheck // best-effort close if the document was already started

	if err := h.Backend.PropFind(r, &propfind, depth, ms.Write); err != nil {
		if !ms.Started() {
			return err
		}
		// The 207 is already on the wire, so the failure has to be a response.
		return ms.Fail(err)
	}
	return ms.Close()
}

type PropFindFunc func(raw *RawXMLValue) (interface{}, error)

func PropFindValue(value interface{}) PropFindFunc {
	return func(raw *RawXMLValue) (interface{}, error) {
		return value, nil
	}
}

// RemoveFromAllProp deletes properties a DAV:allprop must not return, leaving
// them reachable through an explicit DAV:prop. RFC 4791 §9.6, RFC 6352 §10.4,
// RFC 6578 §4 and RFC 4791 §5.2.5-5.2.9: answering allprop with calendar-data
// puts a collection's whole contents in a reply that asked for metadata.
func RemoveFromAllProp(propfind *PropFind, props map[xml.Name]PropFindFunc, names ...xml.Name) {
	if propfind.AllProp == nil {
		return
	}
	for _, name := range names {
		delete(props, name)
	}
}

func NewPropFindResponse(p string, propfind *PropFind, props map[xml.Name]PropFindFunc) (*Response, error) {
	resp := &Response{Hrefs: []Href{{Path: p}}}

	if _, ok := props[ResourceTypeName]; !ok {
		props[ResourceTypeName] = PropFindValue(NewResourceType())
	}

	if propfind.PropName != nil {
		for xmlName := range props {
			emptyVal := NewRawXMLElement(xmlName, nil, nil)
			if err := resp.EncodeProp(http.StatusOK, emptyVal); err != nil {
				return nil, err
			}
		}
	} else if propfind.AllProp != nil {
		// TODO: add support for propfind.Include
		for xmlName, f := range props {
			emptyVal := NewRawXMLElement(xmlName, nil, nil)

			val, err := f(emptyVal)

			code := http.StatusOK
			if err != nil {
				// TODO: don't throw away error message here
				code = HTTPErrorFromError(err).Code
				val = emptyVal
			}

			if err := resp.EncodeProp(code, val); err != nil {
				return nil, err
			}
		}
	} else if prop := propfind.Prop; prop != nil {
		for i := range prop.Raw {
			raw := &prop.Raw[i]
			xmlName, ok := raw.XMLName()
			if !ok {
				continue
			}

			emptyVal := NewRawXMLElement(xmlName, nil, nil)

			var code int
			var val interface{} = emptyVal
			f, ok := props[xmlName]
			if ok {
				if v, err := f(raw); err != nil {
					// TODO: don't throw away error message here
					code = HTTPErrorFromError(err).Code
				} else {
					code = http.StatusOK
					val = v
				}
			} else {
				code = http.StatusNotFound
			}

			if err := resp.EncodeProp(code, val); err != nil {
				return nil, err
			}
		}
	} else {
		return nil, HTTPErrorf(http.StatusBadRequest, "webdav: request missing propname, allprop or prop element")
	}

	return resp, nil
}

func (h *Handler) handleProppatch(w http.ResponseWriter, r *http.Request) error {
	var update PropertyUpdate
	if err := DecodeXMLRequest(r, &update); err != nil {
		return err
	}

	props := make([]*Prop, 0, len(update.Remove)+len(update.Set))
	for i := range update.Remove {
		props = append(props, &update.Remove[i].Prop)
	}
	for i := range update.Set {
		props = append(props, &update.Set[i].Prop)
	}
	if err := BoundPropNames(props...); err != nil {
		return err
	}
	resp, err := h.Backend.PropPatch(r, &update)
	if err != nil {
		return err
	}

	ms := NewMultiStatus(*resp)
	return ServeMultiStatus(w, ms)
}

func parseDestination(h http.Header) (*Href, error) {
	destHref := h.Get("Destination")
	if destHref == "" {
		return nil, HTTPErrorf(http.StatusBadRequest, "webdav: missing Destination header in MOVE request")
	}
	dest, err := url.Parse(destHref)
	if err != nil {
		return nil, HTTPErrorf(http.StatusBadRequest, "webdav: malformed Destination header in MOVE request: %v", err)
	}
	return (*Href)(dest), nil
}

func (h *Handler) handleCopyMove(w http.ResponseWriter, r *http.Request) error {
	dest, err := parseDestination(r.Header)
	if err != nil {
		return err
	}
	if !h.DisablePathValidation {
		if derr := CheckDestination(r, dest); derr != nil {
			return derr
		}
	}

	overwrite := true
	if s := r.Header.Get("Overwrite"); s != "" {
		overwrite, err = ParseOverwrite(s)
		if err != nil {
			return err
		}
	}

	depth := DepthInfinity
	if s := r.Header.Get("Depth"); s != "" {
		depth, err = ParseDepth(s)
		if err != nil {
			return err
		}
	}

	var created bool
	if r.Method == "COPY" {
		var recursive bool
		switch depth {
		case DepthZero:
			recursive = false
		case DepthOne:
			return HTTPErrorf(http.StatusBadRequest, `webdav: "Depth: 1" is not supported in COPY request`)
		case DepthInfinity:
			recursive = true
		}

		created, err = h.Backend.Copy(r, dest, recursive, overwrite)
	} else {
		if depth != DepthInfinity {
			return HTTPErrorf(http.StatusBadRequest, `webdav: only "Depth: infinity" is accepted in MOVE request`)
		}
		created, err = h.Backend.Move(r, dest, overwrite)
	}
	if err != nil {
		return err
	}

	if created {
		w.WriteHeader(http.StatusCreated)
	} else {
		w.WriteHeader(http.StatusNoContent)
	}
	return nil
}

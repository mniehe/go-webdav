package internal

import (
	"encoding/xml"
	"net/http"
	"strings"
)

// mutatingMethods are the state-changing methods whose effect an RFC 4918 If
// header is meant to make conditional.
var mutatingMethods = map[string]bool{
	http.MethodPut:    true,
	http.MethodDelete: true,
	"PROPPATCH":       true,
	"MKCOL":           true,
	"MKCALENDAR":      true,
	"COPY":            true,
	"MOVE":            true,
}

// RejectUnsupportedIf fails a state-changing request that carries an If header
// (RFC 4918 §10.4). The If grammar can encode entity-tag and lock-token
// conditions this server does not evaluate; treating the header as absent
// would turn a client's conditional write into an unconditional one. Refusing
// with 412 is the safe reading — the condition cannot be shown to hold, so the
// method must not take effect. Conditional requests are served through
// If-Match and If-None-Match, which the write paths do honour.
func RejectUnsupportedIf(r *http.Request) error {
	if !mutatingMethods[r.Method] {
		return nil
	}
	if strings.TrimSpace(r.Header.Get("If")) == "" {
		return nil
	}
	return HTTPErrorf(http.StatusPreconditionFailed, "webdav: the If header is not supported; use If-Match or If-None-Match")
}

// Preconditions this package refuses requests for (RFC 4918 §16).
var (
	PropFindFiniteDepthName         = xml.Name{Space: Namespace, Local: "propfind-finite-depth"}
	ResourceMustBeNullName          = xml.Name{Space: Namespace, Local: "resource-must-be-null"}
	NumberOfMatchesWithinLimitsName = xml.Name{Space: Namespace, Local: "number-of-matches-within-limits"}
)

// NewPreconditionError reports a named precondition as the RFC 4918 §16 error
// body for the given status.
func NewPreconditionError(code int, name xml.Name) error {
	elem := NewRawXMLElement(name, nil, nil)
	return &HTTPError{
		Code: code,
		Err:  &Error{Raw: []RawXMLValue{*elem}},
	}
}

// checkPropFindDepth resolves the Depth of a PROPFIND. An absent Depth defaults
// to 1 rather than infinity: real clients always send one, and an infinite-depth
// walk of a large collection is a memory-exhaustion vector. Explicit infinity is
// refused with RFC 4918 §9.1's DAV:propfind-finite-depth.
func checkPropFindDepth(r *http.Request) (Depth, error) {
	s := r.Header.Get("Depth")
	if s == "" {
		return DepthOne, nil
	}
	depth, err := ParseDepth(s)
	if err != nil {
		return 0, SafeHTTPError(http.StatusBadRequest, err)
	}
	if depth == DepthInfinity {
		return 0, NewPreconditionError(http.StatusForbidden, PropFindFiniteDepthName)
	}
	return depth, nil
}

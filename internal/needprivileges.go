package internal

import (
	"encoding/xml"
	"net/http"
)

// NeedPrivileges is the RFC 3744 §7.1.1 precondition naming what the request
// would have required. A client that is told only "403" cannot tell a missing
// privilege from a malformed request; this says which privilege on which
// resource was wanted, which is what a sharing UI needs to explain the refusal.
type NeedPrivileges struct {
	XMLName  xml.Name              `xml:"DAV: need-privileges"`
	Resource []NeedPrivilegesEntry `xml:"DAV: resource"`
}

type NeedPrivilegesEntry struct {
	XMLName   xml.Name  `xml:"DAV: resource"`
	Href      Href      `xml:"DAV: href"`
	Privilege Privilege `xml:"DAV: privilege"`
}

// NewNeedPrivilegesError reports that the principal lacks privilege on href.
func NewNeedPrivilegesError(href, privilege string) error {
	need := &NeedPrivileges{Resource: []NeedPrivilegesEntry{{
		Href:      Href{Path: href},
		Privilege: NewPrivilege(privilege),
	}}}
	raw, err := EncodeRawXMLElement(need)
	if err != nil {
		return HTTPErrorf(http.StatusForbidden, "webdav: insufficient privileges")
	}
	return &HTTPError{Code: http.StatusForbidden, Err: &Error{Raw: []RawXMLValue{*raw}}}
}

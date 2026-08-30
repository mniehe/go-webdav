package internal

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
)

// MaxXMLNodes bounds how many elements and attributes one request body may
// contain. Without it a 1 MiB body of minimal siblings decodes into tens of
// megabytes before any property bound rejects it. Attributes share the budget
// because encoding/xml hangs the whole []Attr off the one StartElement that
// carries it, so an element-only count leaves that slice unbounded.
//
// REPORT sizes its own budget from the handler's href limit — see
// RequestNodeBudget — so this is the floor rather than a hard ceiling.
const MaxXMLNodes = 20_000

// MaxXMLDepth bounds how deeply a request body may nest. RawXMLValue enforces
// its own equivalent, but a typed recursive element such as caldav's comp-filter
// goes through encoding/xml, whose backstop does not cover Token or Skip.
const MaxXMLDepth = 1000

// scanXMLBody rejects a body the server should not go on to decode: one nesting
// or branching past what a real client sends, or carrying a DOCTYPE.
func scanXMLBody(body []byte, maxNodes int) error {
	dec := xml.NewDecoder(bytes.NewReader(body))
	nodes, depth := 0, 0
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return SafeHTTPError(http.StatusBadRequest, err)
		}

		switch tok := tok.(type) {
		case xml.Directive:
			// RFC 4918 §20.6. Go resolves no external entity, so this is not
			// XXE — but an unaccountable body is refused rather than served.
			return HTTPErrorf(http.StatusForbidden, "webdav: DOCTYPE is not allowed in a request body")
		case xml.StartElement:
			nodes += 1 + len(tok.Attr)
			if maxNodes >= 0 && nodes > maxNodes {
				return HTTPErrorf(http.StatusBadRequest, "webdav: request body has more than %d elements and attributes", maxNodes)
			}
			depth++
			if depth > MaxXMLDepth {
				return HTTPErrorf(http.StatusBadRequest, "webdav: request body nests deeper than %d", MaxXMLDepth)
			}
		case xml.EndElement:
			depth--
		}
	}
}

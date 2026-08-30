package internal

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func decodeBody(t *testing.T, body string, v interface{}) error {
	t.Helper()
	r := httptest.NewRequest("PROPFIND", "/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/xml")
	return DecodeXMLRequest(r, v)
}

// RFC 4918 §20.6 asks a server to refuse a body carrying a DOCTYPE rather than
// serve it. Go resolves nothing, so this is not XXE — but a request the server
// cannot fully account for should not be answered.
func TestDecodeRejectsDoctype(t *testing.T) {
	var pf PropFind
	err := decodeBody(t, `<?xml version="1.0"?>`+
		`<!DOCTYPE d SYSTEM "http://127.0.0.1:1/evil.dtd">`+
		`<d:propfind xmlns:d="DAV:"><d:allprop/></d:propfind>`, &pf)
	if err == nil {
		t.Fatal("a body carrying a DOCTYPE was accepted")
	}
	if code := HTTPErrorFromError(err).Code; code != http.StatusForbidden {
		t.Errorf("code = %d, want 403", code)
	}
}

// The property bound runs after the decoder has already built the tree, so a
// body of many siblings is allocated in full and only then rejected. The count
// has to be enforced while scanning, before anything is materialised.
func TestDecodeRejectsTooManyNodes(t *testing.T) {
	var pf PropFind
	body := `<?xml version="1.0"?><d:propfind xmlns:d="DAV:"><d:prop>` +
		strings.Repeat(`<d:x/>`, MaxXMLNodes+1) +
		`</d:prop></d:propfind>`

	err := decodeBody(t, body, &pf)
	if err == nil {
		t.Fatal("a body with more nodes than the limit was accepted")
	}
	if code := HTTPErrorFromError(err).Code; code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", code)
	}
}

// maxXMLDepth only covers RawXMLValue. A typed recursive element — caldav's
// comp-filter — decodes through the stdlib path and recurses at whatever depth
// the client chose.
func TestDecodeRejectsDeepNesting(t *testing.T) {
	var pf PropFind
	depth := MaxXMLDepth + 10
	body := `<?xml version="1.0"?><d:propfind xmlns:d="DAV:">` +
		strings.Repeat(`<d:x>`, depth) + strings.Repeat(`</d:x>`, depth) +
		`</d:propfind>`

	err := decodeBody(t, body, &pf)
	if err == nil {
		t.Fatal("a body nested past the depth limit was accepted")
	}
	if code := HTTPErrorFromError(err).Code; code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", code)
	}
}

// An ordinary request must still decode.
func TestDecodeAcceptsOrdinaryBody(t *testing.T) {
	var pf PropFind
	if err := decodeBody(t, `<?xml version="1.0"?><d:propfind xmlns:d="DAV:">`+
		`<d:prop><d:getetag/><d:resourcetype/></d:prop></d:propfind>`, &pf); err != nil {
		t.Fatalf("an ordinary PROPFIND was rejected: %v", err)
	}
	if pf.Prop == nil || len(pf.Prop.Raw) != 2 {
		t.Errorf("decoded prop = %+v, want 2 raw properties", pf.Prop)
	}
}

// encoding/xml materialises every attribute of an element into the one
// StartElement token that carries it, so counting elements alone lets a single
// node drag an unbounded []Attr into the decoded tree.
func TestDecodeRejectsTooManyAttributes(t *testing.T) {
	var pf PropFind
	body := `<?xml version="1.0"?><d:propfind xmlns:d="DAV:"` +
		strings.Repeat(` a=""`, MaxXMLNodes+1) +
		`><d:allprop/></d:propfind>`

	err := decodeBody(t, body, &pf)
	if err == nil {
		t.Fatal("a body with more attributes than the node limit was accepted")
	}
	if code := HTTPErrorFromError(err).Code; code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", code)
	}
}

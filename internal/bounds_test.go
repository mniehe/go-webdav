package internal

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func propWithNames(n int, space, local string) *Prop {
	prop := &Prop{}
	for i := 0; i < n; i++ {
		name := xml.Name{Space: space, Local: local}
		prop.Raw = append(prop.Raw, *NewRawXMLElement(name, nil, nil))
	}
	return prop
}

func TestBoundPropNamesRejectsTooManyProps(t *testing.T) {
	// The multistatus echoes one element per requested property, so an unbounded
	// count turns a body under MaxXMLBodySize into an unbounded response.
	if err := BoundPropNames(propWithNames(MaxPropsPerRequest, "DAV:", "x")); err != nil {
		t.Fatalf("%d properties should be accepted: %v", MaxPropsPerRequest, err)
	}

	err := BoundPropNames(propWithNames(MaxPropsPerRequest+1, "DAV:", "x"))
	if err == nil {
		t.Fatal("expected more than the maximum property count to be rejected")
	}
	if code := HTTPErrorFromError(err).Code; code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", code)
	}
}

func TestBoundPropNamesCountsAcrossProps(t *testing.T) {
	// The limit is on the request, not on any single <prop> element: a PROPPATCH
	// can spread its properties over many <set>/<remove> instructions.
	half := propWithNames(MaxPropsPerRequest/2+1, "DAV:", "x")
	if err := BoundPropNames(half, half); err == nil {
		t.Error("expected the property count to be summed across every <prop>")
	}
}

func TestBoundPropNamesRejectsLongName(t *testing.T) {
	// Go's encoder repeats the namespace on every echoed element, so one long
	// namespace declared once in the request is written back once per property.
	long := propWithNames(1, "urn:"+strings.Repeat("a", MaxPropNameSize), "x")
	err := BoundPropNames(long)
	if err == nil {
		t.Fatal("expected an oversized property name to be rejected")
	}
	if code := HTTPErrorFromError(err).Code; code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", code)
	}
}

func TestBoundPropNamesIgnoresNilAndNonElements(t *testing.T) {
	if err := BoundPropNames(nil, &Prop{}); err != nil {
		t.Errorf("a nil or empty prop must be accepted: %v", err)
	}
}

// stubPropfindBackend answers PropFind so the amplification guard can be
// exercised through the real dispatch path.
type stubPropfindBackend struct {
	Backend
	called bool
}

func (b *stubPropfindBackend) PropFind(r *http.Request, pf *PropFind, depth Depth, emit func(*Response) error) error {
	b.called = true
	return nil
}

func TestPropFindRejectsAmplifyingRequest(t *testing.T) {
	// Reachable with no optional backend capability, so the guard has to sit in
	// the shared dispatch rather than in caldav/carddav.
	body := `<?xml version="1.0"?><d:propfind xmlns:d="DAV:" xmlns:a="urn:` +
		strings.Repeat("a", 400) + `"><d:prop>` +
		strings.Repeat(`<a:p/>`, MaxPropsPerRequest+1) +
		`</d:prop></d:propfind>`

	backend := &stubPropfindBackend{}
	h := Handler{Backend: backend}
	req := httptest.NewRequest("PROPFIND", "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("Depth", "0")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if backend.called {
		t.Error("the backend was reached despite an over-large property list")
	}
	if w.Body.Len() > 4096 {
		t.Errorf("the rejection itself should be small, got %d bytes", w.Body.Len())
	}
}

func TestBoundReportPropRequiresExactlyOneSelector(t *testing.T) {
	set := &struct{}{}
	for _, tc := range []struct {
		name              string
		prop              *Prop
		allProp, propName *struct{}
		wantErr           bool
	}{
		{name: "prop only", prop: &Prop{}},
		{name: "allprop only", allProp: set},
		{name: "propname only", propName: set},
		{name: "none", wantErr: true},
		{name: "prop and allprop", prop: &Prop{}, allProp: set, wantErr: true},
		{name: "prop and propname", prop: &Prop{}, propName: set, wantErr: true},
		{name: "allprop and propname", allProp: set, propName: set, wantErr: true},
		{name: "all three", prop: &Prop{}, allProp: set, propName: set, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := BoundReportProp(tc.prop, tc.allProp, tc.propName)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected the selector to be rejected")
				}
				if code := HTTPErrorFromError(err).Code; code != http.StatusBadRequest {
					t.Errorf("expected 400, got %d", code)
				}
				return
			}
			if err != nil {
				t.Errorf("expected the selector to be accepted: %v", err)
			}
		})
	}
}

func TestBoundReportPropStillBoundsNames(t *testing.T) {
	// The selector check must not short-circuit the echo bound.
	err := BoundReportProp(propWithNames(MaxPropsPerRequest+1, "DAV:", "x"), nil, nil)
	if err == nil {
		t.Fatal("expected an over-large property list to be rejected")
	}
}

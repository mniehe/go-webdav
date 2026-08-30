package carddav_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mniehe/davkit/carddav"
)

func request(h *carddav.Handler, method, target, body string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	for name, value := range headers {
		r.Header.Set(name, value)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// A false DAV If condition must prevent the mutation, not be ignored
// (RFC 4918 §10.4).
func TestMutationsRejectAnUnsupportedIfHeader(t *testing.T) {
	store := newStore(t)
	seedItem(t, store, "alice", "standup.vcf")
	h := handlerFor(t, store, carddav.Config{})

	cases := []struct {
		method  string
		target  string
		body    string
		headers map[string]string
	}{
		{http.MethodPut, "/alice/work/standup.vcf", reviewVCF, map[string]string{"Content-Type": "text/vcard"}},
		{http.MethodDelete, "/alice/work/standup.vcf", "", nil},
		{"COPY", "/alice/work/standup.vcf", "", map[string]string{"Destination": "/alice/work/copy.vcf"}},
		{"MOVE", "/alice/work/standup.vcf", "", map[string]string{"Destination": "/alice/work/moved.vcf"}},
		{"PROPPATCH", "/alice/work/", `<?xml version="1.0"?><propertyupdate xmlns="DAV:"><set><prop><displayname>x</displayname></prop></set></propertyupdate>`, map[string]string{"Content-Type": "application/xml"}},
		{"MKCOL", "/alice/new/", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			headers := map[string]string{"If": `(["definitely-stale"])`}
			for k, v := range tc.headers {
				headers[k] = v
			}
			w := request(h, tc.method, tc.target, tc.body, headers)
			if w.Code != http.StatusPreconditionFailed {
				t.Errorf("status = %d, want %d (the unsupported If must fail the request, never be ignored)", w.Code, http.StatusPreconditionFailed)
			}
		})
	}

	if w := do(h, http.MethodGet, "/alice/work/standup.vcf"); w.Code != http.StatusOK {
		t.Fatalf("GET after refused PUT: status = %d", w.Code)
	}
}

func TestReadsTolerateAnIfHeader(t *testing.T) {
	store := newStore(t)
	seedItem(t, store, "alice", "standup.vcf")
	h := handlerFor(t, store, carddav.Config{})

	if w := request(h, http.MethodGet, "/alice/work/standup.vcf", "", map[string]string{"If": `(["x"])`}); w.Code != http.StatusOK {
		t.Errorf("GET with an If header: status = %d, want %d", w.Code, http.StatusOK)
	}
}

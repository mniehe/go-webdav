package caldav_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mniehe/davkit/caldav"
)

func request(h *caldav.Handler, method, target, body string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	for name, value := range headers {
		r.Header.Set(name, value)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// A false DAV If condition must prevent the mutation, not be ignored. The If
// grammar can carry entity-tag and lock-token conditions this server does not
// evaluate; applying the method as if the header were absent would turn a
// conditional write into an unconditional one (RFC 4918 §10.4).
func TestMutationsRejectAnUnsupportedIfHeader(t *testing.T) {
	store := newStore(t)
	seedItem(t, store, "alice", "standup.ics")
	h := handlerFor(t, store, caldav.Config{})

	cases := []struct {
		method  string
		target  string
		body    string
		headers map[string]string
	}{
		{http.MethodPut, "/alice/work/standup.ics", eventICS, map[string]string{"Content-Type": "text/calendar"}},
		{http.MethodDelete, "/alice/work/standup.ics", "", nil},
		{"COPY", "/alice/work/standup.ics", "", map[string]string{"Destination": "/alice/work/copy.ics"}},
		{"MOVE", "/alice/work/standup.ics", "", map[string]string{"Destination": "/alice/work/moved.ics"}},
		{"PROPPATCH", "/alice/work/", `<?xml version="1.0"?><propertyupdate xmlns="DAV:"><set><prop><displayname>x</displayname></prop></set></propertyupdate>`, map[string]string{"Content-Type": "application/xml"}},
		{"MKCALENDAR", "/alice/new/", "", nil},
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

	// The refused PUT must not have applied: the item still reads as seeded.
	if w := do(h, http.MethodGet, "/alice/work/standup.ics"); w.Code != http.StatusOK {
		t.Fatalf("GET after refused PUT: status = %d", w.Code)
	}
}

// A read with an If header is served normally — ignoring the condition on a
// GET is a conformance nicety, not a safety hazard, and mainstream clients use
// If-Match for conditional reads.
func TestReadsTolerateAnIfHeader(t *testing.T) {
	store := newStore(t)
	seedItem(t, store, "alice", "standup.ics")
	h := handlerFor(t, store, caldav.Config{})

	if w := request(h, http.MethodGet, "/alice/work/standup.ics", "", map[string]string{"If": `(["x"])`}); w.Code != http.StatusOK {
		t.Errorf("GET with an If header: status = %d, want %d", w.Code, http.StatusOK)
	}
}

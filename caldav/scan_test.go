package caldav_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mniehe/davkit/caldav"
)

func propfindRaw(h *caldav.Handler, target, depth, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("PROPFIND", target, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/xml")
	if depth != "" {
		r.Header.Set("Depth", depth)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// A collection larger than the scan budget must fail with 507 rather than
// materialise and parse the whole thing. MaxSearchResults bounds returned
// matches, not the work done to find them: a query that matches nothing would
// otherwise scan every item with no ceiling at all.
func TestListingBeyondTheScanBudgetIsRefused(t *testing.T) {
	store := newStore(t)
	for _, name := range []string{"a.ics", "b.ics", "c.ics"} {
		seedRaw(t, store, "alice", name, strings.Replace(augustICS, "UID:august", "UID:"+name, 1), name)
	}
	h := handlerFor(t, store, caldav.Config{MaxCollectionScan: 2})

	t.Run("calendar-query", func(t *testing.T) {
		// A filter that matches nothing still has to scan the whole collection,
		// so the budget is what stops it.
		body := `<?xml version="1.0"?>
<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop><D:getetag/></D:prop>
  <C:filter><C:comp-filter name="VCALENDAR"><C:comp-filter name="VEVENT">
    <C:prop-filter name="SUMMARY"><C:text-match>matches-nothing</C:text-match></C:prop-filter>
  </C:comp-filter></C:comp-filter></C:filter>
</C:calendar-query>`
		w := report(t, h, "/alice/work/", body)
		if w.Code != http.StatusInsufficientStorage {
			t.Fatalf("status = %d, want %d\n%s", w.Code, http.StatusInsufficientStorage, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "number-of-matches-within-limits") {
			t.Errorf("body = %q, want the DAV:number-of-matches-within-limits precondition", w.Body.String())
		}
	})

	t.Run("PROPFIND Depth 1", func(t *testing.T) {
		w := propfindRaw(h, "/alice/work/", "1", `<?xml version="1.0"?><propfind xmlns="DAV:"><prop><getetag/></prop></propfind>`)
		if w.Code != http.StatusInsufficientStorage {
			t.Errorf("status = %d, want %d", w.Code, http.StatusInsufficientStorage)
		}
	})
}

// A collection within the budget lists normally.
func TestListingWithinTheScanBudgetSucceeds(t *testing.T) {
	store := newStore(t)
	seedRaw(t, store, "alice", "a.ics", augustICS, "august")
	h := handlerFor(t, store, caldav.Config{MaxCollectionScan: 100})

	w := propfindRaw(h, "/alice/work/", "1", `<?xml version="1.0"?><propfind xmlns="DAV:"><prop><getetag/></prop></propfind>`)
	if w.Code != http.StatusMultiStatus {
		t.Errorf("status = %d, want %d\n%s", w.Code, http.StatusMultiStatus, w.Body.String())
	}
}

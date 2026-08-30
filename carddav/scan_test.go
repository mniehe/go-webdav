package carddav_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mniehe/davkit/carddav"
)

func propfindRaw(h *carddav.Handler, target, depth, body string) *httptest.ResponseRecorder {
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
// materialise and parse the whole thing.
func TestListingBeyondTheScanBudgetIsRefused(t *testing.T) {
	store := newStore(t)
	for _, name := range []string{"a.vcf", "b.vcf", "c.vcf"} {
		seedRaw(t, store, "alice", name, strings.Replace(adaVCF, "UID:ada", "UID:"+name, 1), name)
	}
	h := handlerFor(t, store, carddav.Config{MaxCollectionScan: 2})

	t.Run("addressbook-query", func(t *testing.T) {
		body := `<?xml version="1.0"?>
<C:addressbook-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav">
  <D:prop><D:getetag/></D:prop>
  <C:filter><C:prop-filter name="FN"><C:text-match>matches-nothing</C:text-match></C:prop-filter></C:filter>
</C:addressbook-query>`
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

func TestListingWithinTheScanBudgetSucceeds(t *testing.T) {
	store := newStore(t)
	seedRaw(t, store, "alice", "a.vcf", adaVCF, "ada")
	h := handlerFor(t, store, carddav.Config{MaxCollectionScan: 100})

	w := propfindRaw(h, "/alice/work/", "1", `<?xml version="1.0"?><propfind xmlns="DAV:"><prop><getetag/></prop></propfind>`)
	if w.Code != http.StatusMultiStatus {
		t.Errorf("status = %d, want %d\n%s", w.Code, http.StatusMultiStatus, w.Body.String())
	}
}

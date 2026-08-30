package webdav

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ServePrincipal is what a combined CalDAV+CardDAV server mounts at its
// principal URL, so the principal properties have to be reported here too — the
// caldav and carddav handlers never see that path.
func TestServePrincipalReportsPrincipalProps(t *testing.T) {
	const principalPath = "/dav/principals/alice/"
	options := &ServePrincipalOptions{CurrentUserPrincipalPath: principalPath}

	body := `<?xml version="1.0" encoding="UTF-8"?>
<d:propfind xmlns:d="DAV:">
  <d:prop><d:principal-URL/><d:principal-collection-set/><d:current-user-principal/></d:prop>
</d:propfind>`

	req := httptest.NewRequest("PROPFIND", principalPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/xml")
	w := httptest.NewRecorder()
	ServePrincipal(w, req, options)

	res := w.Result()
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	resp := string(data)

	if res.StatusCode != 207 {
		t.Fatalf("expected 207, got %d:\n%s", res.StatusCode, resp)
	}
	for _, want := range []string{
		"<principal-URL xmlns=\"DAV:\"><href>" + principalPath + "</href></principal-URL>",
		"<principal-collection-set xmlns=\"DAV:\"><href>/dav/principals/</href></principal-collection-set>",
	} {
		if !strings.Contains(resp, want) {
			t.Errorf("principal PROPFIND missing %q:\n%s", want, resp)
		}
	}
}

func TestServePrincipalRejectsAmplifyingRequest(t *testing.T) {
	// This handler decodes the body itself rather than going through the shared
	// dispatch, so it needs its own bound.
	body := `<?xml version="1.0"?><d:propfind xmlns:d="DAV:" xmlns:a="urn:` +
		strings.Repeat("a", 400) + `"><d:prop>` +
		strings.Repeat(`<a:p/>`, 1025) +
		`</d:prop></d:propfind>`

	req := httptest.NewRequest("PROPFIND", "/dav/principals/alice/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/xml")
	w := httptest.NewRecorder()
	ServePrincipal(w, req, &ServePrincipalOptions{CurrentUserPrincipalPath: "/dav/principals/alice/"})

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
	if w.Body.Len() > 4096 {
		t.Errorf("the rejection itself should be small, got %d bytes", w.Body.Len())
	}
}

package webdav

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mniehe/davkit/internal"
)

func budgetDir(t *testing.T, n int) string {
	t.Helper()
	dir := t.TempDir()
	for i := 0; i < n; i++ {
		name := filepath.Join(dir, fmt.Sprintf("f%d.txt", i))
		if err := os.WriteFile(name, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func budgetPropFindBody() string {
	return `<?xml version="1.0"?><d:propfind xmlns:d="DAV:" xmlns:a="urn:` +
		strings.Repeat("a", internal.MaxPropNameSize-8) + `">` +
		`<d:prop>` + strings.Repeat(`<a:p/>`, internal.MaxPropsPerRequest) + `</d:prop>` +
		`</d:propfind>`
}

func serveDeepPropFind(t *testing.T, h Handler, body string) (status int, respBody string) {
	t.Helper()
	req := httptest.NewRequest("PROPFIND", "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("Depth", "1")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w.Code, w.Body.String()
}

// The property echo is repeated once per member, so a request within every
// per-request bound still buys a response proportional to the collection.
//
// PROPFIND streams, so it cannot know the member count before it starts
// answering: unlike the REPORT paths it stops at the budget mid-document rather
// than refusing with a bare 507. The document stays well formed and says why it
// ended.
func TestPropFindStopsAtItsBudget(t *testing.T) {
	h := Handler{FileSystem: LocalFileSystem(budgetDir(t, 500))}

	body := budgetPropFindBody()
	code, resp := serveDeepPropFind(t, h, body)
	if code != http.StatusMultiStatus {
		t.Fatalf("code = %d, want 207", code)
	}
	if !strings.Contains(resp, "number-of-matches-within-limits") {
		t.Errorf("the truncated document does not say why it ended:\n%s", resp[:min(len(resp), 512)])
	}
	if !strings.HasSuffix(strings.TrimSpace(resp), "</multistatus>") {
		t.Error("the truncated document was not closed")
	}
	dec := xml.NewDecoder(strings.NewReader(resp))
	for {
		_, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("the truncated document is not well-formed XML: %v", err)
		}
	}
	if over := len(resp) - internal.MaxResponsePropBytes; over > internal.MaxResponsePropBytes/4 {
		t.Errorf("the response ran %d bytes past the budget", over)
	}
}

func TestPropFindBudgetIsConfigurable(t *testing.T) {
	dir := budgetDir(t, 500)

	t.Run("a negative budget removes the bound", func(t *testing.T) {
		h := Handler{FileSystem: LocalFileSystem(dir), MaxResponsePropBytes: -1}
		code, resp := serveDeepPropFind(t, h, budgetPropFindBody())
		if code != http.StatusMultiStatus {
			t.Errorf("code = %d, want 207 with the bound removed", code)
		}
		if len(resp) < 1<<20 {
			t.Errorf("expected the unbounded response to stay large, got %d bytes", len(resp))
		}
	})

	t.Run("an ordinary request is untouched", func(t *testing.T) {
		h := Handler{FileSystem: LocalFileSystem(dir)}
		body := `<?xml version="1.0"?><d:propfind xmlns:d="DAV:"><d:prop><d:getetag/></d:prop></d:propfind>`
		if code, resp := serveDeepPropFind(t, h, body); code != http.StatusMultiStatus {
			t.Errorf("code = %d, want 207:\n%s", code, resp[:min(len(resp), 256)])
		}
	})
}

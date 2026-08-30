package webdav

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recordingFS records the paths the handler passes down, so a test can assert
// what the backend was actually asked for rather than only what was returned.
type recordingFS struct {
	LocalFileSystem
	opened []string
	copied [][2]string
}

func (f *recordingFS) Open(ctx context.Context, name string) (io.ReadCloser, error) {
	f.opened = append(f.opened, name)
	return f.LocalFileSystem.Open(ctx, name)
}

func (f *recordingFS) Copy(ctx context.Context, src, dst string, opts *CopyOptions) (bool, error) {
	f.copied = append(f.copied, [2]string{src, dst})
	return f.LocalFileSystem.Copy(ctx, src, dst, opts)
}

// recordedFS is the handler's filesystem, so a test can assert on the paths it
// was actually handed.
func recordedFS(t *testing.T, h Handler) *recordingFS {
	t.Helper()
	fs, ok := h.FileSystem.(*recordingFS)
	if !ok {
		t.Fatalf("FileSystem is %T, not *recordingFS", h.FileSystem)
	}
	return fs
}

func serveDAV(t *testing.T, h Handler, method, target string, headers map[string]string) (int, *recordingFS) {
	t.Helper()
	req := httptest.NewRequest(method, target, http.NoBody)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w.Result().StatusCode, recordedFS(t, h)
}

func newRecordingHandler(t *testing.T) Handler {
	t.Helper()
	dir := t.TempDir()
	return Handler{FileSystem: &recordingFS{LocalFileSystem: LocalFileSystem(dir)}}
}

// caldav.Handler and carddav.Handler reject a non-canonical request path so the
// backend and every path-derived decision see the same string. The base handler
// never gained the guard, so a dot segment reaches the filesystem verbatim.
func TestBaseHandlerRejectsNonCanonicalPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secret"), []byte("classified"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := Handler{FileSystem: &recordingFS{LocalFileSystem: LocalFileSystem(dir)}}

	req := httptest.NewRequest(http.MethodGet, "/a/../secret", http.NoBody)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if code := w.Result().StatusCode; code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400 for a non-canonical path", code)
	}
	if strings.Contains(w.Body.String(), "classified") {
		t.Error("a dot segment resolved to a resource the request never named")
	}
	for _, p := range recordedFS(t, h).opened {
		if strings.Contains(p, "..") {
			t.Errorf("the backend received an uncanonicalised path %q", p)
		}
	}
}

func TestBaseHandlerRejectsEncodedSeparator(t *testing.T) {
	h := newRecordingHandler(t)
	code, _ := serveDAV(t, h, http.MethodGet, "/a/b%2Fc", nil)
	if code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400 for an encoded separator", code)
	}
}

// RFC 4918 §9.8.4: a COPY whose Destination names another server is answered
// 502. Dropping the authority instead turns a cross-origin destination into a
// local write.
func TestCopyRejectsForeignDestinationAuthority(t *testing.T) {
	h := newRecordingHandler(t)
	code, fs := serveDAV(t, h, "COPY", "/src", map[string]string{
		"Destination": "https://evil.example/dst",
		"Overwrite":   "T",
	})
	if code == http.StatusCreated || code == http.StatusNoContent {
		t.Errorf("a cross-origin COPY succeeded with %d", code)
	}
	if len(fs.copied) != 0 {
		t.Errorf("the backend was asked to copy to %v despite a foreign authority", fs.copied)
	}
}

func TestCopyRejectsEncodedSeparatorInDestination(t *testing.T) {
	h := newRecordingHandler(t)
	code, fs := serveDAV(t, h, "COPY", "/src", map[string]string{
		"Destination": "/dst%2Fchild",
		"Overwrite":   "T",
	})
	if code == http.StatusCreated || code == http.StatusNoContent {
		t.Errorf("a COPY with an encoded separator in Destination succeeded with %d", code)
	}
	for _, pair := range fs.copied {
		if strings.Contains(pair[1], "/dst/child") {
			t.Errorf("the encoded separator was resolved into a child path %q", pair[1])
		}
	}
}

func TestCopyAcceptsSameOriginDestination(t *testing.T) {
	h := newRecordingHandler(t)
	code, _ := serveDAV(t, h, "COPY", "/src", map[string]string{
		"Destination": "http://example.com/dst",
		"Overwrite":   "T",
	})
	// The source does not exist, so 404 is the expected outcome — what matters
	// is that the destination was not refused for its authority.
	if code == http.StatusBadGateway {
		t.Error("a same-origin Destination was rejected as foreign")
	}
}

// A consumer whose backend does its own normalisation must be able to turn the
// checks off and keep receiving the raw path.
func TestPathValidationCanBeDisabled(t *testing.T) {
	dir := t.TempDir()
	h := Handler{
		FileSystem:            &recordingFS{LocalFileSystem: LocalFileSystem(dir)},
		DisablePathValidation: true,
	}
	code, _ := serveDAV(t, h, http.MethodGet, "/a/../secret", nil)
	if code == http.StatusBadRequest {
		t.Error("path validation still rejected the request when disabled")
	}
}

// RFC 4918 §10.3 requires an absolute URI or absolute path. path.Clean leaves a
// leading .. in a relative path, so the canonicality check alone accepts one and
// the FileSystem is handed a path that escapes the served root.
func TestCopyRejectsRelativeDestination(t *testing.T) {
	for _, dest := range []string{"../../../etc/passwd", "secret", "./secret", "..%2f..%2fsecret"} {
		t.Run(dest, func(t *testing.T) {
			h := newRecordingHandler(t)
			code, fs := serveDAV(t, h, "COPY", "/src", map[string]string{
				"Destination": dest,
				"Overwrite":   "T",
			})
			if code == http.StatusCreated || code == http.StatusNoContent {
				t.Errorf("a relative Destination %q succeeded with %d", dest, code)
			}
			if len(fs.copied) != 0 {
				t.Errorf("the backend was asked to copy to %v despite a relative Destination", fs.copied)
			}
		})
	}
}

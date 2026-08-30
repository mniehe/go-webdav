package internal

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const backendSecret = "tenant_secret_42"

// safeErrorText used to treat every status below 500 as library-authored. The
// public backend API contradicts that: webdav.NewHTTPError lets a Backend return
// any status with any cause, so a 4xx carried the backend's own text — SQL,
// filesystem paths, tenant identifiers — straight to the client.
func TestServeErrorRedactsBackendTextAtEveryStatus(t *testing.T) {
	codes := []int{
		http.StatusBadRequest,
		http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusNotFound,
		http.StatusConflict,
		http.StatusPreconditionFailed,
		http.StatusInternalServerError,
	}
	for _, code := range codes {
		w := httptest.NewRecorder()
		// The shape a Backend produces via webdav.NewHTTPError.
		ServeError(w, &HTTPError{Code: code, Err: errors.New("database row " + backendSecret + " denied")})
		if strings.Contains(w.Body.String(), backendSecret) {
			t.Errorf("%d leaked backend detail: %s", code, strings.TrimSpace(w.Body.String()))
		}
		if w.Code != code {
			t.Errorf("code = %d, want %d", w.Code, code)
		}
	}
}

// The same text reaches a client through a multistatus entry, which never
// carries a status line of its own for the reader to discount.
func TestErrorResponseRedactsBackendText(t *testing.T) {
	resp := NewErrorResponse("/u/cal/a/e.ics", &HTTPError{
		Code: http.StatusNotFound,
		Err:  errors.New("no row for " + backendSecret),
	})
	if strings.Contains(resp.ResponseDescription, backendSecret) {
		t.Errorf("multistatus responsedescription leaked backend detail: %q", resp.ResponseDescription)
	}
}

// A message the library wrote describes the client's own mistake and stays
// visible, or every 4xx becomes undiagnosable.
func TestServeErrorKeepsLibraryText(t *testing.T) {
	w := httptest.NewRecorder()
	ServeError(w, HTTPErrorf(http.StatusBadRequest, "webdav: unparseable Depth header"))
	if !strings.Contains(w.Body.String(), "unparseable Depth header") {
		t.Errorf("library-authored 4xx text was redacted: %s", w.Body)
	}

	resp := NewErrorResponse("/p", HTTPErrorf(http.StatusForbidden, "webdav: href is outside the request collection"))
	if !strings.Contains(resp.ResponseDescription, "outside the request collection") {
		t.Errorf("library-authored multistatus text was redacted: %q", resp.ResponseDescription)
	}
}

// A backend error wrapped around a library error must not launder the backend's
// text into the response.
func TestWrappedBackendErrorStaysRedacted(t *testing.T) {
	w := httptest.NewRecorder()
	inner := HTTPErrorf(http.StatusForbidden, "webdav: not permitted")
	ServeError(w, &HTTPError{Code: http.StatusForbidden, Err: errors.New(backendSecret + ": " + inner.Error())})
	if strings.Contains(w.Body.String(), backendSecret) {
		t.Errorf("a wrapped backend error leaked its text: %s", strings.TrimSpace(w.Body.String()))
	}
}

// AGENTS.md tells a Backend to wrap with %w. That leaves no outer *HTTPError,
// so a chain walk finds the library's inner one and its safeText flag then
// vouches for the whole outer message.
func TestFmtWrappedBackendErrorStaysRedacted(t *testing.T) {
	w := httptest.NewRecorder()
	inner := HTTPErrorf(http.StatusForbidden, "webdav: not permitted")
	ServeError(w, fmt.Errorf("query %s failed: %w", backendSecret, inner))
	if strings.Contains(w.Body.String(), backendSecret) {
		t.Errorf("a %%w-wrapped backend error leaked its text: %s", strings.TrimSpace(w.Body.String()))
	}

	resp := NewErrorResponse("/p", fmt.Errorf("query %s failed: %w", backendSecret, inner))
	if strings.Contains(resp.ResponseDescription, backendSecret) {
		t.Errorf("a %%w-wrapped backend error leaked into the multistatus: %q", resp.ResponseDescription)
	}
}

// Status is not the client's data, so it is still recovered through the chain:
// %w is the documented way to carry it.
func TestFmtWrappedErrorKeepsStatus(t *testing.T) {
	w := httptest.NewRecorder()
	ServeError(w, fmt.Errorf("backend: %w", HTTPErrorf(http.StatusInsufficientStorage, "webdav: too much")))
	if w.Code != http.StatusInsufficientStorage {
		t.Errorf("code = %d, want 507 carried through the %%w wrap", w.Code)
	}
}

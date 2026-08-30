// This file is package webdav_test on purpose: it may use only what a consumer
// outside this module can reach, which is what the assertions below are about.
package webdav_test

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/mniehe/davkit"
)

var errStore = errors.New("store: row is gone")

// A consumer cannot import internal/, so the status code carried by an error is
// reachable only if the type naming it is exported from here. Without that a
// client cannot tell 404 from 403, 409, 412 or 507.
func TestHTTPErrorIsReachableByConsumers(t *testing.T) {
	err := webdav.NewHTTPError(http.StatusPreconditionFailed, errStore)

	var httpErr *webdav.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("errors.As could not recover a *webdav.HTTPError from %v (%T)", err, err)
	}
	if httpErr.Code != http.StatusPreconditionFailed {
		t.Errorf("Code = %d, want %d", httpErr.Code, http.StatusPreconditionFailed)
	}
	if !errors.Is(err, errStore) {
		t.Error("the cause is not reachable through Unwrap")
	}
}

// A Backend commonly wraps the library's error with its own context; the status
// has to survive that, or callers must match on strings instead.
func TestHTTPErrorSurvivesWrapping(t *testing.T) {
	wrapped := fmt.Errorf("syncing calendar: %w", webdav.NewHTTPError(http.StatusNotFound, errStore))

	var httpErr *webdav.HTTPError
	if !errors.As(wrapped, &httpErr) {
		t.Fatalf("errors.As could not recover a *webdav.HTTPError from a wrapped error")
	}
	if httpErr.Code != http.StatusNotFound {
		t.Errorf("Code = %d, want %d", httpErr.Code, http.StatusNotFound)
	}
}

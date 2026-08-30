// Package internal provides low-level helpers for WebDAV clients and servers.
package internal

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

// Depth indicates whether a request applies to the resource's members. It's
// defined in RFC 4918 section 10.2.
type Depth int

const (
	// DepthZero indicates that the request applies only to the resource.
	DepthZero Depth = 0
	// DepthOne indicates that the request applies to the resource and its
	// internal members only.
	DepthOne Depth = 1
	// DepthInfinity indicates that the request applies to the resource and all
	// of its members.
	DepthInfinity Depth = -1
)

// ParseDepth parses a Depth header.
func ParseDepth(s string) (Depth, error) {
	switch s {
	case "0":
		return DepthZero, nil
	case "1":
		return DepthOne, nil
	case "infinity":
		return DepthInfinity, nil
	}
	return 0, fmt.Errorf("webdav: invalid Depth value")
}

// String formats the depth.
func (d Depth) String() string {
	switch d {
	case DepthZero:
		return "0"
	case DepthOne:
		return "1"
	case DepthInfinity:
		return "infinity"
	}
	panic("webdav: invalid Depth value")
}

// ParseOverwrite parses an Overwrite header.
func ParseOverwrite(s string) (bool, error) {
	switch s {
	case "T":
		return true, nil
	case "F":
		return false, nil
	}
	return false, fmt.Errorf("webdav: invalid Overwrite value")
}

// FormatOverwrite formats an Overwrite header.
func FormatOverwrite(overwrite bool) string {
	if overwrite {
		return "T"
	} else {
		return "F"
	}
}

type HTTPError struct {
	Code int
	Err  error

	// safeText marks a message the library itself wrote, which describes the
	// client's own request and so can be returned verbatim. The status code
	// cannot stand in for it: NewHTTPError lets a Backend return 403 with a
	// database error as its cause.
	safeText bool
}

func HTTPErrorFromError(err error) *HTTPError {
	if err == nil {
		return nil
	}
	if httpErr, ok := err.(*HTTPError); ok {
		return httpErr
	} else {
		return &HTTPError{Code: http.StatusInternalServerError, Err: err}
	}
}

func IsNotFound(err error) bool {
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.Code == http.StatusNotFound
	}
	return false
}

func HTTPErrorf(code int, format string, a ...interface{}) *HTTPError {
	return &HTTPError{Code: code, Err: fmt.Errorf(format, a...), safeText: true}
}

// SafeHTTPError wraps a cause the library produced itself, for paths that wrap an
// existing error rather than format a message. Never pass a Backend's error.
func SafeHTTPError(code int, cause error) *HTTPError {
	return &HTTPError{Code: code, Err: cause, safeText: true}
}

// hasSafeText reports whether err's message may be returned to the client. Only
// the outermost error decides: a Backend wrapping a library error with %w leaves
// no outer *HTTPError, and a chain walk would let the inner error's flag vouch
// for the Backend's own text.
func hasSafeText(err error) bool {
	httpErr, ok := err.(*HTTPError)
	return ok && httpErr.safeText
}

func (err *HTTPError) Error() string {
	s := fmt.Sprintf("%v %v", err.Code, http.StatusText(err.Code))
	if err.Err != nil {
		return fmt.Sprintf("%v: %v", s, err.Err)
	} else {
		return s
	}
}

func (err *HTTPError) Unwrap() error {
	return err.Err
}

type HrefError struct {
	Href url.URL
	Err  error
}

func (err *HrefError) Error() string {
	return fmt.Sprintf("%v: %v", err.Href, err.Err)
}

func (err *HrefError) Unwrap() error {
	return err.Err
}

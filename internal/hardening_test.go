package internal

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRawXMLValueDepthLimit(t *testing.T) {
	// Deeply nested XML must be rejected with an error rather than recursing
	// until the goroutine stack overflows (a fatal, unrecoverable crash).
	body := `<?xml version="1.0"?><propfind xmlns="DAV:"><prop>` +
		strings.Repeat("<x>", maxXMLDepth+50) + strings.Repeat("</x>", maxXMLDepth+50) +
		`</prop></propfind>`

	req := httptest.NewRequest("PROPFIND", "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/xml")
	var pf PropFind
	if err := DecodeXMLRequest(req, &pf); err == nil {
		t.Fatal("expected a depth-limit error for deeply nested XML, got nil")
	}
}

func TestDecodeXMLRequestBodyLimit(t *testing.T) {
	body := `<?xml version="1.0"?><propfind xmlns="DAV:"><prop>` +
		strings.Repeat("<a></a>", int(MaxXMLBodySize)/7+16) + `</prop></propfind>`
	req := httptest.NewRequest("PROPFIND", "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/xml")
	var pf PropFind
	if err := DecodeXMLRequest(req, &pf); err == nil {
		t.Fatal("expected an error for an oversized body, got nil")
	}
}

func TestCleanPath(t *testing.T) {
	tests := []struct {
		in       string
		wantOK   bool
		wantPath string
	}{
		{"/a/b/", true, "/a/b/"},
		{"/a/b", true, "/a/b"},
		{"/a/../b", false, "/b"},
		{"/a//b", false, "/a/b"},
		{"/a/./b", false, "/a/b"},
	}
	for _, tt := range tests {
		got, ok := CleanPath(tt.in)
		if ok != tt.wantOK || got != tt.wantPath {
			t.Errorf("CleanPath(%q) = (%q, %v), want (%q, %v)", tt.in, got, ok, tt.wantPath, tt.wantOK)
		}
	}
}

func TestChildHref(t *testing.T) {
	tests := []struct {
		collection string
		href       string
		wantOK     bool
	}{
		{"/u/cal/a/", "/u/cal/a/e.ics", true},
		{"/u/cal/a/", "/u/cal/b/e.ics", false},
		{"/u/cal/a/", "/u/cal/a/../b/e.ics", false},
		{"/u/cal/a/", "e.ics", false},
		{"/u/cal/a/", "", false},
		// The authority is part of the question: RFC 6352 §8.7 requires each
		// href to identify a resource in the addressed collection, and one on
		// another origin does not, however well its path matches.
		{"/u/cal/a/", "http://example.com/u/cal/a/e.ics", true},
		{"/u/cal/a/", "https://evil.example/u/cal/a/e.ics", false},
		// So is the escaped form: %2F decodes to a separator, re-splitting the
		// path into one the prefix check never saw.
		{"/u/cal/a/", "/u/cal/a%2F../b/e.ics", false},
		{"/u/cal/a/", "/u/cal%2Fa/e.ics", false},
	}
	for _, tt := range tests {
		href := &Href{}
		if tt.href != "" {
			if err := href.UnmarshalText([]byte(tt.href)); err != nil {
				t.Fatalf("parse %q: %v", tt.href, err)
			}
		}
		r := httptest.NewRequest("REPORT", "http://example.com"+tt.collection, http.NoBody)
		if _, ok := ChildHref(r, tt.collection, href); ok != tt.wantOK {
			t.Errorf("ChildHref(%q, %q) ok = %v, want %v", tt.collection, tt.href, ok, tt.wantOK)
		}
	}
}

func TestServeErrorRedacts5xx(t *testing.T) {
	// A bare backend error (500) must not leak its text.
	w := httptest.NewRecorder()
	ServeError(w, errors.New("SECRET: sqlite no such column salary"))
	if strings.Contains(w.Body.String(), "SECRET") {
		t.Errorf("5xx response leaked backend detail: %s", w.Body)
	}
	if w.Code != http.StatusInternalServerError {
		t.Errorf("code = %d, want 500", w.Code)
	}

	// A 4xx the library constructed is safe to show.
	w = httptest.NewRecorder()
	ServeError(w, HTTPErrorf(http.StatusBadRequest, "bad depth header"))
	if !strings.Contains(w.Body.String(), "bad depth header") {
		t.Errorf("4xx message was redacted: %s", w.Body)
	}
}

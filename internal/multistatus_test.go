package internal

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func streamResponse(path string) *Response {
	return &Response{
		Hrefs: []Href{{Path: path}},
		PropStats: []PropStat{{
			Status: Status{Code: http.StatusOK},
			Prop:   Prop{Raw: []RawXMLValue{*NewRawXMLElement(xml.Name{Space: Namespace, Local: "getetag"}, nil, nil)}},
		}},
	}
}

// A truncated document is worse than a refused one: the client cannot tell a
// server that gave up from one that is still sending.
func assertWellFormed(t *testing.T, body string) {
	t.Helper()
	dec := xml.NewDecoder(strings.NewReader(body))
	for {
		_, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			t.Fatalf("response is not well-formed XML: %v\n%s", err, body)
		}
	}
}

func TestMultiStatusWriterStreams(t *testing.T) {
	rec := httptest.NewRecorder()
	m := NewMultiStatusWriter(rec, "/c", 0)

	if err := m.Write(streamResponse("/c/a")); err != nil {
		t.Fatal(err)
	}
	afterFirst := rec.Body.Len()
	if afterFirst == 0 {
		t.Fatal("nothing reached the client after the first response")
	}
	if err := m.Write(streamResponse("/c/b")); err != nil {
		t.Fatal(err)
	}
	if rec.Body.Len() <= afterFirst {
		t.Error("the second response did not add to what had already been written")
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}

	assertWellFormed(t, rec.Body.String())
	if rec.Code != http.StatusMultiStatus {
		t.Errorf("code = %d, want 207", rec.Code)
	}
	for _, want := range []string{"/c/a", "/c/b", "</multistatus>"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("response does not contain %q:\n%s", want, rec.Body)
		}
	}
}

// Once the status line is out it cannot be retracted, so a later failure has to
// be reported inside the body.
func TestMultiStatusWriterReportsAMidStreamFailureInTheBody(t *testing.T) {
	rec := httptest.NewRecorder()
	m := NewMultiStatusWriter(rec, "/c", 0)

	if err := m.Write(streamResponse("/c/a")); err != nil {
		t.Fatal(err)
	}
	if !m.Started() {
		t.Fatal("Started() is false after a response was written")
	}
	if err := m.Fail(HTTPErrorf(http.StatusForbidden, "webdav: gave up")); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}

	body := rec.Body.String()
	assertWellFormed(t, body)
	if rec.Code != http.StatusMultiStatus {
		t.Errorf("code = %d, want the 207 already sent", rec.Code)
	}
	if !strings.Contains(body, "403") {
		t.Errorf("the mid-stream failure was not reported in the body:\n%s", body)
	}
	if !strings.HasSuffix(strings.TrimSpace(body), "</multistatus>") {
		t.Errorf("the document was not closed:\n%s", body)
	}
}

// A failure before anything is written leaves the status open, so the handler
// can still answer with a plain HTTP error.
func TestMultiStatusWriterLeavesTheStatusOpenBeforeTheFirstWrite(t *testing.T) {
	rec := httptest.NewRecorder()
	m := NewMultiStatusWriter(rec, "/c", 0)
	if m.Started() {
		t.Error("Started() is true before anything was written")
	}
	if err := m.Abort(); err != nil {
		t.Fatal(err)
	}
	if m.Started() {
		t.Error("Abort started the document it was meant to leave alone")
	}
	if rec.Body.Len() != 0 {
		t.Error("the writer wrote before it was asked to")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("code = %d, want the status still unset", rec.Code)
	}
}

func TestMultiStatusWriterStopsAtItsBudget(t *testing.T) {
	rec := httptest.NewRecorder()
	m := NewMultiStatusWriter(rec, "/c", 512)

	for i := 0; i < 200; i++ {
		if err := m.Write(streamResponse(fmt.Sprintf("/c/%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}

	body := rec.Body.String()
	assertWellFormed(t, body)
	if !strings.Contains(body, "number-of-matches-within-limits") {
		t.Errorf("the truncated document does not say why:\n%s", body)
	}
	if !strings.Contains(body, "507") {
		t.Errorf("the overflow was not reported as 507:\n%s", body)
	}
	if !strings.HasSuffix(strings.TrimSpace(body), "</multistatus>") {
		t.Errorf("the document was not closed after the budget tripped:\n%s", body)
	}
	if strings.Contains(body, "/c/199") {
		t.Error("responses were still written after the budget was spent")
	}
}

func TestMultiStatusWriterBudgetConvention(t *testing.T) {
	t.Run("a negative budget removes the bound", func(t *testing.T) {
		rec := httptest.NewRecorder()
		m := NewMultiStatusWriter(rec, "/c", -1)
		for i := 0; i < 200; i++ {
			if err := m.Write(streamResponse(fmt.Sprintf("/c/%d", i))); err != nil {
				t.Fatal(err)
			}
		}
		if err := m.Close(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(rec.Body.String(), "/c/199") {
			t.Error("the bound was applied although it was removed")
		}
	})

	t.Run("an empty multistatus is still a document", func(t *testing.T) {
		rec := httptest.NewRecorder()
		m := NewMultiStatusWriter(rec, "/c", 0)
		if err := m.Close(); err != nil {
			t.Fatal(err)
		}
		assertWellFormed(t, rec.Body.String())
		if rec.Code != http.StatusMultiStatus {
			t.Errorf("code = %d, want 207", rec.Code)
		}
	})

	t.Run("the sync token is emitted before the end tag", func(t *testing.T) {
		rec := httptest.NewRecorder()
		m := NewMultiStatusWriter(rec, "/c", 0)
		m.SetSyncToken("tok-42")
		if err := m.Write(streamResponse("/c/a")); err != nil {
			t.Fatal(err)
		}
		if err := m.Close(); err != nil {
			t.Fatal(err)
		}
		body := rec.Body.String()
		assertWellFormed(t, body)
		tok := strings.Index(body, "tok-42")
		end := strings.Index(body, "</multistatus>")
		if tok < 0 || end < 0 || tok > end {
			t.Errorf("sync-token is not inside the document:\n%s", body)
		}
	})
}

// failingPropfindBackend fails before producing anything, so the status line is
// still the handler's to choose.
type failingPropfindBackend struct{ Backend }

func (b *failingPropfindBackend) PropFind(r *http.Request, pf *PropFind, depth Depth, emit func(*Response) error) error {
	return HTTPErrorf(http.StatusForbidden, "webdav: not permitted")
}

// Deferring Close rather than Abort would commit a 207 and an empty document
// here, burying the error the handler still wanted to send.
func TestPropFindFailingBeforeAnyResponseIsAnHTTPError(t *testing.T) {
	h := Handler{Backend: &failingPropfindBackend{}}
	req := httptest.NewRequest("PROPFIND", "/c", strings.NewReader(
		`<?xml version="1.0"?><d:propfind xmlns:d="DAV:"><d:prop><d:getetag/></d:prop></d:propfind>`))
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("Depth", "1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("code = %d, want 403 for a failure before anything was written", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "multistatus") {
		t.Errorf("a document was started for a request that produced nothing:\n%s", rec.Body)
	}
}

// The budget has to stop the response that busts it, not merely the one after.
// A single response is not small by nature — expanded recurrence instances make
// one arbitrarily large — so a check made only once the bytes are already on the
// wire bounds nothing.
func TestMultiStatusWriterBoundsASingleResponse(t *testing.T) {
	rec := httptest.NewRecorder()
	m := NewMultiStatusWriter(rec, "/c", 4096)

	if err := m.Write(streamResponse("/c/" + strings.Repeat("a", 100_000))); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}

	body := rec.Body.String()
	assertWellFormed(t, body)
	if len(body) > 2*4096 {
		t.Errorf("wrote %d bytes against a 4096-byte budget", len(body))
	}
	if !strings.Contains(body, "number-of-matches-within-limits") {
		t.Errorf("the oversized response was not reported as an overflow:\n%s", body)
	}
	if !strings.HasSuffix(strings.TrimSpace(body), "</multistatus>") {
		t.Errorf("the document was not closed:\n%s", body)
	}
}

// lateFailingPropfindBackend emits one response and then fails, which is the
// only arm where Fail matters: the 207 is already committed on its behalf.
type lateFailingPropfindBackend struct{ Backend }

func (b *lateFailingPropfindBackend) PropFind(r *http.Request, pf *PropFind, depth Depth, emit func(*Response) error) error {
	if err := emit(streamResponse("/c/a")); err != nil {
		return err
	}
	return HTTPErrorf(http.StatusInternalServerError, "webdav: backend gave up")
}

// Returning the error to ServeHTTP instead would close the document and then
// append the error text after </multistatus>, which no client can parse.
func TestPropFindFailingMidStreamStaysInsideTheDocument(t *testing.T) {
	h := Handler{Backend: &lateFailingPropfindBackend{}}
	req := httptest.NewRequest("PROPFIND", "/c", strings.NewReader(
		`<?xml version="1.0"?><d:propfind xmlns:d="DAV:"><d:prop><d:getetag/></d:prop></d:propfind>`))
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("Depth", "1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	body := rec.Body.String()
	assertWellFormed(t, body)
	if rec.Code != http.StatusMultiStatus {
		t.Errorf("code = %d, want the 207 already sent", rec.Code)
	}
	if got := strings.TrimSpace(body); !strings.HasSuffix(got, "</multistatus>") {
		t.Errorf("error text was written outside the document:\n%s", body)
	}

	var ms MultiStatus
	if err := xml.Unmarshal([]byte(body), &ms); err != nil {
		t.Fatalf("failed to decode multistatus: %v\n%s", err, body)
	}
	last := ms.Responses[len(ms.Responses)-1]
	if last.Status == nil || last.Status.Code != http.StatusInternalServerError {
		t.Errorf("the mid-stream failure was not reported as a 500 response entry:\n%s", body)
	}
	// RFC 4918 §14.24: a response carries either a status or propstats.
	if len(last.PropStats) != 0 {
		t.Errorf("the failure response also carries propstats:\n%s", body)
	}
}

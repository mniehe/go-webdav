package internal

import (
	"encoding/xml"
	"io"
	"net/http"
)

// MaxResponseBytes bounds a streamed multistatus. Streaming removes the memory
// cost of a large response but not the work, and unlike MaxResponsePropBytes
// this counts what actually reached the wire — the only way to bound content
// whose size the request does not predict, such as calendar data or expanded
// recurrence instances.
const MaxResponseBytes = 128 << 20

type countingWriter struct {
	w http.ResponseWriter
	n int
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += n
	return n, err
}

// stagingWriter holds one response back from the wire until its encoded size is
// known. Past the cap it keeps accepting and discards: the encoder has to finish
// the element either way, or its namespace state would not be balanced for the
// next one.
type stagingWriter struct {
	w     io.Writer
	buf   []byte
	limit int
	on    bool
	over  bool
}

func (s *stagingWriter) Write(p []byte) (int, error) {
	switch {
	case !s.on:
		return s.w.Write(p)
	case s.over:
		return len(p), nil
	case len(s.buf)+len(p) > s.limit:
		s.over = true
		s.buf = s.buf[:0]
		return len(p), nil
	default:
		s.buf = append(s.buf, p...)
		return len(p), nil
	}
}

func (s *stagingWriter) begin(limit int) {
	s.buf, s.limit, s.on, s.over = s.buf[:0], limit, true, false
}

func (s *stagingWriter) commit() error {
	s.on = false
	if len(s.buf) == 0 {
		return nil
	}
	_, err := s.w.Write(s.buf)
	s.buf = s.buf[:0]
	return err
}

func (s *stagingWriter) discard() {
	s.buf, s.on, s.over = s.buf[:0], false, false
}

// MultiStatusWriter streams a DAV:multistatus one response at a time.
//
// The status line is written with the first response rather than up front, so a
// failure before that can still be reported as an HTTP error. Once it is out the
// code cannot be retracted, and every later failure has to travel inside the
// document — see Fail. Close always emits the end tag, so a client parses a
// complete document even when the server gave up part-way.
type MultiStatusWriter struct {
	w     http.ResponseWriter
	path  string
	limit int

	counted   *countingWriter
	stage     *stagingWriter
	enc       *xml.Encoder
	syncToken string
	echoSize  int
	echoLimit int
	written   int
	started   bool
	closed    bool
	exhausted bool

	// declared distinguishes a truncation the caller announced -- for which it
	// also supplied a token describing exactly what was written -- from the
	// response budget running out mid-document, where the token in hand still
	// describes changes that were never sent.
	declared bool
}

// NewMultiStatusWriter streams a multistatus for the resource at path. A zero
// limit means MaxResponseBytes; a negative one removes the bound.
func NewMultiStatusWriter(w http.ResponseWriter, path string, limit int) *MultiStatusWriter {
	if limit == 0 {
		limit = MaxResponseBytes
	}
	return &MultiStatusWriter{w: w, path: path, limit: limit}
}

// Started reports whether any of the document has been written, and so whether
// the status code is still open to change.
func (m *MultiStatusWriter) Started() bool { return m.started }

// SetSyncToken records the RFC 6578 token to emit before the end tag.
func (m *MultiStatusWriter) SetSyncToken(token string) { m.syncToken = token }

// LimitPropEcho stops the document once the echoed property names would exceed
// limit across the responses written. This is BoundResponseWork for a path whose
// resource count is not known before the responses are produced; where it is
// known, the caller checks up front and refuses without a body.
func (m *MultiStatusWriter) LimitPropEcho(echoSize, limit int) {
	if limit == 0 {
		limit = MaxResponsePropBytes
	}
	m.echoSize = echoSize
	m.echoLimit = limit
}

func (m *MultiStatusWriter) start() error {
	if m.started {
		return nil
	}
	m.started = true

	m.w.Header().Add("Content-Type", "application/xml; charset=\"utf-8\"")
	m.w.WriteHeader(http.StatusMultiStatus)

	m.counted = &countingWriter{w: m.w}
	if _, err := m.counted.Write([]byte(xml.Header)); err != nil {
		return err
	}
	m.stage = &stagingWriter{w: m.counted}
	m.enc = xml.NewEncoder(m.stage)
	if err := m.enc.EncodeToken(xml.StartElement{
		Name: xml.Name{Local: "multistatus"},
		Attr: []xml.Attr{{Name: xml.Name{Local: "xmlns"}, Value: Namespace}},
	}); err != nil {
		return err
	}
	// The open tag has to reach the wire before the first response is staged, or
	// it would still be buffered in the encoder and be discarded along with a
	// response that overflows.
	return m.enc.Flush()
}

// Write emits one response. After the budget is spent it reports the overflow
// once and then drops further responses, leaving the document well formed.
func (m *MultiStatusWriter) Write(resp *Response) error {
	if m.closed || m.exhausted {
		return nil
	}
	if err := m.start(); err != nil {
		return err
	}
	// Staged rather than streamed straight out: a response's size is not known
	// until it is encoded, and a budget consulted only afterwards cannot stop the
	// response that busts it.
	bounded := m.limit > 0
	if bounded {
		m.stage.begin(m.limit - m.counted.n)
	}
	err := m.enc.Encode(resp)
	if err == nil {
		err = m.enc.Flush()
	}
	if err != nil {
		m.stage.discard()
		return err
	}
	if bounded {
		if m.stage.over {
			m.stage.discard()
			return m.overflow()
		}
		if err := m.stage.commit(); err != nil {
			return err
		}
	}

	m.written++
	if m.echoLimit > 0 && m.echoSize > 0 && m.echoSize > m.echoLimit/m.written {
		return m.overflow()
	}
	return nil
}

// Fail reports an error that surfaced after the document was started. RFC 4918
// §14.24 gives a response either a status or propstats, never both, so the
// failure becomes a bare href-and-status entry.
func (m *MultiStatusWriter) Fail(err error) error {
	if m.closed || m.exhausted {
		return nil
	}
	if serr := m.start(); serr != nil {
		return serr
	}
	if eerr := m.enc.Encode(NewErrorResponse(m.path, err)); eerr != nil {
		return eerr
	}
	return m.enc.Flush()
}

// Truncate ends the result set with the marker RFC 6352 section 8.6.2 requires
// when a server returns fewer matches than it found: a 507 for the request URI
// naming DAV:number-of-matches-within-limits. Without it a client reads a
// shortened result as the complete set.
func (m *MultiStatusWriter) Truncate() error {
	if m.closed || m.exhausted {
		return nil
	}
	if err := m.start(); err != nil {
		return err
	}
	m.declared = true
	return m.overflow()
}

func (m *MultiStatusWriter) overflow() error {
	m.exhausted = true
	resp := NewErrorResponse(m.path, NewPreconditionError(http.StatusInsufficientStorage, NumberOfMatchesWithinLimitsName))
	if err := m.enc.Encode(resp); err != nil {
		return err
	}
	return m.enc.Flush()
}

// Abort ends a document that was started and does nothing to one that was not,
// so it is safe to defer alongside the explicit Close on the success path. A
// bare Close would start — and so commit the 207 for — a request that failed
// before producing anything, when the caller still wants to answer with an
// HTTP error.
func (m *MultiStatusWriter) Abort() error {
	if !m.started {
		return nil
	}
	return m.Close()
}

// Close ends the document. It is safe to call more than once and on a writer
// that never wrote a response, which is how an empty multistatus is served.
func (m *MultiStatusWriter) Close() error {
	if m.closed {
		return nil
	}
	if err := m.start(); err != nil {
		return err
	}
	m.closed = true

	// RFC 6578 §3.6: the token must describe the changes actually reported, and
	// a caller that declared its own truncation supplied exactly that. A document
	// cut short by the response budget did not: the token in hand covers members
	// that were never sent, so it is withheld and the client resyncs in full.
	if m.syncToken != "" && (!m.exhausted || m.declared) {
		// Written as a bare local name so it inherits the document's default
		// namespace, as the buffered encoder did; a DAV:-qualified name would add
		// a redundant xmlns to every token.
		start := xml.StartElement{Name: xml.Name{Local: "sync-token"}}
		if err := m.enc.EncodeToken(start); err != nil {
			return err
		}
		if err := m.enc.EncodeToken(xml.CharData(m.syncToken)); err != nil {
			return err
		}
		if err := m.enc.EncodeToken(xml.EndElement{Name: start.Name}); err != nil {
			return err
		}
	}
	if err := m.enc.EncodeToken(xml.EndElement{Name: xml.Name{Local: "multistatus"}}); err != nil {
		return err
	}
	return m.enc.Flush()
}

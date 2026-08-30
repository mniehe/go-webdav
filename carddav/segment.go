package carddav

import (
	"fmt"
	"unicode"
	"unicode/utf8"
)

// maxSegmentLen bounds a segment so a hostile name cannot make the handler
// build an arbitrarily long URL out of it. Generous for an address book or item
// name; every real client generates UUIDs.
const maxSegmentLen = 255

// Segment is a validated URL path segment: never empty, never containing a
// slash, a dot segment, or a control character. The zero value is invalid.
//
// Validation happens once, on construction, which is why nothing downstream has
// to defend against a name that escapes its collection. Segments are comparable
// and usable as map keys.
type Segment struct {
	s string
}

// ParseSegment validates s as a single path segment.
func ParseSegment(s string) (Segment, error) {
	switch {
	case s == "":
		return Segment{}, fmt.Errorf("carddav: empty path segment")
	case s == "." || s == "..":
		return Segment{}, fmt.Errorf("carddav: %q is a dot segment", s)
	case len(s) > maxSegmentLen:
		return Segment{}, fmt.Errorf("carddav: path segment is %d bytes, over the %d limit", len(s), maxSegmentLen)
	case !utf8.ValidString(s):
		return Segment{}, fmt.Errorf("carddav: path segment is not valid UTF-8")
	}
	for _, r := range s {
		if r == '/' {
			return Segment{}, fmt.Errorf("carddav: path segment %q contains a slash", s)
		}
		if unicode.IsControl(r) {
			return Segment{}, fmt.Errorf("carddav: path segment %q contains a control character", s)
		}
	}
	return Segment{s: s}, nil
}

// MustSegment is ParseSegment for values known good at compile time, such as
// literals in tests and fixtures. It panics on anything else.
func MustSegment(s string) Segment {
	seg, err := ParseSegment(s)
	if err != nil {
		panic(err)
	}
	return seg
}

func (s Segment) String() string { return s.s }

// IsZero reports whether s is the invalid zero value. The library checks this
// on everything a backend returns.
func (s Segment) IsZero() bool { return s.s == "" }

// AccountID is a stable account identity from your system. It is never
// interpreted as a URL and never parsed. Renaming an account's URL must not
// change it.
type AccountID string

// Actor is whoever is making the request.
type Actor struct {
	Account AccountID

	// Claims is your own identity value, passed through untouched. The library
	// never reads it; your Authorizer type-asserts it back to its own type.
	Claims any
}

// AddressBookRef names one address book. Account is the account that owns it, which is
// not necessarily the actor asking about it.
type AddressBookRef struct {
	Account AccountID
	Book    Segment
}

// ItemRef names one item within an address book.
type ItemRef struct {
	Book AddressBookRef
	Item Segment
}

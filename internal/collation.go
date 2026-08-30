package internal

import (
	"errors"
	"fmt"
	"strings"
)

// ErrUnsupportedCollation is wrapped so callers can re-report it with their own
// namespace's supported-collation precondition (RFC 4791 §7.5.1, RFC 6352
// §10.5.1).
var ErrUnsupportedCollation = errors.New("webdav: unsupported collation")

// Collations every CalDAV and CardDAV server must support. i;ascii-casemap is
// CalDAV's default, i;unicode-casemap CardDAV's.
const (
	CollationOctet          = "i;octet"
	CollationASCIICasemap   = "i;ascii-casemap"
	CollationUnicodeCasemap = "i;unicode-casemap"
)

// FoldForCollation reduces s to the form the named collation compares. An empty
// collation means defaultCollation.
//
// i;unicode-casemap (RFC 5051) is approximated by lowercasing rather than the
// full NFKC-then-titlecase mapping, to avoid a dependency on x/text.
func FoldForCollation(collation, defaultCollation, s string) (string, error) {
	if collation == "" {
		collation = defaultCollation
	}
	switch collation {
	case CollationOctet:
		return s, nil
	case CollationASCIICasemap:
		return asciiLower(s), nil
	case CollationUnicodeCasemap:
		return strings.ToLower(s), nil
	}
	return "", fmt.Errorf("%w %q", ErrUnsupportedCollation, collation)
}

// asciiLower lowercases only A-Z, which is all i;ascii-casemap defines.
func asciiLower(s string) string {
	var b []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			if b == nil {
				b = []byte(s)
			}
			b[i] = c + ('a' - 'A')
		}
	}
	if b == nil {
		return s
	}
	return string(b)
}

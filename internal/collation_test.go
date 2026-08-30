package internal

import (
	"errors"
	"testing"
)

// i;ascii-casemap (RFC 4790 §9.2) folds only A-Z; i;unicode-casemap (RFC 5051)
// folds the whole repertoire. Every case below therefore carries a non-ASCII
// letter, which is the only thing that separates the two.
func TestFoldForCollation(t *testing.T) {
	for _, tc := range []struct {
		name             string
		collation        string
		defaultCollation string
		in               string
		want             string
	}{
		{"octet preserves case", CollationOctet, CollationASCIICasemap, "ÉaB", "ÉaB"},
		{"ascii-casemap folds A-Z only", CollationASCIICasemap, CollationOctet, "ÉaB", "Éab"},
		{"unicode-casemap folds beyond ASCII", CollationUnicodeCasemap, CollationOctet, "ÉaB", "éab"},
		{"empty takes the ascii-casemap default", "", CollationASCIICasemap, "ÉaB", "Éab"},
		{"empty takes the unicode-casemap default", "", CollationUnicodeCasemap, "ÉaB", "éab"},
		{"empty takes the octet default", "", CollationOctet, "ÉaB", "ÉaB"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := FoldForCollation(tc.collation, tc.defaultCollation, tc.in)
			if err != nil {
				t.Fatalf("FoldForCollation: %v", err)
			}
			if got != tc.want {
				t.Errorf("FoldForCollation(%q, %q, %q) = %q, want %q", tc.collation, tc.defaultCollation, tc.in, got, tc.want)
			}
		})
	}
}

func TestFoldForCollationRejectsUnknownCollation(t *testing.T) {
	got, err := FoldForCollation("i;made-up", CollationASCIICasemap, "ÉaB")
	if !errors.Is(err, ErrUnsupportedCollation) {
		t.Fatalf("err = %v, want ErrUnsupportedCollation", err)
	}
	if got != "" {
		t.Errorf("a rejected collation returned %q; callers must not compare it", got)
	}
}

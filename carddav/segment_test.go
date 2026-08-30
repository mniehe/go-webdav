package carddav

import (
	"strings"
	"testing"
)

func TestParseSegment(t *testing.T) {
	for _, tc := range []struct {
		name  string
		in    string
		valid bool
	}{
		{"ordinary", "work", true},
		{"with an extension", "meeting.ics", true},
		{"spaces and ampersands", "Work & Home", true},
		{"non-ASCII", "café", true},
		{"a single dot inside", "a.b", true},
		{"at the length limit", strings.Repeat("a", maxSegmentLen), true},

		{"empty", "", false},
		{"dot", ".", false},
		{"dot dot", "..", false},
		{"a slash", "a/b", false},
		{"a trailing slash", "work/", false},
		{"a newline", "a\nb", false},
		{"a null byte", "a\x00b", false},
		{"invalid UTF-8", "a\xffb", false},
		{"over the length limit", strings.Repeat("a", maxSegmentLen+1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seg, err := ParseSegment(tc.in)
			switch {
			case tc.valid && err != nil:
				t.Fatalf("ParseSegment(%q) = %v, want it accepted", tc.in, err)
			case !tc.valid && err == nil:
				t.Fatalf("ParseSegment(%q) was accepted as %q, want an error", tc.in, seg)
			case !tc.valid:
				return
			}
			if seg.String() != tc.in {
				t.Errorf("String() = %q, want %q", seg.String(), tc.in)
			}
			if seg.IsZero() {
				t.Error("a parsed segment reports IsZero")
			}
		})
	}
}

func TestZeroSegmentIsInvalid(t *testing.T) {
	var zero Segment
	if !zero.IsZero() {
		t.Error("the zero Segment does not report IsZero; the library uses this to reject backend output")
	}
	if zero.String() != "" {
		t.Errorf("the zero Segment prints as %q", zero.String())
	}
}

func TestMustSegmentPanicsOnInvalid(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("MustSegment accepted a slash")
		}
	}()
	MustSegment("a/b")
}

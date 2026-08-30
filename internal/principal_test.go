package internal

import "testing"

func TestPrincipalCollectionPath(t *testing.T) {
	for _, tc := range []struct {
		principal string
		want      string
	}{
		{"/dav/cal/alice", "/dav/cal/"},
		{"/dav/cal/alice/", "/dav/cal/"},
		{"/user", "/"},
		{"/user/", "/"},
		{"/", "/"},
		{"", "/"},
	} {
		if got := PrincipalCollectionPath(tc.principal); got != tc.want {
			t.Errorf("PrincipalCollectionPath(%q) = %q, want %q", tc.principal, got, tc.want)
		}
	}
}

func TestParseHrefKeepsAbsoluteURIs(t *testing.T) {
	for _, tc := range []string{
		"mailto:alice@example.com",
		"/dav/cal/alice/",
		"https://example.com/dav/",
	} {
		href, err := ParseHref(tc)
		if err != nil {
			t.Fatalf("ParseHref(%q): %v", tc, err)
		}
		if got := href.String(); got != tc {
			t.Errorf("ParseHref(%q).String() = %q, want it unchanged", tc, got)
		}
	}
}

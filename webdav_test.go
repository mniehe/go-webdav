package webdav

import "testing"

func TestConditionalMatchMatchETag(t *testing.T) {
	tests := []struct {
		name   string
		header string
		stored string
		want   bool
		wantOK bool // false means an error is expected
	}{
		{"exact match", `"abc"`, "abc", true, true},
		{"no match", `"abc"`, "xyz", false, true},
		{"weak validator matches", `W/"abc"`, "abc", true, true},
		{"comma list matches second", `"a", "b"`, "b", true, true},
		{"comma list no match", `"a", "b"`, "c", false, true},
		{"wildcard matches existing", "*", "abc", true, true},
		{"wildcard no match on absent", "*", "", false, true},
		{"empty stored, non-wildcard", `"abc"`, "", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ConditionalMatch(tt.header).MatchETag(tt.stored)
			if tt.wantOK && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tt.wantOK && err == nil {
				t.Fatalf("expected an error, got none")
			}
			if got != tt.want {
				t.Errorf("MatchETag(%q, %q) = %v, want %v", tt.header, tt.stored, got, tt.want)
			}
		})
	}
}

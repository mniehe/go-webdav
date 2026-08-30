package caldav

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mniehe/davkit/internal"
)

func summaryObject(t *testing.T) *calendarObject {
	t.Helper()
	return &calendarObject{
		Path: "/u/c/e.ics",
		Data: parseCalendar(t, `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Test//EN
BEGIN:VEVENT
UID:u1
DTSTAMP:20260101T000000Z
DTSTART:20260101T100000Z
DTEND:20260101T110000Z
SUMMARY:Quarterly Board Meeting in ZÜRICH
END:VEVENT
END:VCALENDAR`),
	}
}

func summaryFilter(text, collation string) *compFilter {
	return &compFilter{
		Name: "VCALENDAR",
		Comps: []compFilter{{
			Name: "VEVENT",
			Props: []propFilter{{
				Name:      "SUMMARY",
				TextMatch: &textMatch{Text: text, Collation: collation},
			}},
		}},
	}
}

// RFC 4791 §7.5.1 makes i;ascii-casemap CalDAV's default collation, so an
// unqualified text-match is case-insensitive. Mirrors the CardDAV rule, whose
// default is i;unicode-casemap.
func TestCalendarTextMatchDefaultCollationIsCaseInsensitive(t *testing.T) {
	matched, err := matchObject(summaryFilter("board meeting", ""), summaryObject(t))
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if !matched {
		t.Error(`text-match "board meeting" did not match the SUMMARY; the default collation i;ascii-casemap is case-insensitive`)
	}
}

// i;ascii-casemap folds A-Z and nothing else, so a case difference outside ASCII
// stays a difference. This is the only observable that separates CalDAV's
// default from CardDAV's i;unicode-casemap.
func TestCalendarDefaultCollationDoesNotFoldBeyondASCII(t *testing.T) {
	matched, err := matchObject(summaryFilter("zürich", ""), summaryObject(t))
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if matched {
		t.Error(`text-match "zürich" matched SUMMARY:...ZÜRICH under the default collation; RFC 4791 §7.5.1 makes it i;ascii-casemap`)
	}

	matched, err = matchObject(summaryFilter("zürich", "i;unicode-casemap"), summaryObject(t))
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if !matched {
		t.Error(`i;unicode-casemap did not fold Ü; the non-match above would then say nothing about the default`)
	}
}

func TestCalendarTextMatchOctetCollationIsCaseSensitive(t *testing.T) {
	matched, err := matchObject(summaryFilter("board meeting", "i;octet"), summaryObject(t))
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if matched {
		t.Error("i;octet matched across a case difference; it compares bytes")
	}
}

// RFC 4791 §7.5.1: an unsupported collation is refused with 403 and the
// CALDAV:supported-collation precondition, in CalDAV's own namespace. A plain
// error would still fail the request, but leaves the client nothing to act on.
func TestCalendarTextMatchRejectsUnknownCollation(t *testing.T) {
	_, err := matchObject(summaryFilter("board meeting", "i;made-up"), summaryObject(t))
	if err == nil {
		t.Fatal("an unsupported collation was accepted; RFC 4791 §7.5.1 requires CALDAV:supported-collation")
	}

	w := httptest.NewRecorder()
	internal.ServeError(w, err)

	if w.Code != http.StatusForbidden {
		t.Errorf("code = %d, want 403", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "supported-collation") {
		t.Errorf("the body does not name supported-collation:\n%s", body)
	}
	if !strings.Contains(body, `"urn:ietf:params:xml:ns:caldav"`) {
		t.Errorf("supported-collation is not in the CalDAV namespace:\n%s", body)
	}
}

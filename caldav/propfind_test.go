package caldav_test

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/mniehe/davkit/caldav"
	"github.com/mniehe/davkit/caldavmem"
)

const (
	davNS    = "DAV:"
	caldavNS = "urn:ietf:params:xml:ns:caldav"
	appleNS  = "http://apple.com/ns/ical/"
	ctagNS   = "http://calendarserver.org/ns/"
)

func davName(local string) xml.Name    { return xml.Name{Space: davNS, Local: local} }
func caldavName(local string) xml.Name { return xml.Name{Space: caldavNS, Local: local} }
func appleName(local string) xml.Name  { return xml.Name{Space: appleNS, Local: local} }

// Decoding just enough of a multistatus to assert on it. The wire format is the
// contract these tests are about, so they read it rather than any in-process
// representation of it.
type multistatus struct {
	XMLName   xml.Name      `xml:"DAV: multistatus"`
	Responses []davResponse `xml:"DAV: response"`
	SyncToken string        `xml:"DAV: sync-token"`
}

type davResponse struct {
	Href      string     `xml:"DAV: href"`
	Status    string     `xml:"DAV: status"`
	PropStats []propStat `xml:"DAV: propstat"`
}

type propStat struct {
	Status string `xml:"DAV: status"`
	Prop   struct {
		Values []davProp `xml:",any"`
	} `xml:"DAV: prop"`
}

type davProp struct {
	XMLName xml.Name
	Inner   string `xml:",innerxml"`
}

func propfind(t *testing.T, h *caldav.Handler, target, depth, body string) multistatus {
	t.Helper()

	r := httptest.NewRequest("PROPFIND", target, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/xml")
	if depth != "" {
		r.Header.Set("Depth", depth)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusMultiStatus {
		t.Fatalf("PROPFIND %s: status = %d, want %d\n%s", target, w.Code, http.StatusMultiStatus, w.Body.String())
	}
	var ms multistatus
	if err := xml.Unmarshal(w.Body.Bytes(), &ms); err != nil {
		t.Fatalf("decoding multistatus: %v\n%s", err, w.Body.String())
	}
	return ms
}

func askFor(names ...xml.Name) string {
	body := `<?xml version="1.0"?><propfind xmlns="DAV:"><prop>`
	for _, name := range names {
		body += `<` + name.Local + ` xmlns="` + name.Space + `"/>`
	}
	return body + `</prop></propfind>`
}

const allProp = `<?xml version="1.0"?><propfind xmlns="DAV:"><allprop/></propfind>`

func (m multistatus) at(t *testing.T, href string) davResponse {
	t.Helper()

	for _, resp := range m.Responses {
		if resp.Href == href {
			return resp
		}
	}
	t.Fatalf("no response for %q; got %v", href, m.hrefs())
	return davResponse{}
}

func (m multistatus) hrefs() []string {
	hrefs := make([]string, 0, len(m.Responses))
	for _, resp := range m.Responses {
		hrefs = append(hrefs, resp.Href)
	}
	return hrefs
}

// found returns the status a property came back under, and whether it was
// reported at all.
func (r davResponse) found(name xml.Name) (int, bool) {
	for _, ps := range r.PropStats {
		for _, prop := range ps.Prop.Values {
			if prop.XMLName == name {
				return statusCode(ps.Status), true
			}
		}
	}
	return 0, false
}

func (r davResponse) value(t *testing.T, name xml.Name) string {
	t.Helper()

	for _, ps := range r.PropStats {
		for _, prop := range ps.Prop.Values {
			if prop.XMLName != name {
				continue
			}
			if code := statusCode(ps.Status); code != http.StatusOK {
				t.Fatalf("%s came back %d, want 200", name.Local, code)
			}
			return prop.Inner
		}
	}
	t.Fatalf("%s was not reported for %q", name.Local, r.Href)
	return ""
}

func statusCode(status string) int {
	fields := strings.Fields(status)
	if len(fields) < 2 {
		return 0
	}
	code, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0
	}
	return code
}

func TestPropFindDescribesAnAccountAsAPrincipalAndACalendarHome(t *testing.T) {
	h := handlerFor(t, newStore(t), caldav.Config{})

	ms := propfind(t, h, "/alice/", "0", askFor(
		davName("resourcetype"), davName("current-user-principal"),
		davName("principal-URL"), caldavName("calendar-home-set")))

	if len(ms.Responses) != 1 {
		t.Fatalf("depth 0 returned %d responses: %v", len(ms.Responses), ms.hrefs())
	}
	resp := ms.at(t, "/alice/")

	for _, want := range []string{"collection", "principal"} {
		if got := resp.value(t, davName("resourcetype")); !strings.Contains(got, want) {
			t.Errorf("resourcetype = %q, missing %s", got, want)
		}
	}
	// Discovery walks current-user-principal to calendar-home-set and then
	// enumerates it, so a client that follows the chain has to arrive back here.
	for _, name := range []xml.Name{davName("current-user-principal"), davName("principal-URL"), caldavName("calendar-home-set")} {
		if got := resp.value(t, name); !strings.Contains(got, "/alice/") {
			t.Errorf("%s = %q, want a link to /alice/", name.Local, got)
		}
	}
}

func TestPropFindListsAnAccountsCalendars(t *testing.T) {
	h := handlerFor(t, newStore(t), caldav.Config{})

	ms := propfind(t, h, "/alice/", "1", askFor(davName("displayname"), davName("resourcetype")))

	if !slices.Contains(ms.hrefs(), "/alice/work/") {
		t.Fatalf("hrefs = %v, missing the calendar", ms.hrefs())
	}
	cal := ms.at(t, "/alice/work/")
	if got := cal.value(t, davName("resourcetype")); !strings.Contains(got, "calendar") {
		t.Errorf("resourcetype = %q, want a calendar", got)
	}
	if got := cal.value(t, davName("displayname")); got != "Work" {
		t.Errorf("displayname = %q, want %q", got, "Work")
	}
}

// hiddenCalendar denies every calendar named "private", whatever the account
// listing says is there. The two are separate grants, and a listing that
// ignored the second would hand a sharee the URLs of everything else in the
// account.
type hiddenCalendar struct{ *caldavmem.Store }

func (h hiddenCalendar) CalendarPermissions(ctx context.Context, actor caldav.Actor, ref caldav.CalendarRef) (caldav.CalendarPermissions, error) {
	if ref.Calendar.String() == "private" {
		return caldav.CalendarPermissions{}, nil
	}
	return h.Store.CalendarPermissions(ctx, actor, ref)
}

func TestPropFindOmitsCalendarsTheActorMayNotSee(t *testing.T) {
	store := newStore(t)
	req := caldav.CreateCalendarRequest{Name: caldav.MustSegment("private"), DisplayName: "Private"}
	if _, err := store.CompareAndCreateCalendar(context.Background(), "alice", req, caldav.Unconditional()); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	h := handlerFor(t, hiddenCalendar{store}, caldav.Config{})

	ms := propfind(t, h, "/alice/", "1", askFor(davName("displayname")))

	if slices.Contains(ms.hrefs(), "/alice/private/") {
		t.Errorf("hrefs = %v, includes a calendar the actor may not see", ms.hrefs())
	}
	if !slices.Contains(ms.hrefs(), "/alice/work/") {
		t.Errorf("hrefs = %v, dropped a calendar the actor may see", ms.hrefs())
	}
}

func TestPropFindDescribesACalendar(t *testing.T) {
	h := handlerFor(t, newStore(t), caldav.Config{})

	ms := propfind(t, h, "/alice/work/", "0", askFor(
		davName("resourcetype"), davName("displayname"), davName("owner"),
		davName("current-user-privilege-set"),
		caldavName("supported-calendar-component-set"), caldavName("supported-calendar-data")))
	resp := ms.at(t, "/alice/work/")

	if got := resp.value(t, davName("owner")); !strings.Contains(got, "/alice/") {
		t.Errorf("owner = %q, want the owning account", got)
	}
	// A calendar with no restriction accepts every kind, and a client reads this
	// to decide which components it may write here.
	comps := resp.value(t, caldavName("supported-calendar-component-set"))
	for _, want := range []string{"VEVENT", "VTODO", "VJOURNAL", "VFREEBUSY"} {
		if !strings.Contains(comps, want) {
			t.Errorf("supported-calendar-component-set = %q, missing %s", comps, want)
		}
	}
	if got := resp.value(t, caldavName("supported-calendar-data")); !strings.Contains(got, "text/calendar") {
		t.Errorf("supported-calendar-data = %q, want text/calendar", got)
	}
}

func TestPropFindListsTheItemsInACalendar(t *testing.T) {
	store := newStore(t)
	seedItem(t, store, "alice", "standup.ics")
	h := handlerFor(t, store, caldav.Config{})

	ms := propfind(t, h, "/alice/work/", "1", askFor(davName("getetag"), davName("getcontenttype")))

	if !slices.Contains(ms.hrefs(), "/alice/work/standup.ics") {
		t.Fatalf("hrefs = %v, missing the item", ms.hrefs())
	}
	item := ms.at(t, "/alice/work/standup.ics")
	if got := item.value(t, davName("getetag")); got == "" {
		t.Error("no getetag, so a client cannot tell whether it already has this")
	}
	if got := item.value(t, davName("getcontenttype")); !strings.Contains(got, "text/calendar") {
		t.Errorf("getcontenttype = %q, want text/calendar", got)
	}
}

func TestPropFindServesAnItemsCalendarData(t *testing.T) {
	store := newStore(t)
	seedItem(t, store, "alice", "standup.ics")
	h := handlerFor(t, store, caldav.Config{})

	ms := propfind(t, h, "/alice/work/standup.ics", "0", askFor(caldavName("calendar-data")))
	got := ms.at(t, "/alice/work/standup.ics").value(t, caldavName("calendar-data"))

	if !strings.Contains(got, "BEGIN:VEVENT") {
		t.Errorf("calendar-data = %q, want the stored object", got)
	}
}

func TestAllPropWithholdsWholesaleProperties(t *testing.T) {
	store := newStore(t)
	seedItem(t, store, "alice", "standup.ics")
	h := handlerFor(t, store, caldav.Config{})

	// RFC 4791 §9.6 and RFC 6578 §4: allprop asks for a resource's metadata, and
	// answering it with the resource's whole content, or with a sync position
	// that a listing cannot honour, is what these exclusions prevent.
	cal := propfind(t, h, "/alice/work/", "0", allProp).at(t, "/alice/work/")
	for _, name := range []xml.Name{davName("sync-token"), davName("supported-privilege-set"), davName("current-user-privilege-set")} {
		if _, reported := cal.found(name); reported {
			t.Errorf("allprop on a calendar reported %s", name.Local)
		}
	}
	if _, reported := cal.found(davName("displayname")); !reported {
		t.Error("allprop on a calendar dropped displayname")
	}

	item := propfind(t, h, "/alice/work/standup.ics", "0", allProp).at(t, "/alice/work/standup.ics")
	if _, reported := item.found(caldavName("calendar-data")); reported {
		t.Error("allprop on an item reported calendar-data")
	}
	if _, reported := item.found(davName("getetag")); !reported {
		t.Error("allprop on an item dropped getetag")
	}
}

func TestPropFindReportsAnUnknownPropertyAsNotFound(t *testing.T) {
	h := handlerFor(t, newStore(t), caldav.Config{})

	ms := propfind(t, h, "/alice/work/", "0", askFor(xml.Name{Space: "http://example.invalid/", Local: "invented"}))
	resp := ms.at(t, "/alice/work/")

	code, reported := resp.found(xml.Name{Space: "http://example.invalid/", Local: "invented"})
	if !reported {
		t.Fatal("an unrequestable property was dropped rather than reported 404")
	}
	if code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", code, http.StatusNotFound)
	}
}

func TestPropFindRefusesInfiniteDepth(t *testing.T) {
	h := handlerFor(t, newStore(t), caldav.Config{})

	r := httptest.NewRequest("PROPFIND", "/alice/", strings.NewReader(allProp))
	r.Header.Set("Content-Type", "application/xml")
	r.Header.Set("Depth", "infinity")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
	if !strings.Contains(w.Body.String(), "propfind-finite-depth") {
		t.Errorf("body = %q, want the DAV:propfind-finite-depth precondition", w.Body.String())
	}
}

func TestPropFindOnAMissingItemIsNotFound(t *testing.T) {
	h := handlerFor(t, newStore(t), caldav.Config{})

	r := httptest.NewRequest("PROPFIND", "/alice/work/gone.ics", strings.NewReader(allProp))
	r.Header.Set("Content-Type", "application/xml")
	r.Header.Set("Depth", "0")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

// readOnlyBackend implements caldav.Backend and nothing else, by forwarding
// only that interface. Embedding the store would carry every optional
// capability with it, which is the opposite of what this fixture is for.
type readOnlyBackend struct{ store *caldavmem.Store }

func (b readOnlyBackend) CalendarPermissions(ctx context.Context, actor caldav.Actor, ref caldav.CalendarRef) (caldav.CalendarPermissions, error) {
	return b.store.CalendarPermissions(ctx, actor, ref)
}

func (b readOnlyBackend) AccountPermissions(ctx context.Context, actor caldav.Actor, account caldav.AccountID) (caldav.AccountPermissions, error) {
	return b.store.AccountPermissions(ctx, actor, account)
}

func (b readOnlyBackend) ListCalendars(ctx context.Context, account caldav.AccountID) ([]caldav.Calendar, error) {
	return b.store.ListCalendars(ctx, account)
}

func (b readOnlyBackend) GetCalendar(ctx context.Context, ref caldav.CalendarRef) (caldav.Calendar, error) {
	return b.store.GetCalendar(ctx, ref)
}

func (b readOnlyBackend) GetItem(ctx context.Context, ref caldav.ItemRef) (caldav.Item, error) {
	return b.store.GetItem(ctx, ref)
}

func (b readOnlyBackend) ListItems(ctx context.Context, ref caldav.CalendarRef, yield func(caldav.Item) bool) (caldav.Revision, error) {
	return b.store.ListItems(ctx, ref, yield)
}

func TestSyncPropertiesFollowTheBackendsCapability(t *testing.T) {
	store := newStore(t)
	ask := askFor(davName("sync-token"), xml.Name{Space: ctagNS, Local: "getctag"})

	syncing := handlerFor(t, store, caldav.Config{})
	resp := propfind(t, syncing, "/alice/work/", "0", ask).at(t, "/alice/work/")
	for _, name := range []xml.Name{davName("sync-token"), {Space: ctagNS, Local: "getctag"}} {
		if code, _ := resp.found(name); code != http.StatusOK {
			t.Errorf("%s came back %d on a syncing backend, want 200", name.Local, code)
		}
	}

	// Without a change log there is no delta to lead a client to, so a token
	// would be a promise the backend cannot keep.
	plain := handlerFor(t, readOnlyBackend{store}, caldav.Config{})
	resp = propfind(t, plain, "/alice/work/", "0", ask).at(t, "/alice/work/")
	for _, name := range []xml.Name{davName("sync-token"), {Space: ctagNS, Local: "getctag"}} {
		if code, _ := resp.found(name); code != http.StatusNotFound {
			t.Errorf("%s came back %d on a backend that cannot sync, want %d", name.Local, code, http.StatusNotFound)
		}
	}
}

func TestPropFindRefusesAnActorWhoMayOnlySeeBusyTimes(t *testing.T) {
	store := newStore(t)
	seedItem(t, store, "alice", "standup.ics")
	h := handlerFor(t, availabilityOnly{store}, caldav.Config{})

	for _, target := range []string{"/alice/work/standup.ics", "/alice/work/"} {
		t.Run(target, func(t *testing.T) {
			depth := "0"
			if strings.HasSuffix(target, "/") {
				depth = "1"
			}
			r := httptest.NewRequest("PROPFIND", target, strings.NewReader(allProp))
			r.Header.Set("Content-Type", "application/xml")
			r.Header.Set("Depth", depth)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)

			if w.Code == http.StatusMultiStatus && strings.Contains(w.Body.String(), "standup.ics") {
				t.Error("an item was named to an actor allowed to see only busy times")
			}
		})
	}
}

// privilegeNames decodes a current-user-privilege-set into the exact element
// names it granted. Substring matching cannot do this job: "read" is a prefix
// of read-acl and of read-free-busy, and telling a free-busy share from a full
// read is the entire point of the property.
func privilegeNames(t *testing.T, inner string) []xml.Name {
	t.Helper()

	var set struct {
		Privileges []struct {
			Granted []struct{ XMLName xml.Name } `xml:",any"`
		} `xml:"DAV: privilege"`
	}
	if err := xml.Unmarshal([]byte("<wrapper>"+inner+"</wrapper>"), &set); err != nil {
		t.Fatalf("decoding privileges %q: %v", inner, err)
	}
	var names []xml.Name
	for _, priv := range set.Privileges {
		for _, granted := range priv.Granted {
			names = append(names, granted.XMLName)
		}
	}
	return names
}

func assertPrivileges(t *testing.T, h *caldav.Handler, target string, want, unwanted []xml.Name) {
	t.Helper()

	inner := propfind(t, h, target, "0", askFor(davName("current-user-privilege-set"))).
		at(t, target).value(t, davName("current-user-privilege-set"))
	granted := privilegeNames(t, inner)

	for _, name := range want {
		if !slices.Contains(granted, name) {
			t.Errorf("%s: privileges = %v, missing %s", target, granted, name.Local)
		}
	}
	for _, name := range unwanted {
		if slices.Contains(granted, name) {
			t.Errorf("%s: privileges = %v, includes %s", target, granted, name.Local)
		}
	}
}

func TestPropFindDistinguishesAFreeBusyShareFromAFullRead(t *testing.T) {
	store := newStore(t)
	h := handlerFor(t, availabilityOnly{store}, caldav.Config{})

	// The actor may learn when the calendar is busy and nothing else. DAV:read
	// would say it may fetch the items, which GET refuses it.
	assertPrivileges(t, h, "/alice/work/",
		[]xml.Name{caldavName("read-free-busy")},
		[]xml.Name{davName("read"), davName("write"), davName("bind")})
}

func TestPropFindGrantsAnOwnerTheAggregateWrite(t *testing.T) {
	h := handlerFor(t, newStore(t), caldav.Config{})

	assertPrivileges(t, h, "/alice/work/",
		[]xml.Name{davName("read"), caldavName("read-free-busy"), davName("write"),
			davName("write-content"), davName("write-properties"), davName("bind"), davName("unbind")},
		nil)
}

func TestPropFindWithholdsWriteFromAViewOnlySharee(t *testing.T) {
	store := newStore(t)
	if err := store.Share(caldav.CalendarRef{Account: "carol", Calendar: caldav.MustSegment("work")}, "alice"); err != nil {
		t.Fatalf("sharing: %v", err)
	}
	h := handlerFor(t, store, caldav.Config{})

	assertPrivileges(t, h, "/carol/work/",
		[]xml.Name{davName("read"), caldavName("read-free-busy")},
		[]xml.Name{davName("write"), davName("write-content"), davName("bind"), davName("unbind")})
}

func TestPropFindWithholdsWriteFromABackendThatCannotWrite(t *testing.T) {
	// The actor owns the calendar, so the permissions say it may write. The
	// backend cannot, and advertising DAV:bind would promise a PUT that 405s.
	h := handlerFor(t, readOnlyBackend{newStore(t)}, caldav.Config{})

	assertPrivileges(t, h, "/alice/work/",
		[]xml.Name{davName("read")},
		[]xml.Name{davName("write"), davName("write-content"), davName("bind"), davName("unbind"), davName("read-acl")})
}

// editorOnly may change the items but not the calendar's own settings. The
// aggregate DAV:write covers both, so this is the grant that tells whether the
// aggregate is computed or merely echoed.
type editorOnly struct{ *caldavmem.Store }

func (editorOnly) CalendarPermissions(context.Context, caldav.Actor, caldav.CalendarRef) (caldav.CalendarPermissions, error) {
	return caldav.EditPermissions(), nil
}

func TestPropFindWithholdsTheAggregateWriteFromAPartialGrant(t *testing.T) {
	h := handlerFor(t, editorOnly{newStore(t)}, caldav.Config{})

	assertPrivileges(t, h, "/alice/work/",
		[]xml.Name{davName("write-content"), davName("bind"), davName("unbind")},
		[]xml.Name{davName("write"), davName("write-properties")})
}

func TestGetETagPropertyMatchesTheETagHeader(t *testing.T) {
	store := newStore(t)
	seedRaw(t, store, "alice", "august.ics", augustICS, "august")
	h := handlerFor(t, store, caldav.Config{})

	// Clients read getetag from listings and echo it into If-Match headers, so
	// the property and the header must carry the same entity tag. A quoting
	// mismatch here silently breaks every conditional write those clients send.
	header := do(h, http.MethodGet, "/alice/work/august.ics").Header().Get("ETag")

	listed := textOf(t, propfind(t, h, "/alice/work/august.ics", "0", askFor(davName("getetag"))).
		at(t, "/alice/work/august.ics").value(t, davName("getetag")))
	if listed != header {
		t.Errorf("PROPFIND getetag = %q, ETag header = %q", listed, header)
	}

	reported := textOf(t, reportMS(t, h, "/alice/work/", multigetBody("/alice/work/august.ics")).
		at(t, "/alice/work/august.ics").value(t, davName("getetag")))
	if reported != header {
		t.Errorf("REPORT getetag = %q, ETag header = %q", reported, header)
	}
}

// textOf decodes a property's inner XML to the text a client would read, so
// character references do not fake a mismatch (or hide one) in comparisons.
func textOf(t *testing.T, inner string) string {
	t.Helper()

	var v struct {
		Text string `xml:",chardata"`
	}
	if err := xml.Unmarshal([]byte("<x>"+inner+"</x>"), &v); err != nil {
		t.Fatalf("decoding property text %q: %v", inner, err)
	}
	return v.Text
}

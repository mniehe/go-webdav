package carddav_test

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/mniehe/davkit/carddav"
	"github.com/mniehe/davkit/carddavmem"
)

const (
	davNS     = "DAV:"
	carddavNS = "urn:ietf:params:xml:ns:carddav"
	ctagNS    = "http://calendarserver.org/ns/"
)

func davName(local string) xml.Name     { return xml.Name{Space: davNS, Local: local} }
func carddavName(local string) xml.Name { return xml.Name{Space: carddavNS, Local: local} }

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

func propfind(t *testing.T, h *carddav.Handler, target, depth, body string) multistatus {
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

func TestPropFindDescribesAnAccountAsAPrincipalAndAAddressBookHome(t *testing.T) {
	h := handlerFor(t, newStore(t), carddav.Config{})

	ms := propfind(t, h, "/alice/", "0", askFor(
		davName("resourcetype"), davName("current-user-principal"),
		davName("principal-URL"), carddavName("addressbook-home-set")))

	if len(ms.Responses) != 1 {
		t.Fatalf("depth 0 returned %d responses: %v", len(ms.Responses), ms.hrefs())
	}
	resp := ms.at(t, "/alice/")

	for _, want := range []string{"collection", "principal"} {
		if got := resp.value(t, davName("resourcetype")); !strings.Contains(got, want) {
			t.Errorf("resourcetype = %q, missing %s", got, want)
		}
	}
	// Discovery walks current-user-principal to addressbook-home-set and then
	// enumerates it, so a client that follows the chain has to arrive back here.
	for _, name := range []xml.Name{davName("current-user-principal"), davName("principal-URL"), carddavName("addressbook-home-set")} {
		if got := resp.value(t, name); !strings.Contains(got, "/alice/") {
			t.Errorf("%s = %q, want a link to /alice/", name.Local, got)
		}
	}
}

func TestPropFindListsAnAccountsCalendars(t *testing.T) {
	h := handlerFor(t, newStore(t), carddav.Config{})

	ms := propfind(t, h, "/alice/", "1", askFor(davName("displayname"), davName("resourcetype")))

	if !slices.Contains(ms.hrefs(), "/alice/work/") {
		t.Fatalf("hrefs = %v, missing the calendar", ms.hrefs())
	}
	cal := ms.at(t, "/alice/work/")
	if got := cal.value(t, davName("resourcetype")); !strings.Contains(got, "addressbook") {
		t.Errorf("resourcetype = %q, want an addressbook", got)
	}
	if got := cal.value(t, davName("displayname")); got != "Work" {
		t.Errorf("displayname = %q, want %q", got, "Work")
	}
}

// hiddenCalendar denies every calendar named "private", whatever the account
// listing says is there. The two are separate grants, and a listing that
// ignored the second would hand a sharee the URLs of everything else in the
// account.
type hiddenCalendar struct{ *carddavmem.Store }

func (h hiddenCalendar) AddressBookPermissions(ctx context.Context, actor carddav.Actor, ref carddav.AddressBookRef) (carddav.AddressBookPermissions, error) {
	if ref.Book.String() == "private" {
		return carddav.AddressBookPermissions{}, nil
	}
	return h.Store.AddressBookPermissions(ctx, actor, ref)
}

func TestPropFindOmitsCalendarsTheActorMayNotSee(t *testing.T) {
	store := newStore(t)
	req := carddav.CreateAddressBookRequest{Name: carddav.MustSegment("private"), DisplayName: "Private"}
	if _, err := store.CompareAndCreateAddressBook(context.Background(), "alice", req, carddav.Unconditional()); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	h := handlerFor(t, hiddenCalendar{store}, carddav.Config{})

	ms := propfind(t, h, "/alice/", "1", askFor(davName("displayname")))

	if slices.Contains(ms.hrefs(), "/alice/private/") {
		t.Errorf("hrefs = %v, includes a calendar the actor may not see", ms.hrefs())
	}
	if !slices.Contains(ms.hrefs(), "/alice/work/") {
		t.Errorf("hrefs = %v, dropped a calendar the actor may see", ms.hrefs())
	}
}

func TestPropFindDescribesACalendar(t *testing.T) {
	h := handlerFor(t, newStore(t), carddav.Config{})

	ms := propfind(t, h, "/alice/work/", "0", askFor(
		davName("resourcetype"), davName("displayname"), davName("owner"),
		davName("current-user-privilege-set"),
		carddavName("supported-address-data")))
	resp := ms.at(t, "/alice/work/")

	if got := resp.value(t, davName("owner")); !strings.Contains(got, "/alice/") {
		t.Errorf("owner = %q, want the owning account", got)
	}
	// RFC 6352 §6.2.2: a client reads this to pick the vCard version it writes.
	data := resp.value(t, carddavName("supported-address-data"))
	for _, want := range []string{"text/vcard", "3.0", "4.0"} {
		if !strings.Contains(data, want) {
			t.Errorf("supported-address-data = %q, missing %s", data, want)
		}
	}
}

func TestPropFindListsTheItemsInACalendar(t *testing.T) {
	store := newStore(t)
	seedItem(t, store, "alice", "standup.vcf")
	h := handlerFor(t, store, carddav.Config{})

	ms := propfind(t, h, "/alice/work/", "1", askFor(davName("getetag"), davName("getcontenttype")))

	if !slices.Contains(ms.hrefs(), "/alice/work/standup.vcf") {
		t.Fatalf("hrefs = %v, missing the item", ms.hrefs())
	}
	item := ms.at(t, "/alice/work/standup.vcf")
	if got := item.value(t, davName("getetag")); got == "" {
		t.Error("no getetag, so a client cannot tell whether it already has this")
	}
	if got := item.value(t, davName("getcontenttype")); !strings.Contains(got, "text/vcard") {
		t.Errorf("getcontenttype = %q, want text/vcard", got)
	}
}

func TestPropFindServesAnItemsCalendarData(t *testing.T) {
	store := newStore(t)
	seedItem(t, store, "alice", "standup.vcf")
	h := handlerFor(t, store, carddav.Config{})

	ms := propfind(t, h, "/alice/work/standup.vcf", "0", askFor(carddavName("address-data")))
	got := ms.at(t, "/alice/work/standup.vcf").value(t, carddavName("address-data"))

	if !strings.Contains(got, "FN:Stan Dupp") {
		t.Errorf("address-data = %q, want the stored object", got)
	}
}

func TestAllPropWithholdsWholesaleProperties(t *testing.T) {
	store := newStore(t)
	seedItem(t, store, "alice", "standup.vcf")
	h := handlerFor(t, store, carddav.Config{})

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

	item := propfind(t, h, "/alice/work/standup.vcf", "0", allProp).at(t, "/alice/work/standup.vcf")
	if _, reported := item.found(carddavName("address-data")); reported {
		t.Error("allprop on an item reported address-data")
	}
	if _, reported := item.found(davName("getetag")); !reported {
		t.Error("allprop on an item dropped getetag")
	}
}

func TestPropFindReportsAnUnknownPropertyAsNotFound(t *testing.T) {
	h := handlerFor(t, newStore(t), carddav.Config{})

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
	h := handlerFor(t, newStore(t), carddav.Config{})

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
	h := handlerFor(t, newStore(t), carddav.Config{})

	r := httptest.NewRequest("PROPFIND", "/alice/work/gone.ics", strings.NewReader(allProp))
	r.Header.Set("Content-Type", "application/xml")
	r.Header.Set("Depth", "0")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

// readOnlyBackend implements carddav.Backend and nothing else, by forwarding
// only that interface. Embedding the store would carry every optional
// capability with it, which is the opposite of what this fixture is for.
type readOnlyBackend struct{ store *carddavmem.Store }

func (b readOnlyBackend) AddressBookPermissions(ctx context.Context, actor carddav.Actor, ref carddav.AddressBookRef) (carddav.AddressBookPermissions, error) {
	return b.store.AddressBookPermissions(ctx, actor, ref)
}

func (b readOnlyBackend) AccountPermissions(ctx context.Context, actor carddav.Actor, account carddav.AccountID) (carddav.AccountPermissions, error) {
	return b.store.AccountPermissions(ctx, actor, account)
}

func (b readOnlyBackend) ListAddressBooks(ctx context.Context, account carddav.AccountID) ([]carddav.AddressBook, error) {
	return b.store.ListAddressBooks(ctx, account)
}

func (b readOnlyBackend) GetAddressBook(ctx context.Context, ref carddav.AddressBookRef) (carddav.AddressBook, error) {
	return b.store.GetAddressBook(ctx, ref)
}

func (b readOnlyBackend) GetItem(ctx context.Context, ref carddav.ItemRef) (carddav.Item, error) {
	return b.store.GetItem(ctx, ref)
}

func (b readOnlyBackend) ListItems(ctx context.Context, ref carddav.AddressBookRef, yield func(carddav.Item) bool) (carddav.Revision, error) {
	return b.store.ListItems(ctx, ref, yield)
}

func TestSyncPropertiesFollowTheBackendsCapability(t *testing.T) {
	store := newStore(t)
	ask := askFor(davName("sync-token"), xml.Name{Space: ctagNS, Local: "getctag"})

	syncing := handlerFor(t, store, carddav.Config{})
	resp := propfind(t, syncing, "/alice/work/", "0", ask).at(t, "/alice/work/")
	for _, name := range []xml.Name{davName("sync-token"), {Space: ctagNS, Local: "getctag"}} {
		if code, _ := resp.found(name); code != http.StatusOK {
			t.Errorf("%s came back %d on a syncing backend, want 200", name.Local, code)
		}
	}

	// Without a change log there is no delta to lead a client to, so a token
	// would be a promise the backend cannot keep.
	plain := handlerFor(t, readOnlyBackend{store}, carddav.Config{})
	resp = propfind(t, plain, "/alice/work/", "0", ask).at(t, "/alice/work/")
	for _, name := range []xml.Name{davName("sync-token"), {Space: ctagNS, Local: "getctag"}} {
		if code, _ := resp.found(name); code != http.StatusNotFound {
			t.Errorf("%s came back %d on a backend that cannot sync, want %d", name.Local, code, http.StatusNotFound)
		}
	}
}

// privilegeNames decodes a current-user-privilege-set into the exact element
// names it granted. Substring matching cannot do this job: "read" is a prefix
// of read-acl, so an exact decode is the only honest comparison.
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

func assertPrivileges(t *testing.T, h *carddav.Handler, target string, want, unwanted []xml.Name) {
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

func TestPropFindGrantsAnOwnerTheAggregateWrite(t *testing.T) {
	h := handlerFor(t, newStore(t), carddav.Config{})

	assertPrivileges(t, h, "/alice/work/",
		[]xml.Name{davName("read"), davName("write"),
			davName("write-content"), davName("write-properties"), davName("bind"), davName("unbind")},
		nil)
}

func TestPropFindWithholdsWriteFromAViewOnlySharee(t *testing.T) {
	store := newStore(t)
	if err := store.Share(carddav.AddressBookRef{Account: "carol", Book: carddav.MustSegment("work")}, "alice"); err != nil {
		t.Fatalf("sharing: %v", err)
	}
	h := handlerFor(t, store, carddav.Config{})

	assertPrivileges(t, h, "/carol/work/",
		[]xml.Name{davName("read")},
		[]xml.Name{davName("write"), davName("write-content"), davName("bind"), davName("unbind")})
}

func TestPropFindWithholdsWriteFromABackendThatCannotWrite(t *testing.T) {
	// The actor owns the calendar, so the permissions say it may write. The
	// backend cannot, and advertising DAV:bind would promise a PUT that 405s.
	h := handlerFor(t, readOnlyBackend{newStore(t)}, carddav.Config{})

	assertPrivileges(t, h, "/alice/work/",
		[]xml.Name{davName("read")},
		[]xml.Name{davName("write"), davName("write-content"), davName("bind"), davName("unbind"), davName("read-acl")})
}

// editorOnly may change the items but not the calendar's own settings. The
// aggregate DAV:write covers both, so this is the grant that tells whether the
// aggregate is computed or merely echoed.
type editorOnly struct{ *carddavmem.Store }

func (editorOnly) AddressBookPermissions(context.Context, carddav.Actor, carddav.AddressBookRef) (carddav.AddressBookPermissions, error) {
	return carddav.EditPermissions(), nil
}

func TestPropFindWithholdsTheAggregateWriteFromAPartialGrant(t *testing.T) {
	h := handlerFor(t, editorOnly{newStore(t)}, carddav.Config{})

	assertPrivileges(t, h, "/alice/work/",
		[]xml.Name{davName("write-content"), davName("bind"), davName("unbind")},
		[]xml.Name{davName("write"), davName("write-properties")})
}

func TestGetETagPropertyMatchesTheETagHeader(t *testing.T) {
	store := newStore(t)
	seedItem(t, store, "alice", "standup.vcf")
	h := handlerFor(t, store, carddav.Config{})

	// Clients read getetag from listings and echo it into If-Match headers, so
	// the property and the header must carry the same entity tag. A quoting
	// mismatch here silently breaks every conditional write those clients send.
	header := do(h, http.MethodGet, "/alice/work/standup.vcf").Header().Get("ETag")

	listed := textOf(t, propfind(t, h, "/alice/work/standup.vcf", "0", askFor(davName("getetag"))).
		at(t, "/alice/work/standup.vcf").value(t, davName("getetag")))
	if listed != header {
		t.Errorf("PROPFIND getetag = %q, ETag header = %q", listed, header)
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

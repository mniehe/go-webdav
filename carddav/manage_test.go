package carddav_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mniehe/davkit/carddav"
	"github.com/mniehe/davkit/carddavmem"
)

func mkcol(h *carddav.Handler, target, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("MKCOL", target, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/xml")
	} else {
		r.Body = http.NoBody
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func proppatch(h *carddav.Handler, target, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("PROPPATCH", target, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/xml")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestMkcolCreatesAConfiguredAddressBook(t *testing.T) {
	h := handlerFor(t, newStore(t), carddav.Config{})

	// RFC 5689: extended MKCOL with the addressbook resourcetype.
	body := `<?xml version="1.0"?>
<D:mkcol xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav">
  <D:set><D:prop>
    <D:resourcetype><D:collection/><C:addressbook/></D:resourcetype>
    <D:displayname>Team contacts</D:displayname>
    <C:addressbook-description>Shared team address book</C:addressbook-description>
  </D:prop></D:set>
</D:mkcol>`

	w := mkcol(h, "/alice/team/", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d\n%s", w.Code, http.StatusCreated, w.Body.String())
	}

	resp := propfind(t, h, "/alice/team/", "0", askFor(
		davName("displayname"), carddavName("addressbook-description"))).
		at(t, "/alice/team/")
	if got := resp.value(t, davName("displayname")); got != "Team contacts" {
		t.Errorf("displayname = %q", got)
	}
	if got := resp.value(t, carddavName("addressbook-description")); got != "Shared team address book" {
		t.Errorf("addressbook-description = %q", got)
	}
}

func TestMkcolWithoutABodyCreatesADefaultAddressBook(t *testing.T) {
	h := handlerFor(t, newStore(t), carddav.Config{})

	if w := mkcol(h, "/alice/plain/", ""); w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d\n%s", w.Code, http.StatusCreated, w.Body.String())
	}
	if w := put(h, "/alice/plain/review.vcf", reviewVCF, nil); w.Code != http.StatusCreated {
		t.Errorf("PUT into the new address book: status = %d, want %d\n%s", w.Code, http.StatusCreated, w.Body.String())
	}
}

func TestMkcolRequiresTheAddressbookResourcetype(t *testing.T) {
	h := handlerFor(t, newStore(t), carddav.Config{})

	// A body naming a resourcetype without CARDDAV:addressbook asks for a
	// collection this handler does not make.
	body := `<?xml version="1.0"?>
<D:mkcol xmlns:D="DAV:">
  <D:set><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop></D:set>
</D:mkcol>`

	if w := mkcol(h, "/alice/folder/", body); w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d\n%s", w.Code, http.StatusBadRequest, w.Body.String())
	}
}

func TestMkcolRefusesAPropertyItCannotSet(t *testing.T) {
	h := handlerFor(t, newStore(t), carddav.Config{})

	// A property that cannot be set means the address book is not created at
	// all — a partial create would leave a collection the client did not ask
	// for.
	body := `<?xml version="1.0"?>
<D:mkcol xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav" xmlns:X="http://example.invalid/">
  <D:set><D:prop>
    <D:resourcetype><D:collection/><C:addressbook/></D:resourcetype>
    <D:displayname>Half-made</D:displayname>
    <X:invented>x</X:invented>
  </D:prop></D:set>
</D:mkcol>`

	if w := mkcol(h, "/alice/team/", body); w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d\n%s", w.Code, http.StatusForbidden, w.Body.String())
	}
	if w := do(h, http.MethodGet, "/alice/team/review.vcf"); w.Code == http.StatusOK {
		t.Error("fixture error")
	}
	if w := propfind(t, h, "/alice/", "1", askFor(davName("displayname"))); len(w.hrefs()) != 2 {
		t.Errorf("account lists %v, want only the account and its seeded book", w.hrefs())
	}
}

func TestMkcolOverAnExistingAddressBookIsRefused(t *testing.T) {
	h := handlerFor(t, newStore(t), carddav.Config{})

	// RFC 4918 §9.3: the request URI must be unmapped, reported with the
	// DAV:resource-must-be-null precondition rather than a silent 201 that
	// reads as a successful create.
	w := mkcol(h, "/alice/work/", "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
	if !strings.Contains(w.Body.String(), "resource-must-be-null") {
		t.Errorf("body = %q, want DAV:resource-must-be-null", w.Body.String())
	}
}

func TestMkcolTargetsOnlyAnAddressBookPath(t *testing.T) {
	h := handlerFor(t, newStore(t), carddav.Config{})

	for _, target := range []string{"/alice/", "/alice/work/deep.vcf"} {
		if w := mkcol(h, target, ""); w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: status = %d, want %d", target, w.Code, http.StatusMethodNotAllowed)
		}
	}
}

// listOnly may enumerate the account's address books but not add to them.
type listOnly struct{ *carddavmem.Store }

func (listOnly) AccountPermissions(context.Context, carddav.Actor, carddav.AccountID) (carddav.AccountPermissions, error) {
	return carddav.AccountPermissions{ListBooks: true}, nil
}

func TestMkcolRequiresTheCreatePermission(t *testing.T) {
	h := handlerFor(t, listOnly{newStore(t)}, carddav.Config{})

	if w := mkcol(h, "/alice/new/", ""); w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestMkcolConcealsAForeignAccount(t *testing.T) {
	h := handlerFor(t, newStore(t), carddav.Config{})

	if w := mkcol(h, "/carol/new/", ""); w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestMkcolNeedsACreatingBackend(t *testing.T) {
	h := handlerFor(t, readOnlyBackend{newStore(t)}, carddav.Config{})

	if w := mkcol(h, "/alice/new/", ""); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestProppatchUpdatesAddressBookSettings(t *testing.T) {
	h := handlerFor(t, newStore(t), carddav.Config{})

	body := `<?xml version="1.0"?>
<D:propertyupdate xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav">
  <D:set><D:prop>
    <D:displayname>Renamed</D:displayname>
    <C:addressbook-description>Fresh description</C:addressbook-description>
  </D:prop></D:set>
</D:propertyupdate>`

	w := proppatch(h, "/alice/work/", body)
	if w.Code != http.StatusMultiStatus {
		t.Fatalf("status = %d, want %d\n%s", w.Code, http.StatusMultiStatus, w.Body.String())
	}

	resp := propfind(t, h, "/alice/work/", "0", askFor(
		davName("displayname"), carddavName("addressbook-description"))).
		at(t, "/alice/work/")
	if got := resp.value(t, davName("displayname")); got != "Renamed" {
		t.Errorf("displayname = %q", got)
	}
	if got := resp.value(t, carddavName("addressbook-description")); got != "Fresh description" {
		t.Errorf("addressbook-description = %q", got)
	}
}

func TestProppatchRemoveClearsAProperty(t *testing.T) {
	store := newStore(t)
	h := handlerFor(t, store, carddav.Config{})

	set := `<?xml version="1.0"?>
<D:propertyupdate xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav">
  <D:set><D:prop><C:addressbook-description>doomed</C:addressbook-description></D:prop></D:set>
</D:propertyupdate>`
	if w := proppatch(h, "/alice/work/", set); w.Code != http.StatusMultiStatus {
		t.Fatalf("setting up: %d\n%s", w.Code, w.Body.String())
	}

	remove := `<?xml version="1.0"?>
<D:propertyupdate xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav">
  <D:remove><D:prop><C:addressbook-description/></D:prop></D:remove>
</D:propertyupdate>`
	if w := proppatch(h, "/alice/work/", remove); w.Code != http.StatusMultiStatus {
		t.Fatalf("status = %d, want %d\n%s", w.Code, http.StatusMultiStatus, w.Body.String())
	}

	resp := propfind(t, h, "/alice/work/", "0", askFor(carddavName("addressbook-description"))).at(t, "/alice/work/")
	if code, reported := resp.found(carddavName("addressbook-description")); reported && code == http.StatusOK {
		t.Error("addressbook-description survived its removal")
	}
}

func TestProppatchIsAtomic(t *testing.T) {
	h := handlerFor(t, newStore(t), carddav.Config{})

	// RFC 4918 §9.2: instructions apply together or not at all. The dead
	// property is refused 403, and the displayname — valid on its own — must
	// report 424 and stay unapplied.
	body := `<?xml version="1.0"?>
<D:propertyupdate xmlns:D="DAV:" xmlns:X="http://example.invalid/">
  <D:set><D:prop>
    <D:displayname>Half-applied</D:displayname>
    <X:invented>x</X:invented>
  </D:prop></D:set>
</D:propertyupdate>`

	w := proppatch(h, "/alice/work/", body)
	if w.Code != http.StatusMultiStatus {
		t.Fatalf("status = %d, want %d\n%s", w.Code, http.StatusMultiStatus, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "424") {
		t.Errorf("body = %q, want a 424 Failed Dependency propstat", w.Body.String())
	}

	got := propfind(t, h, "/alice/work/", "0", askFor(davName("displayname"))).
		at(t, "/alice/work/").value(t, davName("displayname"))
	if got == "Half-applied" {
		t.Error("a failed PROPPATCH applied one of its instructions anyway")
	}
}

func TestProppatchTargetsOnlyAnAddressBook(t *testing.T) {
	store := newStore(t)
	seedItem(t, store, "alice", "standup.vcf")
	h := handlerFor(t, store, carddav.Config{})

	body := `<?xml version="1.0"?>
<D:propertyupdate xmlns:D="DAV:"><D:set><D:prop><D:displayname>x</D:displayname></D:prop></D:set></D:propertyupdate>`
	for _, target := range []string{"/alice/", "/alice/work/standup.vcf"} {
		if w := proppatch(h, target, body); w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: status = %d, want %d", target, w.Code, http.StatusMethodNotAllowed)
		}
	}
}

func TestProppatchRequiresUpdateSettings(t *testing.T) {
	store := newStore(t)
	if err := store.Share(carddav.AddressBookRef{Account: "carol", Book: carddav.MustSegment("work")}, "alice"); err != nil {
		t.Fatalf("sharing: %v", err)
	}
	h := handlerFor(t, store, carddav.Config{})

	body := `<?xml version="1.0"?>
<D:propertyupdate xmlns:D="DAV:"><D:set><D:prop><D:displayname>mine now</D:displayname></D:prop></D:set></D:propertyupdate>`
	if w := proppatch(h, "/carol/work/", body); w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestProppatchNeedsAnUpdatingBackend(t *testing.T) {
	h := handlerFor(t, readOnlyBackend{newStore(t)}, carddav.Config{})

	body := `<?xml version="1.0"?>
<D:propertyupdate xmlns:D="DAV:"><D:set><D:prop><D:displayname>x</D:displayname></D:prop></D:set></D:propertyupdate>`
	if w := proppatch(h, "/alice/work/", body); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestDeleteRemovesAnAddressBookAndItsItems(t *testing.T) {
	store := newStore(t)
	seedItem(t, store, "alice", "standup.vcf")
	h := handlerFor(t, store, carddav.Config{})

	if w := del(h, "/alice/work/", nil); w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d\n%s", w.Code, http.StatusNoContent, w.Body.String())
	}
	for _, target := range []string{"/alice/work/", "/alice/work/standup.vcf"} {
		if w := do(h, http.MethodGet, target); w.Code != http.StatusNotFound {
			t.Errorf("%s after delete: status = %d, want %d", target, w.Code, http.StatusNotFound)
		}
	}
}

func TestDeleteAddressBookRequiresItsOwnPermission(t *testing.T) {
	// An editor may change every item and still not delete the address book.
	h := handlerFor(t, editorOnly{newStore(t)}, carddav.Config{})

	if w := del(h, "/alice/work/", nil); w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestDeleteAddressBookNeedsADeletingBackend(t *testing.T) {
	h := handlerFor(t, readOnlyBackend{newStore(t)}, carddav.Config{})

	if w := del(h, "/alice/work/", nil); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestDeleteOnAnAccountIsRefused(t *testing.T) {
	h := handlerFor(t, newStore(t), carddav.Config{})

	if w := del(h, "/alice/", nil); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

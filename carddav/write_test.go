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

const reviewVCF = "BEGIN:VCARD\r\nVERSION:4.0\r\nUID:review\r\nFN:Rev Ewer\r\nEND:VCARD\r\n"

func put(h *carddav.Handler, target, body string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPut, target, strings.NewReader(body))
	r.Header.Set("Content-Type", "text/vcard")
	for name, value := range headers {
		r.Header.Set(name, value)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func del(h *carddav.Handler, target string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodDelete, target, http.NoBody)
	for name, value := range headers {
		r.Header.Set(name, value)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestPutCreatesAnItem(t *testing.T) {
	h := handlerFor(t, newStore(t), carddav.Config{})

	w := put(h, "/alice/work/review.vcf", reviewVCF, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d\n%s", w.Code, http.StatusCreated, w.Body.String())
	}
	if w.Header().Get("ETag") == "" {
		t.Error("no ETag, so the client cannot make its next write conditional")
	}

	got := do(h, http.MethodGet, "/alice/work/review.vcf")
	if got.Body.String() != reviewVCF {
		t.Errorf("stored bytes differ from what was sent:\n%q", got.Body.String())
	}
}

func TestPutReplaceIsConditional(t *testing.T) {
	store := newStore(t)
	seedItem(t, store, "alice", "standup.vcf")
	h := handlerFor(t, store, carddav.Config{})
	current := do(h, http.MethodGet, "/alice/work/standup.vcf").Header().Get("ETag")

	replaced := strings.Replace(standupVCF, "Stan Dupp", "Stan Dupp Jr", 1)

	w := put(h, "/alice/work/standup.vcf", replaced, map[string]string{"If-Match": current})
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d\n%s", w.Code, http.StatusNoContent, w.Body.String())
	}
	fresh := w.Header().Get("ETag")
	if fresh == "" || fresh == current {
		t.Errorf("ETag after replace = %q, want a new one (had %q)", fresh, current)
	}

	// The write moved the revision on, so the tag the client used is now stale
	// and the same request must not apply twice.
	if w := put(h, "/alice/work/standup.vcf", replaced, map[string]string{"If-Match": current}); w.Code != http.StatusPreconditionFailed {
		t.Errorf("stale If-Match: status = %d, want %d", w.Code, http.StatusPreconditionFailed)
	}

	// The tag just issued has to work as the next If-Match, or the client is
	// stranded: it holds a validator no write will ever match.
	again := strings.Replace(replaced, "Jr", "Sr", 1)
	if w := put(h, "/alice/work/standup.vcf", again, map[string]string{"If-Match": fresh}); w.Code != http.StatusNoContent {
		t.Errorf("If-Match with the returned ETag: status = %d, want %d\n%s", w.Code, http.StatusNoContent, w.Body.String())
	}
}

func TestPutIfNoneMatchStarRefusesOverwrite(t *testing.T) {
	store := newStore(t)
	seedItem(t, store, "alice", "standup.vcf")
	h := handlerFor(t, store, carddav.Config{})

	conflictBody := strings.Replace(reviewVCF, "UID:review", "UID:standup", 1)
	if w := put(h, "/alice/work/standup.vcf", conflictBody, map[string]string{"If-None-Match": "*"}); w.Code != http.StatusPreconditionFailed {
		t.Errorf("over an existing item: status = %d, want %d", w.Code, http.StatusPreconditionFailed)
	}
	if w := put(h, "/alice/work/review.vcf", reviewVCF, map[string]string{"If-None-Match": "*"}); w.Code != http.StatusCreated {
		t.Errorf("at a free name: status = %d, want %d", w.Code, http.StatusCreated)
	}
}

func TestPutRejectsUnparseableContent(t *testing.T) {
	h := handlerFor(t, newStore(t), carddav.Config{})

	w := put(h, "/alice/work/junk.vcf", "this is not a vcard", nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
	if !strings.Contains(w.Body.String(), "valid-address-data") {
		t.Errorf("body = %q, want the CARDDAV:valid-address-data precondition", w.Body.String())
	}
}

func TestPutRejectsACardBreakingResourceRules(t *testing.T) {
	h := handlerFor(t, newStore(t), carddav.Config{})

	// RFC 6352 §5.1: an address object resource is exactly one vCard, and this
	// server additionally needs its UID as the item's identity.
	tests := map[string]string{
		"two cards in one resource":     standupVCF + reviewVCF,
		"a missing UID":                 "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:No Identity\r\nEND:VCARD\r\n",
		"a missing FN":                  "BEGIN:VCARD\r\nVERSION:4.0\r\nUID:noname\r\nEND:VCARD\r\n",
		"a missing VERSION":             "BEGIN:VCARD\r\nUID:nover\r\nFN:No Version\r\nEND:VCARD\r\n",
		"trailing bytes after the card": reviewVCF + "this is not a vcard\r\n",
		"leading bytes before the card": "this is not a vcard\r\n" + reviewVCF,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			w := put(h, "/alice/work/bad.vcf", body, nil)
			if w.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want %d\n%s", w.Code, http.StatusForbidden, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), "valid-address-data") {
				t.Errorf("body = %q, want the CARDDAV:valid-address-data precondition", w.Body.String())
			}
		})
	}
}

func TestPutRejectsAVersionTheServerCannotServe(t *testing.T) {
	h := handlerFor(t, newStore(t), carddav.Config{})

	// The address book advertises text/vcard 3.0 and 4.0; storing a 2.1 card
	// would hand every reader data the server never claimed to support.
	body := "BEGIN:VCARD\r\nVERSION:2.1\r\nUID:old\r\nFN:Very Old\r\nEND:VCARD\r\n"
	w := put(h, "/alice/work/old.vcf", body, nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d\n%s", w.Code, http.StatusForbidden, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "supported-address-data") {
		t.Errorf("body = %q, want the CARDDAV:supported-address-data precondition", w.Body.String())
	}
}

func TestPutRefusesAUIDHeldByAnotherItem(t *testing.T) {
	store := newStore(t)
	seedItem(t, store, "alice", "standup.vcf") // holds UID "standup"
	h := handlerFor(t, store, carddav.Config{})

	conflictBody := strings.Replace(reviewVCF, "UID:review", "UID:standup", 1)
	w := put(h, "/alice/work/other.vcf", conflictBody, nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d\n%s", w.Code, http.StatusConflict, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "no-uid-conflict") {
		t.Errorf("body = %q, want the CARDDAV:no-uid-conflict precondition", w.Body.String())
	}
}

func TestPutRejectsAForeignContentType(t *testing.T) {
	h := handlerFor(t, newStore(t), carddav.Config{})

	r := httptest.NewRequest(http.MethodPut, "/alice/work/review.vcf", strings.NewReader(reviewVCF))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnsupportedMediaType)
	}
}

func TestPutBoundsTheBody(t *testing.T) {
	h := handlerFor(t, newStore(t), carddav.Config{})

	oversized := "BEGIN:VCARD\r\n" + strings.Repeat("X-PAD:y\r\n", (10<<20)/9+1)
	w := put(h, "/alice/work/huge.vcf", oversized, nil)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusRequestEntityTooLarge)
	}
	if !strings.Contains(w.Body.String(), "max-resource-size") {
		t.Errorf("body = %q, want the CARDDAV:max-resource-size precondition", w.Body.String())
	}
}

// smallBook narrows one address book's MaxItemSize without teaching the store
// about limits: the handler must honour what GetAddressBook reports.
type smallBook struct{ *carddavmem.Store }

func (b smallBook) GetAddressBook(ctx context.Context, ref carddav.AddressBookRef) (carddav.AddressBook, error) {
	book, err := b.Store.GetAddressBook(ctx, ref)
	book.MaxItemSize = 64
	return book, err
}

func TestPutHonoursTheBooksOwnSizeLimit(t *testing.T) {
	h := handlerFor(t, smallBook{newStore(t)}, carddav.Config{})

	oversized := strings.Replace(reviewVCF, "FN:Rev Ewer",
		"FN:Rev Ewer\r\nNOTE:"+strings.Repeat("x", 100), 1)
	w := put(h, "/alice/work/review.vcf", oversized, nil)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d\n%s", w.Code, http.StatusRequestEntityTooLarge, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "max-resource-size") {
		t.Errorf("body = %q, want the CARDDAV:max-resource-size precondition", w.Body.String())
	}
}

func TestPutRequiresWritePermission(t *testing.T) {
	store := newStore(t)
	if err := store.Share(carddav.AddressBookRef{Account: "carol", Book: carddav.MustSegment("work")}, "alice"); err != nil {
		t.Fatalf("sharing: %v", err)
	}
	h := handlerFor(t, store, carddav.Config{})

	// The share is view-only: alice can see the address book, so the refusal
	// is an honest 403 rather than a concealing 404.
	if w := put(h, "/carol/work/review.vcf", reviewVCF, nil); w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestPutOnACollectionIsRefused(t *testing.T) {
	h := handlerFor(t, newStore(t), carddav.Config{})

	for _, target := range []string{"/alice/", "/alice/work/"} {
		if w := put(h, target, reviewVCF, nil); w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: status = %d, want %d", target, w.Code, http.StatusMethodNotAllowed)
		}
	}
}

func TestWritesNeedAWritingBackend(t *testing.T) {
	store := newStore(t)
	seedItem(t, store, "alice", "standup.vcf")
	h := handlerFor(t, readOnlyBackend{store}, carddav.Config{})

	if w := put(h, "/alice/work/review.vcf", reviewVCF, nil); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("PUT: status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
	if w := del(h, "/alice/work/standup.vcf", nil); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("DELETE: status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestAllowShrinksWithTheBackendsCapabilities(t *testing.T) {
	assertAllowMatchesDispatch(t, func() *carddav.Handler {
		store := newStore(t)
		seedItem(t, store, "alice", "standup.vcf")
		return handlerFor(t, readOnlyBackend{store}, carddav.Config{})
	}, "/alice/", "/alice/work/", "/alice/work/standup.vcf")
}

func TestDeleteRemovesAnItem(t *testing.T) {
	store := newStore(t)
	seedItem(t, store, "alice", "standup.vcf")
	h := handlerFor(t, store, carddav.Config{})

	if w := del(h, "/alice/work/standup.vcf", nil); w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d\n%s", w.Code, http.StatusNoContent, w.Body.String())
	}
	if w := do(h, http.MethodGet, "/alice/work/standup.vcf"); w.Code != http.StatusNotFound {
		t.Errorf("after delete: GET = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestDeleteIsConditional(t *testing.T) {
	store := newStore(t)
	seedItem(t, store, "alice", "standup.vcf")
	h := handlerFor(t, store, carddav.Config{})
	current := do(h, http.MethodGet, "/alice/work/standup.vcf").Header().Get("ETag")

	stale := strings.Replace(current, `-`, `-f`, 1)
	if w := del(h, "/alice/work/standup.vcf", map[string]string{"If-Match": stale}); w.Code != http.StatusPreconditionFailed {
		t.Errorf("stale If-Match: status = %d, want %d", w.Code, http.StatusPreconditionFailed)
	}
	if w := do(h, http.MethodGet, "/alice/work/standup.vcf"); w.Code != http.StatusOK {
		t.Errorf("the refused delete removed the item anyway: GET = %d", w.Code)
	}

	if w := del(h, "/alice/work/standup.vcf", map[string]string{"If-Match": current}); w.Code != http.StatusNoContent {
		t.Errorf("current If-Match: status = %d, want %d", w.Code, http.StatusNoContent)
	}
}

func TestDeleteOfAMissingItemIsNotFound(t *testing.T) {
	h := handlerFor(t, newStore(t), carddav.Config{})

	if w := del(h, "/alice/work/gone.vcf", nil); w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestDeleteRequiresPermission(t *testing.T) {
	store := newStore(t)
	seedItem(t, store, "carol", "standup.vcf")
	if err := store.Share(carddav.AddressBookRef{Account: "carol", Book: carddav.MustSegment("work")}, "alice"); err != nil {
		t.Fatalf("sharing: %v", err)
	}
	h := handlerFor(t, store, carddav.Config{})

	if w := del(h, "/carol/work/standup.vcf", nil); w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
	if w := do(h, http.MethodGet, "/carol/work/standup.vcf"); w.Code != http.StatusOK {
		t.Errorf("the refused delete removed the item anyway: GET = %d", w.Code)
	}
}

// createOnly may add items but never replace one. Which of the two applies
// depends on whether the target exists — a fact only the backend's transaction
// knows — so this grant is what proves the handler hands both flags over
// faithfully instead of collapsing them.
type createOnly struct{ *carddavmem.Store }

func (createOnly) AddressBookPermissions(context.Context, carddav.Actor, carddav.AddressBookRef) (carddav.AddressBookPermissions, error) {
	return carddav.AddressBookPermissions{ViewDetails: true, CreateItems: true}, nil
}

// replaceOnly is createOnly's mirror: it may overwrite what exists but never
// add a name.
type replaceOnly struct{ *carddavmem.Store }

func (replaceOnly) AddressBookPermissions(context.Context, carddav.Actor, carddav.AddressBookRef) (carddav.AddressBookPermissions, error) {
	return carddav.AddressBookPermissions{ViewDetails: true, ReplaceItems: true}, nil
}

func TestPutPermissionIsSelectedByExistence(t *testing.T) {
	store := newStore(t)
	seedItem(t, store, "alice", "standup.vcf")
	h := handlerFor(t, createOnly{store}, carddav.Config{})

	if w := put(h, "/alice/work/review.vcf", reviewVCF, nil); w.Code != http.StatusCreated {
		t.Errorf("creating: status = %d, want %d\n%s", w.Code, http.StatusCreated, w.Body.String())
	}
	replaced := strings.Replace(standupVCF, "Stan Dupp", "Stan Dupp Jr", 1)
	if w := put(h, "/alice/work/standup.vcf", replaced, nil); w.Code != http.StatusForbidden {
		t.Errorf("replacing: status = %d, want %d", w.Code, http.StatusForbidden)
	}

	h = handlerFor(t, replaceOnly{store}, carddav.Config{})
	if w := put(h, "/alice/work/standup.vcf", replaced, nil); w.Code != http.StatusNoContent {
		t.Errorf("replace-only replacing: status = %d, want %d\n%s", w.Code, http.StatusNoContent, w.Body.String())
	}
	freshCard := strings.Replace(reviewVCF, "UID:review", "UID:someone-new", 1)
	if w := put(h, "/alice/work/new.vcf", freshCard, nil); w.Code != http.StatusForbidden {
		t.Errorf("replace-only creating: status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

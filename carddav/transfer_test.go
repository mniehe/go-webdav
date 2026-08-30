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

// transferStore has two of alice's address books, with a card in "work".
func transferStore(t *testing.T) *carddavmem.Store {
	t.Helper()

	store := newStore(t)
	req := carddav.CreateAddressBookRequest{Name: carddav.MustSegment("personal"), DisplayName: "Personal"}
	if _, err := store.CompareAndCreateAddressBook(context.Background(), "alice", req, carddav.Unconditional()); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	seedRaw(t, store, "alice", "ada.vcf", adaVCF, "ada")
	return store
}

func transfer(h *carddav.Handler, method, source, dest string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, source, http.NoBody)
	r.Header.Set("Destination", dest)
	for name, value := range headers {
		r.Header.Set(name, value)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestCopyDuplicatesAnItemAcrossAddressBooks(t *testing.T) {
	store := transferStore(t)
	h := handlerFor(t, store, carddav.Config{})

	w := transfer(h, "COPY", "/alice/work/ada.vcf", "/alice/personal/copy.vcf", nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d\n%s", w.Code, http.StatusCreated, w.Body.String())
	}

	if got := do(h, http.MethodGet, "/alice/work/ada.vcf"); got.Code != http.StatusOK {
		t.Errorf("source after COPY: status = %d, want %d", got.Code, http.StatusOK)
	}
	if got := do(h, http.MethodGet, "/alice/personal/copy.vcf"); got.Body.String() != adaVCF {
		t.Errorf("destination bytes differ from the source:\n%q", got.Body.String())
	}
}

func TestCopyWithinAnAddressBookHitsTheUIDConflict(t *testing.T) {
	store := transferStore(t)
	h := handlerFor(t, store, carddav.Config{})

	// The source still holds the UID, and one address book cannot hold the
	// same card twice.
	w := transfer(h, "COPY", "/alice/work/ada.vcf", "/alice/work/twin.vcf", nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d\n%s", w.Code, http.StatusConflict, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "no-uid-conflict") {
		t.Errorf("body = %q, want the CARDDAV:no-uid-conflict precondition", w.Body.String())
	}
}

func TestMoveRelocatesAnItem(t *testing.T) {
	store := transferStore(t)
	h := handlerFor(t, store, carddav.Config{})

	w := transfer(h, "MOVE", "/alice/work/ada.vcf", "/alice/personal/ada.vcf", nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d\n%s", w.Code, http.StatusCreated, w.Body.String())
	}
	if got := do(h, http.MethodGet, "/alice/work/ada.vcf"); got.Code != http.StatusNotFound {
		t.Errorf("source after MOVE: status = %d, want %d", got.Code, http.StatusNotFound)
	}
	if got := do(h, http.MethodGet, "/alice/personal/ada.vcf"); got.Body.String() != adaVCF {
		t.Errorf("destination bytes differ from the source:\n%q", got.Body.String())
	}
}

func TestMoveRenamesWithinAnAddressBook(t *testing.T) {
	store := transferStore(t)
	h := handlerFor(t, store, carddav.Config{})

	if w := transfer(h, "MOVE", "/alice/work/ada.vcf", "/alice/work/renamed.vcf", nil); w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d\n%s", w.Code, http.StatusCreated, w.Body.String())
	}
	if got := do(h, http.MethodGet, "/alice/work/ada.vcf"); got.Code != http.StatusNotFound {
		t.Errorf("old name still resolves: %d", got.Code)
	}
	if got := do(h, http.MethodGet, "/alice/work/renamed.vcf"); got.Code != http.StatusOK {
		t.Errorf("new name: status = %d, want %d", got.Code, http.StatusOK)
	}
}

func TestMoveOntoItselfIsANoOp(t *testing.T) {
	store := transferStore(t)
	h := handlerFor(t, store, carddav.Config{})

	// The degenerate MOVE /A → /A must not follow "write then delete source"
	// to a deleted item.
	w := transfer(h, "MOVE", "/alice/work/ada.vcf", "/alice/work/ada.vcf", nil)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d\n%s", w.Code, http.StatusNoContent, w.Body.String())
	}
	if got := do(h, http.MethodGet, "/alice/work/ada.vcf"); got.Code != http.StatusOK {
		t.Errorf("the item vanished: status = %d", got.Code)
	}
}

func TestTransferHonoursOverwrite(t *testing.T) {
	store := transferStore(t)
	seedRaw(t, store, "alice", "bob.vcf", bobVCF, "bob")
	h := handlerFor(t, store, carddav.Config{})

	// RFC 4918 §9.8.4: Overwrite: F over an existing destination is 412.
	refused := transfer(h, "COPY", "/alice/work/ada.vcf", "/alice/work/bob.vcf", map[string]string{"Overwrite": "F"})
	if refused.Code != http.StatusPreconditionFailed {
		t.Fatalf("Overwrite F: status = %d, want %d\n%s", refused.Code, http.StatusPreconditionFailed, refused.Body.String())
	}
	if got := do(h, http.MethodGet, "/alice/work/bob.vcf"); !strings.Contains(got.Body.String(), "UID:bob") {
		t.Error("the refused transfer replaced the destination anyway")
	}

	// The default overwrites: Bob's content is replaced and his old UID
	// retired, answered 204 because nothing was created.
	replaced := transfer(h, "MOVE", "/alice/work/ada.vcf", "/alice/work/bob.vcf", nil)
	if replaced.Code != http.StatusNoContent {
		t.Fatalf("overwrite: status = %d, want %d\n%s", replaced.Code, http.StatusNoContent, replaced.Body.String())
	}
	if got := do(h, http.MethodGet, "/alice/work/bob.vcf"); !strings.Contains(got.Body.String(), "UID:ada") {
		t.Errorf("destination = %q, want the moved content", got.Body.String())
	}
}

func TestMoveChecksASourcePrecondition(t *testing.T) {
	store := transferStore(t)
	h := handlerFor(t, store, carddav.Config{})

	current := do(h, http.MethodGet, "/alice/work/ada.vcf").Header().Get("ETag")
	stale := strings.Replace(current, `-`, `-f`, 1)

	w := transfer(h, "MOVE", "/alice/work/ada.vcf", "/alice/personal/ada.vcf", map[string]string{"If-Match": stale})
	if w.Code != http.StatusPreconditionFailed {
		t.Fatalf("status = %d, want %d\n%s", w.Code, http.StatusPreconditionFailed, w.Body.String())
	}
	if got := do(h, http.MethodGet, "/alice/work/ada.vcf"); got.Code != http.StatusOK {
		t.Errorf("the refused move removed the source: %d", got.Code)
	}
}

func TestTransferIntoAMissingAddressBookIsAConflict(t *testing.T) {
	store := transferStore(t)
	// editorOnly grants permissions on any reference, so the destination is
	// not concealed and the backend discovers the missing address book.
	// RFC 4918 §9.8.5: a destination whose parent does not exist is 409.
	h := handlerFor(t, editorOnly{store}, carddav.Config{})

	w := transfer(h, "COPY", "/alice/work/ada.vcf", "/alice/nowhere/ada.vcf", nil)
	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want %d\n%s", w.Code, http.StatusConflict, w.Body.String())
	}

	// Without a grant the same destination is concealed: under the default
	// denial policy it must look exactly like any other URL that is not there.
	concealed := handlerFor(t, store, carddav.Config{})
	if w := transfer(concealed, "COPY", "/alice/work/ada.vcf", "/alice/nowhere/ada.vcf", nil); w.Code != http.StatusNotFound {
		t.Errorf("concealed: status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestTransferIntoAnAddressBookTheActorCannotWrite(t *testing.T) {
	store := transferStore(t)
	seedRaw(t, store, "carol", "hers.vcf", bobVCF, "bob")
	if err := store.Share(carddav.AddressBookRef{Account: "carol", Book: carddav.MustSegment("work")}, "alice"); err != nil {
		t.Fatalf("sharing: %v", err)
	}
	h := handlerFor(t, store, carddav.Config{})

	// The share is view-only: the destination is visible, so the refusal is an
	// honest 403 — and nothing lands there.
	w := transfer(h, "COPY", "/alice/work/ada.vcf", "/carol/work/theirs.vcf", nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d\n%s", w.Code, http.StatusForbidden, w.Body.String())
	}
	if got := do(h, http.MethodGet, "/carol/work/theirs.vcf"); got.Code != http.StatusNotFound {
		t.Errorf("the refused copy landed: %d", got.Code)
	}
}

func TestTransferIntoAConcealedAddressBookIsNotFound(t *testing.T) {
	store := transferStore(t)
	h := handlerFor(t, store, carddav.Config{})

	// carol's address book exists but alice holds no grant at all, so the
	// destination must look exactly like one that does not exist.
	w := transfer(h, "COPY", "/alice/work/ada.vcf", "/carol/work/theirs.vcf", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d\n%s", w.Code, http.StatusNotFound, w.Body.String())
	}
}

func TestMoveRequiresDeleteOnTheSource(t *testing.T) {
	store := transferStore(t)
	h := handlerFor(t, createOnly{store}, carddav.Config{})

	// createOnly may add items anywhere but never remove one, and a MOVE
	// removes its source.
	w := transfer(h, "MOVE", "/alice/work/ada.vcf", "/alice/personal/ada.vcf", nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d\n%s", w.Code, http.StatusForbidden, w.Body.String())
	}
	if got := do(h, http.MethodGet, "/alice/work/ada.vcf"); got.Code != http.StatusOK {
		t.Errorf("the refused move removed the source: %d", got.Code)
	}
}

func TestTransferNeedsATransferringBackend(t *testing.T) {
	store := transferStore(t)
	h := handlerFor(t, readOnlyBackend{store}, carddav.Config{})

	for _, method := range []string{"COPY", "MOVE"} {
		if w := transfer(h, method, "/alice/work/ada.vcf", "/alice/personal/dst.vcf", nil); w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: status = %d, want %d", method, w.Code, http.StatusMethodNotAllowed)
		}
	}
}

func TestTransferOfAnAddressBookIsRefused(t *testing.T) {
	store := transferStore(t)
	h := handlerFor(t, store, carddav.Config{})

	if w := transfer(h, "COPY", "/alice/work/", "/alice/backup/", nil); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

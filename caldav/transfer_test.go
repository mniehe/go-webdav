package caldav_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mniehe/davkit/caldav"
	"github.com/mniehe/davkit/caldavmem"
)

// transferStore has two of alice's calendars, with an event in "work".
func transferStore(t *testing.T) *caldavmem.Store {
	t.Helper()

	store := newStore(t)
	req := caldav.CreateCalendarRequest{Name: caldav.MustSegment("personal"), DisplayName: "Personal"}
	if _, err := store.CompareAndCreateCalendar(context.Background(), "alice", req, caldav.Unconditional()); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	seedRaw(t, store, "alice", "august.ics", augustICS, "august")
	return store
}

func transfer(h *caldav.Handler, method, source, dest string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, source, http.NoBody)
	r.Header.Set("Destination", dest)
	for name, value := range headers {
		r.Header.Set(name, value)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestCopyDuplicatesAnItemAcrossCalendars(t *testing.T) {
	store := transferStore(t)
	h := handlerFor(t, store, caldav.Config{})

	w := transfer(h, "COPY", "/alice/work/august.ics", "/alice/personal/copy.ics", nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d\n%s", w.Code, http.StatusCreated, w.Body.String())
	}

	if got := do(h, http.MethodGet, "/alice/work/august.ics"); got.Code != http.StatusOK {
		t.Errorf("source after COPY: status = %d, want %d", got.Code, http.StatusOK)
	}
	if got := do(h, http.MethodGet, "/alice/personal/copy.ics"); got.Body.String() != augustICS {
		t.Errorf("destination bytes differ from the source:\n%q", got.Body.String())
	}
}

func TestCopyWithinACalendarHitsTheUIDConflict(t *testing.T) {
	store := transferStore(t)
	h := handlerFor(t, store, caldav.Config{})

	// The source still holds the UID, and one calendar cannot hold the same
	// event twice.
	w := transfer(h, "COPY", "/alice/work/august.ics", "/alice/work/twin.ics", nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d\n%s", w.Code, http.StatusConflict, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "no-uid-conflict") {
		t.Errorf("body = %q, want the CALDAV:no-uid-conflict precondition", w.Body.String())
	}
}

func TestMoveRelocatesAnItem(t *testing.T) {
	store := transferStore(t)
	h := handlerFor(t, store, caldav.Config{})

	w := transfer(h, "MOVE", "/alice/work/august.ics", "/alice/personal/august.ics", nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d\n%s", w.Code, http.StatusCreated, w.Body.String())
	}
	if got := do(h, http.MethodGet, "/alice/work/august.ics"); got.Code != http.StatusNotFound {
		t.Errorf("source after MOVE: status = %d, want %d", got.Code, http.StatusNotFound)
	}
	if got := do(h, http.MethodGet, "/alice/personal/august.ics"); got.Body.String() != augustICS {
		t.Errorf("destination bytes differ from the source:\n%q", got.Body.String())
	}
}

func TestMoveRenamesWithinACalendar(t *testing.T) {
	store := transferStore(t)
	h := handlerFor(t, store, caldav.Config{})

	if w := transfer(h, "MOVE", "/alice/work/august.ics", "/alice/work/renamed.ics", nil); w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d\n%s", w.Code, http.StatusCreated, w.Body.String())
	}
	if got := do(h, http.MethodGet, "/alice/work/august.ics"); got.Code != http.StatusNotFound {
		t.Errorf("old name still resolves: %d", got.Code)
	}
	if got := do(h, http.MethodGet, "/alice/work/renamed.ics"); got.Code != http.StatusOK {
		t.Errorf("new name: status = %d, want %d", got.Code, http.StatusOK)
	}
}

func TestMoveOntoItselfIsANoOp(t *testing.T) {
	store := transferStore(t)
	h := handlerFor(t, store, caldav.Config{})

	// The degenerate MOVE /A → /A must not follow "write then delete source"
	// to a deleted item.
	w := transfer(h, "MOVE", "/alice/work/august.ics", "/alice/work/august.ics", nil)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d\n%s", w.Code, http.StatusNoContent, w.Body.String())
	}
	if got := do(h, http.MethodGet, "/alice/work/august.ics"); got.Code != http.StatusOK {
		t.Errorf("the item vanished: status = %d", got.Code)
	}
}

func TestTransferHonoursOverwrite(t *testing.T) {
	store := transferStore(t)
	seedRaw(t, store, "alice", "october.ics", octoberICS, "october")
	h := handlerFor(t, store, caldav.Config{})

	// RFC 4918 §9.8.4: Overwrite: F over an existing destination is 412.
	refused := transfer(h, "COPY", "/alice/work/august.ics", "/alice/work/october.ics", map[string]string{"Overwrite": "F"})
	if refused.Code != http.StatusPreconditionFailed {
		t.Fatalf("Overwrite F: status = %d, want %d\n%s", refused.Code, http.StatusPreconditionFailed, refused.Body.String())
	}
	if got := do(h, http.MethodGet, "/alice/work/october.ics"); !strings.Contains(got.Body.String(), "UID:october") {
		t.Error("the refused transfer replaced the destination anyway")
	}

	// The default overwrites: October's content is replaced and its old UID
	// retired, answered 204 because nothing was created.
	replaced := transfer(h, "MOVE", "/alice/work/august.ics", "/alice/work/october.ics", nil)
	if replaced.Code != http.StatusNoContent {
		t.Fatalf("overwrite: status = %d, want %d\n%s", replaced.Code, http.StatusNoContent, replaced.Body.String())
	}
	if got := do(h, http.MethodGet, "/alice/work/october.ics"); !strings.Contains(got.Body.String(), "UID:august") {
		t.Errorf("destination = %q, want the moved content", got.Body.String())
	}
}

func TestMoveChecksASourcePrecondition(t *testing.T) {
	store := transferStore(t)
	h := handlerFor(t, store, caldav.Config{})

	current := do(h, http.MethodGet, "/alice/work/august.ics").Header().Get("ETag")
	stale := strings.Replace(current, `-`, `-f`, 1)

	w := transfer(h, "MOVE", "/alice/work/august.ics", "/alice/personal/august.ics", map[string]string{"If-Match": stale})
	if w.Code != http.StatusPreconditionFailed {
		t.Fatalf("status = %d, want %d\n%s", w.Code, http.StatusPreconditionFailed, w.Body.String())
	}
	if got := do(h, http.MethodGet, "/alice/work/august.ics"); got.Code != http.StatusOK {
		t.Errorf("the refused move removed the source: %d", got.Code)
	}
}

func TestTransferIntoAMissingCalendarIsAConflict(t *testing.T) {
	store := transferStore(t)
	// editorOnly grants permissions on any reference, so the destination is
	// not concealed and the backend discovers the missing calendar. RFC 4918
	// §9.8.5: a destination whose parent does not exist is 409.
	h := handlerFor(t, editorOnly{store}, caldav.Config{})

	w := transfer(h, "COPY", "/alice/work/august.ics", "/alice/nowhere/august.ics", nil)
	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want %d\n%s", w.Code, http.StatusConflict, w.Body.String())
	}

	// Without a grant the same destination is concealed: under the default
	// denial policy it must look exactly like any other URL that is not there.
	concealed := handlerFor(t, store, caldav.Config{})
	if w := transfer(concealed, "COPY", "/alice/work/august.ics", "/alice/nowhere/august.ics", nil); w.Code != http.StatusNotFound {
		t.Errorf("concealed: status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestTransferIntoACalendarTheActorCannotWrite(t *testing.T) {
	store := transferStore(t)
	seedRaw(t, store, "carol", "hers.ics", octoberICS, "october")
	if err := store.Share(caldav.CalendarRef{Account: "carol", Calendar: caldav.MustSegment("work")}, "alice"); err != nil {
		t.Fatalf("sharing: %v", err)
	}
	h := handlerFor(t, store, caldav.Config{})

	// The share is view-only: the destination is visible, so the refusal is an
	// honest 403 — and nothing lands there.
	w := transfer(h, "COPY", "/alice/work/august.ics", "/carol/work/theirs.ics", nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d\n%s", w.Code, http.StatusForbidden, w.Body.String())
	}
	if got := do(h, http.MethodGet, "/carol/work/theirs.ics"); got.Code != http.StatusNotFound {
		t.Errorf("the refused copy landed: %d", got.Code)
	}
}

func TestTransferIntoAConcealedCalendarIsNotFound(t *testing.T) {
	store := transferStore(t)
	h := handlerFor(t, store, caldav.Config{})

	// carol's calendar exists but alice holds no grant at all, so the
	// destination must look exactly like one that does not exist.
	w := transfer(h, "COPY", "/alice/work/august.ics", "/carol/work/theirs.ics", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d\n%s", w.Code, http.StatusNotFound, w.Body.String())
	}
}

func TestMoveRequiresDeleteOnTheSource(t *testing.T) {
	store := transferStore(t)
	h := handlerFor(t, createOnly{store}, caldav.Config{})

	// createOnly may add items anywhere but never remove one, and a MOVE
	// removes its source.
	w := transfer(h, "MOVE", "/alice/work/august.ics", "/alice/personal/august.ics", nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d\n%s", w.Code, http.StatusForbidden, w.Body.String())
	}
	if got := do(h, http.MethodGet, "/alice/work/august.ics"); got.Code != http.StatusOK {
		t.Errorf("the refused move removed the source: %d", got.Code)
	}
}

func TestTransferNeedsATransferringBackend(t *testing.T) {
	store := transferStore(t)
	h := handlerFor(t, readOnlyBackend{store}, caldav.Config{})

	for _, method := range []string{"COPY", "MOVE"} {
		if w := transfer(h, method, "/alice/work/august.ics", "/alice/personal/dst.ics", nil); w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: status = %d, want %d", method, w.Code, http.StatusMethodNotAllowed)
		}
	}
}

func TestTransferOfACalendarIsRefused(t *testing.T) {
	store := transferStore(t)
	h := handlerFor(t, store, caldav.Config{})

	if w := transfer(h, "COPY", "/alice/work/", "/alice/backup/", nil); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

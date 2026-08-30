package caldav_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/mniehe/davkit/caldav"
	"github.com/mniehe/davkit/caldavmem"
)

// zeroRevWriter stores normally but reports a zero revision, the value a
// read-only backend uses to ask for content-hash ETags. From a writing backend
// it is a contract violation: the hash ETag cannot be parsed back into a
// revision, so the client's next If-Match write could never succeed.
type zeroRevWriter struct{ *caldavmem.Store }

//nolint:gocritic // the signature must match the ItemWriter interface exactly
func (z zeroRevWriter) CompareAndStoreItem(ctx context.Context, ref caldav.ItemRef, req caldav.StoreItemRequest) (caldav.StoreItemResult, error) {
	res, err := z.Store.CompareAndStoreItem(ctx, ref, req)
	res.Revision = 0
	return res, err
}

func TestWriteRejectsAZeroRevisionFromAWritingBackend(t *testing.T) {
	h := handlerFor(t, zeroRevWriter{newStore(t)}, caldav.Config{})

	w := put(h, "/alice/work/review.ics", eventICS, nil)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d: a writing backend must not return a zero revision", w.Code, http.StatusInternalServerError)
	}
}

// loopingSyncer reports a change while claiming its position did not advance —
// exactly what makes a client re-request the same token forever.
type loopingSyncer struct{ *caldavmem.Store }

func (loopingSyncer) ListChanges(_ context.Context, _ caldav.CalendarRef, after caldav.Revision, _ int) (caldav.ChangeBatch, error) {
	return caldav.ChangeBatch{
		Changes:        []caldav.Change{{Item: caldav.MustSegment("x.ics")}},
		CoveredThrough: after, // did not advance
		HasMore:        true,
	}, nil
}

func TestSyncRejectsABatchThatDoesNotAdvance(t *testing.T) {
	store := newStore(t)
	seedItem(t, store, "alice", "standup.ics")
	h := handlerFor(t, loopingSyncer{store}, caldav.Config{})

	// The initial sync (empty token) goes through the item listing and hands
	// back a usable token; the delta for that token is where the bad batch is.
	token := reportMS(t, h, "/alice/work/", syncBody("")).SyncToken
	if token == "" {
		t.Fatal("no initial sync token")
	}

	w := report(t, h, "/alice/work/", syncBody(token))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d: a non-advancing batch would loop the client forever", w.Code, http.StatusInternalServerError)
	}
}

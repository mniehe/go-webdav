package carddav_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/mniehe/davkit/carddav"
	"github.com/mniehe/davkit/carddavmem"
)

type zeroRevWriter struct{ *carddavmem.Store }

//nolint:gocritic // the signature must match the ItemWriter interface exactly
func (z zeroRevWriter) CompareAndStoreItem(ctx context.Context, ref carddav.ItemRef, req carddav.StoreItemRequest) (carddav.StoreItemResult, error) {
	res, err := z.Store.CompareAndStoreItem(ctx, ref, req)
	res.Revision = 0
	return res, err
}

func TestWriteRejectsAZeroRevisionFromAWritingBackend(t *testing.T) {
	h := handlerFor(t, zeroRevWriter{newStore(t)}, carddav.Config{})

	w := put(h, "/alice/work/review.vcf", reviewVCF, nil)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d: a writing backend must not return a zero revision", w.Code, http.StatusInternalServerError)
	}
}

type loopingSyncer struct{ *carddavmem.Store }

func (loopingSyncer) ListChanges(_ context.Context, _ carddav.AddressBookRef, after carddav.Revision, _ int) (carddav.ChangeBatch, error) {
	return carddav.ChangeBatch{
		Changes:        []carddav.Change{{Item: carddav.MustSegment("x.vcf")}},
		CoveredThrough: after,
		HasMore:        true,
	}, nil
}

func TestSyncRejectsABatchThatDoesNotAdvance(t *testing.T) {
	store := newStore(t)
	seedItem(t, store, "alice", "standup.vcf")
	h := handlerFor(t, loopingSyncer{store}, carddav.Config{})

	token := reportMS(t, h, "/alice/work/", syncBody("")).SyncToken
	if token == "" {
		t.Fatal("no initial sync token")
	}

	w := report(t, h, "/alice/work/", syncBody(token))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d: a non-advancing batch would loop the client forever", w.Code, http.StatusInternalServerError)
	}
}

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

// These backends violate the output contract in one way each. The handler
// must answer a loud 500, never serve a response built from the bad output.

type doubledBooks struct{ *carddavmem.Store }

func (b doubledBooks) ListAddressBooks(ctx context.Context, account carddav.AccountID) ([]carddav.AddressBook, error) {
	cals, err := b.Store.ListAddressBooks(ctx, account)
	if err != nil {
		return nil, err
	}
	return append(cals, cals[0]), nil
}

type namelessBook struct{ *carddavmem.Store }

func (b namelessBook) ListAddressBooks(ctx context.Context, account carddav.AccountID) ([]carddav.AddressBook, error) {
	cals, err := b.Store.ListAddressBooks(ctx, account)
	if err != nil {
		return nil, err
	}
	cals[0].Name = carddav.Segment{}
	return cals, nil
}

type zeroIDListing struct{ *carddavmem.Store }

func (b zeroIDListing) ListAddressBooks(ctx context.Context, account carddav.AccountID) ([]carddav.AddressBook, error) {
	cals, err := b.Store.ListAddressBooks(ctx, account)
	if err != nil {
		return nil, err
	}
	cals[0].ID = ""
	return cals, nil
}

type zeroIDBook struct{ *carddavmem.Store }

func (b zeroIDBook) GetAddressBook(ctx context.Context, ref carddav.AddressBookRef) (carddav.AddressBook, error) {
	cal, err := b.Store.GetAddressBook(ctx, ref)
	cal.ID = ""
	return cal, err
}

type renamedBook struct{ *carddavmem.Store }

func (b renamedBook) GetAddressBook(ctx context.Context, ref carddav.AddressBookRef) (carddav.AddressBook, error) {
	cal, err := b.Store.GetAddressBook(ctx, ref)
	cal.Name = carddav.MustSegment("impostor")
	return cal, err
}

type renamedItem struct{ *carddavmem.Store }

func (b renamedItem) GetItem(ctx context.Context, ref carddav.ItemRef) (carddav.Item, error) {
	item, err := b.Store.GetItem(ctx, ref)
	item.Name = carddav.MustSegment("impostor.ics")
	return item, err
}

type doubledItems struct{ *carddavmem.Store }

func (b doubledItems) ListItems(ctx context.Context, ref carddav.AddressBookRef, yield func(carddav.Item) bool) (carddav.Revision, error) {
	var items []carddav.Item
	rev, err := b.Store.ListItems(ctx, ref, func(item carddav.Item) bool {
		items = append(items, item)
		return true
	})
	if err != nil {
		return 0, err
	}
	for _, item := range append(items, items...) {
		if !yield(item) {
			break
		}
	}
	return rev, nil
}

type namelessItems struct{ *carddavmem.Store }

func (b namelessItems) ListItems(ctx context.Context, ref carddav.AddressBookRef, yield func(carddav.Item) bool) (carddav.Revision, error) {
	rev, err := b.Store.ListItems(ctx, ref, func(item carddav.Item) bool {
		item.Name = carddav.Segment{}
		return yield(item)
	})
	return rev, err
}

func rawPropfind(h *carddav.Handler, target, depth string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("PROPFIND", target, strings.NewReader(allProp))
	r.Header.Set("Content-Type", "application/xml")
	r.Header.Set("Depth", depth)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestContractViolationsAreLoud(t *testing.T) {
	for name, tc := range map[string]struct {
		backend func(*carddavmem.Store) carddav.Backend
		method  string
		target  string
		depth   string
	}{
		"calendar listed twice": {
			backend: func(s *carddavmem.Store) carddav.Backend { return doubledBooks{s} },
			method:  "PROPFIND", target: "/alice/", depth: "1",
		},
		"calendar with an invalid name": {
			backend: func(s *carddavmem.Store) carddav.Backend { return namelessBook{s} },
			method:  "PROPFIND", target: "/alice/", depth: "1",
		},
		"calendar listed with a zero ID": {
			backend: func(s *carddavmem.Store) carddav.Backend { return zeroIDListing{s} },
			method:  "PROPFIND", target: "/alice/", depth: "1",
		},
		"calendar with a zero ID": {
			backend: func(s *carddavmem.Store) carddav.Backend { return zeroIDBook{s} },
			method:  http.MethodGet, target: "/alice/work/standup.vcf",
		},
		"calendar renamed on the way out": {
			backend: func(s *carddavmem.Store) carddav.Backend { return renamedBook{s} },
			method:  http.MethodGet, target: "/alice/work/standup.vcf",
		},
		"item renamed on the way out": {
			backend: func(s *carddavmem.Store) carddav.Backend { return renamedItem{s} },
			method:  http.MethodGet, target: "/alice/work/standup.vcf",
		},
		"item listed twice": {
			backend: func(s *carddavmem.Store) carddav.Backend { return doubledItems{s} },
			method:  "PROPFIND", target: "/alice/work/", depth: "1",
		},
		"item with an invalid name": {
			backend: func(s *carddavmem.Store) carddav.Backend { return namelessItems{s} },
			method:  "PROPFIND", target: "/alice/work/", depth: "1",
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := newStore(t)
			seedItem(t, store, "alice", "standup.vcf")
			h := handlerFor(t, tc.backend(store), carddav.Config{})

			var w *httptest.ResponseRecorder
			if tc.method == "PROPFIND" {
				w = rawPropfind(h, tc.target, tc.depth)
			} else {
				w = do(h, tc.method, tc.target)
			}
			if w.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want %d\n%s", w.Code, http.StatusInternalServerError, w.Body.String())
			}
		})
	}
}

func TestContractViolationFailsAMultigetOutright(t *testing.T) {
	store := newStore(t)
	seedItem(t, store, "alice", "standup.vcf")
	h := handlerFor(t, renamedItem{store}, carddav.Config{})

	// A violation must not shrink to one bad row in an otherwise-successful
	// multistatus: the rows around it would read as a complete answer.
	w := report(t, h, "/alice/work/", multigetBody("/alice/work/standup.vcf"))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d\n%s", w.Code, http.StatusInternalServerError, w.Body.String())
	}
}

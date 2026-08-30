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

// These backends violate the output contract in one way each. The handler
// must answer a loud 500, never serve a response built from the bad output.

type doubledCalendars struct{ *caldavmem.Store }

func (b doubledCalendars) ListCalendars(ctx context.Context, account caldav.AccountID) ([]caldav.Calendar, error) {
	cals, err := b.Store.ListCalendars(ctx, account)
	if err != nil {
		return nil, err
	}
	return append(cals, cals[0]), nil
}

type namelessCalendar struct{ *caldavmem.Store }

func (b namelessCalendar) ListCalendars(ctx context.Context, account caldav.AccountID) ([]caldav.Calendar, error) {
	cals, err := b.Store.ListCalendars(ctx, account)
	if err != nil {
		return nil, err
	}
	cals[0].Name = caldav.Segment{}
	return cals, nil
}

type zeroIDListing struct{ *caldavmem.Store }

func (b zeroIDListing) ListCalendars(ctx context.Context, account caldav.AccountID) ([]caldav.Calendar, error) {
	cals, err := b.Store.ListCalendars(ctx, account)
	if err != nil {
		return nil, err
	}
	cals[0].ID = ""
	return cals, nil
}

type zeroIDCalendar struct{ *caldavmem.Store }

func (b zeroIDCalendar) GetCalendar(ctx context.Context, ref caldav.CalendarRef) (caldav.Calendar, error) {
	cal, err := b.Store.GetCalendar(ctx, ref)
	cal.ID = ""
	return cal, err
}

type renamedCalendar struct{ *caldavmem.Store }

func (b renamedCalendar) GetCalendar(ctx context.Context, ref caldav.CalendarRef) (caldav.Calendar, error) {
	cal, err := b.Store.GetCalendar(ctx, ref)
	cal.Name = caldav.MustSegment("impostor")
	return cal, err
}

type renamedItem struct{ *caldavmem.Store }

func (b renamedItem) GetItem(ctx context.Context, ref caldav.ItemRef) (caldav.Item, error) {
	item, err := b.Store.GetItem(ctx, ref)
	item.Name = caldav.MustSegment("impostor.ics")
	return item, err
}

type doubledItems struct{ *caldavmem.Store }

func (b doubledItems) ListItems(ctx context.Context, ref caldav.CalendarRef, yield func(caldav.Item) bool) (caldav.Revision, error) {
	var items []caldav.Item
	rev, err := b.Store.ListItems(ctx, ref, func(item caldav.Item) bool {
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

type namelessItems struct{ *caldavmem.Store }

func (b namelessItems) ListItems(ctx context.Context, ref caldav.CalendarRef, yield func(caldav.Item) bool) (caldav.Revision, error) {
	rev, err := b.Store.ListItems(ctx, ref, func(item caldav.Item) bool {
		item.Name = caldav.Segment{}
		return yield(item)
	})
	return rev, err
}

func rawPropfind(h *caldav.Handler, target, depth string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("PROPFIND", target, strings.NewReader(allProp))
	r.Header.Set("Content-Type", "application/xml")
	r.Header.Set("Depth", depth)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestContractViolationsAreLoud(t *testing.T) {
	for name, tc := range map[string]struct {
		backend func(*caldavmem.Store) caldav.Backend
		method  string
		target  string
		depth   string
	}{
		"calendar listed twice": {
			backend: func(s *caldavmem.Store) caldav.Backend { return doubledCalendars{s} },
			method:  "PROPFIND", target: "/alice/", depth: "1",
		},
		"calendar with an invalid name": {
			backend: func(s *caldavmem.Store) caldav.Backend { return namelessCalendar{s} },
			method:  "PROPFIND", target: "/alice/", depth: "1",
		},
		"calendar listed with a zero ID": {
			backend: func(s *caldavmem.Store) caldav.Backend { return zeroIDListing{s} },
			method:  "PROPFIND", target: "/alice/", depth: "1",
		},
		"calendar with a zero ID": {
			backend: func(s *caldavmem.Store) caldav.Backend { return zeroIDCalendar{s} },
			method:  http.MethodGet, target: "/alice/work/standup.ics",
		},
		"calendar renamed on the way out": {
			backend: func(s *caldavmem.Store) caldav.Backend { return renamedCalendar{s} },
			method:  http.MethodGet, target: "/alice/work/standup.ics",
		},
		"item renamed on the way out": {
			backend: func(s *caldavmem.Store) caldav.Backend { return renamedItem{s} },
			method:  http.MethodGet, target: "/alice/work/standup.ics",
		},
		"item listed twice": {
			backend: func(s *caldavmem.Store) caldav.Backend { return doubledItems{s} },
			method:  "PROPFIND", target: "/alice/work/", depth: "1",
		},
		"item with an invalid name": {
			backend: func(s *caldavmem.Store) caldav.Backend { return namelessItems{s} },
			method:  "PROPFIND", target: "/alice/work/", depth: "1",
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := newStore(t)
			seedItem(t, store, "alice", "standup.ics")
			h := handlerFor(t, tc.backend(store), caldav.Config{})

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
	seedItem(t, store, "alice", "standup.ics")
	h := handlerFor(t, renamedItem{store}, caldav.Config{})

	// A violation must not shrink to one bad row in an otherwise-successful
	// multistatus: the rows around it would read as a complete answer.
	w := report(t, h, "/alice/work/", multigetBody("/alice/work/standup.ics"))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d\n%s", w.Code, http.StatusInternalServerError, w.Body.String())
	}
}

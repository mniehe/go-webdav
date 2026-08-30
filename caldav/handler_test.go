package caldav_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mniehe/davkit/caldav"
	"github.com/mniehe/davkit/caldavmem"
)

func newStore(t *testing.T) *caldavmem.Store {
	t.Helper()

	store := caldavmem.New()
	for _, account := range []caldav.AccountID{"alice", "carol"} {
		req := caldav.CreateCalendarRequest{
			Name:        caldav.MustSegment("work"),
			DisplayName: "Work",
		}
		if _, err := store.CompareAndCreateCalendar(context.Background(), account, req, caldav.Unconditional()); err != nil {
			t.Fatalf("seeding %s: %v", account, err)
		}
	}
	return store
}

func handlerFor(t *testing.T, backend caldav.Backend, cfg caldav.Config) *caldav.Handler {
	t.Helper()

	if cfg.Authenticate == nil {
		cfg.Authenticate = func(*http.Request) (caldav.Actor, error) {
			return caldav.Actor{Account: "alice"}, nil
		}
	}
	if cfg.WWWAuthenticate == "" {
		cfg.WWWAuthenticate = `Basic realm="test"`
	}

	h, err := caldav.NewHandler(backend, cfg)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return h
}

func do(h *caldav.Handler, method, target string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(method, target, http.NoBody))
	return w
}

func TestNewHandlerRefusesAnUnusableConfig(t *testing.T) {
	store := newStore(t)
	auth := func(*http.Request) (caldav.Actor, error) { return caldav.Actor{}, nil }

	tests := map[string]struct {
		backend caldav.Backend
		cfg     caldav.Config
	}{
		"no backend":          {nil, caldav.Config{Authenticate: auth, WWWAuthenticate: "Basic"}},
		"no authenticator":    {store, caldav.Config{WWWAuthenticate: "Basic"}},
		"no WWW-Authenticate": {store, caldav.Config{Authenticate: auth}},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := caldav.NewHandler(test.backend, test.cfg); err == nil {
				t.Fatal("NewHandler accepted the configuration")
			}
		})
	}
}

func TestHandlerAsksForCredentials(t *testing.T) {
	h := handlerFor(t, newStore(t), caldav.Config{
		Authenticate: func(*http.Request) (caldav.Actor, error) {
			return caldav.Actor{}, fmt.Errorf("no header: %w", caldav.ErrUnauthorized)
		},
		WWWAuthenticate: `Basic realm="calendar"`,
	})

	w := do(h, http.MethodDelete, "/alice/work/")
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
	if got := w.Header().Get("WWW-Authenticate"); got != `Basic realm="calendar"` {
		t.Errorf("WWW-Authenticate = %q, want the configured challenge", got)
	}
}

func TestHandlerReportsAFailedLookupAsAServerError(t *testing.T) {
	h := handlerFor(t, newStore(t), caldav.Config{
		Authenticate: func(*http.Request) (caldav.Actor, error) {
			return caldav.Actor{}, errors.New("directory unreachable")
		},
	})

	w := do(h, http.MethodDelete, "/alice/work/")
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
	if body := w.Body.String(); !strings.Contains(body, http.StatusText(http.StatusInternalServerError)) {
		t.Errorf("body = %q, want the status text and nothing about the cause", body)
	}
	if strings.Contains(w.Body.String(), "directory unreachable") {
		t.Error("the authenticator's error reached the client")
	}
}

func TestHandlerRejectsAPathThatNamesNoResource(t *testing.T) {
	h := handlerFor(t, newStore(t), caldav.Config{})

	for _, target := range []string{"/alice/work/one/two", "/alice/work/item/"} {
		t.Run(target, func(t *testing.T) {
			if w := do(h, http.MethodDelete, target); w.Code != http.StatusNotFound {
				t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
			}
		})
	}
}

func TestHandlerConcealsWhatAnActorMayNotSee(t *testing.T) {
	h := handlerFor(t, newStore(t), caldav.Config{})

	// carol's calendar exists; alice has no share of it. Concealing is the
	// default because a 403 here would confirm that it does.
	for _, target := range []string{"/carol/", "/carol/work/", "/carol/work/thing.ics"} {
		t.Run(target, func(t *testing.T) {
			if w := do(h, http.MethodDelete, target); w.Code != http.StatusNotFound {
				t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
			}
		})
	}
}

func TestHandlerRevealsRefusalWhenConfiguredTo(t *testing.T) {
	h := handlerFor(t, newStore(t), caldav.Config{Denial: caldav.RevealDenied})

	for _, target := range []string{"/carol/", "/carol/work/", "/carol/work/thing.ics"} {
		t.Run(target, func(t *testing.T) {
			if w := do(h, http.MethodDelete, target); w.Code != http.StatusForbidden {
				t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
			}
		})
	}
}

func TestHandlerResolvesWhatTheActorMaySee(t *testing.T) {
	h := handlerFor(t, newStore(t), caldav.Config{})

	// MKCOL is not implemented yet, and a 405 is how this test tells resolution
	// succeeding apart from the request having been refused or misrouted.
	for _, target := range []string{"/alice/", "/alice/work/", "/alice/work/thing.ics"} {
		t.Run(target, func(t *testing.T) {
			if w := do(h, "MKCOL", target); w.Code != http.StatusMethodNotAllowed {
				t.Errorf("status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
			}
		})
	}
}

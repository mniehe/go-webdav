package carddav_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mniehe/davkit/carddav"
	"github.com/mniehe/davkit/carddavmem"
)

func newStore(t *testing.T) *carddavmem.Store {
	t.Helper()

	store := carddavmem.New()
	for _, account := range []carddav.AccountID{"alice", "carol"} {
		req := carddav.CreateAddressBookRequest{
			Name:        carddav.MustSegment("work"),
			DisplayName: "Work",
		}
		if _, err := store.CompareAndCreateAddressBook(context.Background(), account, req, carddav.Unconditional()); err != nil {
			t.Fatalf("seeding %s: %v", account, err)
		}
	}
	return store
}

func handlerFor(t *testing.T, backend carddav.Backend, cfg carddav.Config) *carddav.Handler {
	t.Helper()

	if cfg.Authenticate == nil {
		cfg.Authenticate = func(*http.Request) (carddav.Actor, error) {
			return carddav.Actor{Account: "alice"}, nil
		}
	}
	if cfg.WWWAuthenticate == "" {
		cfg.WWWAuthenticate = `Basic realm="test"`
	}

	h, err := carddav.NewHandler(backend, cfg)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return h
}

func do(h *carddav.Handler, method, target string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(method, target, http.NoBody))
	return w
}

func TestNewHandlerRefusesAnUnusableConfig(t *testing.T) {
	store := newStore(t)
	auth := func(*http.Request) (carddav.Actor, error) { return carddav.Actor{}, nil }

	tests := map[string]struct {
		backend carddav.Backend
		cfg     carddav.Config
	}{
		"no backend":          {nil, carddav.Config{Authenticate: auth, WWWAuthenticate: "Basic"}},
		"no authenticator":    {store, carddav.Config{WWWAuthenticate: "Basic"}},
		"no WWW-Authenticate": {store, carddav.Config{Authenticate: auth}},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := carddav.NewHandler(test.backend, test.cfg); err == nil {
				t.Fatal("NewHandler accepted the configuration")
			}
		})
	}
}

func TestHandlerAsksForCredentials(t *testing.T) {
	h := handlerFor(t, newStore(t), carddav.Config{
		Authenticate: func(*http.Request) (carddav.Actor, error) {
			return carddav.Actor{}, fmt.Errorf("no header: %w", carddav.ErrUnauthorized)
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
	h := handlerFor(t, newStore(t), carddav.Config{
		Authenticate: func(*http.Request) (carddav.Actor, error) {
			return carddav.Actor{}, errors.New("directory unreachable")
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
	h := handlerFor(t, newStore(t), carddav.Config{})

	for _, target := range []string{"/alice/work/one/two", "/alice/work/item/"} {
		t.Run(target, func(t *testing.T) {
			if w := do(h, http.MethodDelete, target); w.Code != http.StatusNotFound {
				t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
			}
		})
	}
}

func TestHandlerConcealsWhatAnActorMayNotSee(t *testing.T) {
	h := handlerFor(t, newStore(t), carddav.Config{})

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
	h := handlerFor(t, newStore(t), carddav.Config{Denial: carddav.RevealDenied})

	for _, target := range []string{"/carol/", "/carol/work/", "/carol/work/thing.ics"} {
		t.Run(target, func(t *testing.T) {
			if w := do(h, http.MethodDelete, target); w.Code != http.StatusForbidden {
				t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
			}
		})
	}
}

func TestHandlerResolvesWhatTheActorMaySee(t *testing.T) {
	h := handlerFor(t, newStore(t), carddav.Config{})

	// Each probe is a method the target's kind never serves: a 405 is how this
	// test tells resolution succeeding apart from the request having been
	// refused or misrouted.
	probes := map[string]*http.Request{
		"/alice/":               everyMethod[http.MethodPut]("/alice/"),
		"/alice/work/":          everyMethod[http.MethodPut]("/alice/work/"),
		"/alice/work/thing.vcf": everyMethod["PROPPATCH"]("/alice/work/thing.vcf"),
	}
	for target, r := range probes {
		t.Run(target, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusMethodNotAllowed {
				t.Errorf("status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
			}
		})
	}
}

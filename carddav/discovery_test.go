package carddav_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mniehe/davkit/carddav"
)

func TestWellKnownRedirectsToThePrincipal(t *testing.T) {
	h := handlerFor(t, newStore(t), carddav.Config{})

	// RFC 6764 §5: a client given only a host name starts at the well-known
	// URI. It follows the redirect and asks whatever it lands on for
	// DAV:current-user-principal — which the account resource answers.
	for _, method := range []string{http.MethodGet, "PROPFIND", http.MethodOptions} {
		t.Run(method, func(t *testing.T) {
			w := do(h, method, "/.well-known/carddav")
			if w.Code != http.StatusMovedPermanently {
				t.Fatalf("status = %d, want %d\n%s", w.Code, http.StatusMovedPermanently, w.Body.String())
			}
			if loc := w.Header().Get("Location"); loc != "/alice/" {
				t.Errorf("Location = %q, want the actor's principal %q", loc, "/alice/")
			}
		})
	}
}

func TestServerRootRedirectsToThePrincipal(t *testing.T) {
	h := handlerFor(t, newStore(t), carddav.Config{})

	w := do(h, "PROPFIND", "/")
	if w.Code != http.StatusMovedPermanently {
		t.Fatalf("status = %d, want %d\n%s", w.Code, http.StatusMovedPermanently, w.Body.String())
	}
	if loc := w.Header().Get("Location"); loc != "/alice/" {
		t.Errorf("Location = %q, want the actor's principal %q", loc, "/alice/")
	}
}

func TestDiscoveryChallengesBeforeRedirecting(t *testing.T) {
	// Clients probe the well-known URI without credentials first; the 401 with
	// its challenge is what tells them to retry authenticated. Redirecting an
	// anonymous probe would name an account to someone who never logged in.
	cfg := carddav.Config{
		Authenticate: func(*http.Request) (carddav.Actor, error) {
			return carddav.Actor{}, carddav.ErrUnauthorized
		},
	}
	h := handlerFor(t, newStore(t), cfg)

	w := do(h, "PROPFIND", "/.well-known/carddav")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
	if w.Header().Get("WWW-Authenticate") == "" {
		t.Error("no WWW-Authenticate challenge; the client has no way to retry with credentials")
	}
}

func TestDiscoveryFlowReachesTheAddressBookHome(t *testing.T) {
	h := handlerFor(t, newStore(t), carddav.Config{})

	// The whole bootstrap, as a client walks it: well-known, redirect,
	// current-user-principal, addressbook-home-set — all landing on one URL.
	target := do(h, "PROPFIND", "/.well-known/carddav").Header().Get("Location")
	if target == "" {
		t.Fatal("no redirect target")
	}
	r := httptest.NewRequest("PROPFIND", target, strings.NewReader(allProp))
	r.Header.Set("Content-Type", "application/xml")
	r.Header.Set("Depth", "0")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	body := w.Body.String()
	if w.Code != http.StatusMultiStatus {
		t.Fatalf("status = %d, want %d\n%s", w.Code, http.StatusMultiStatus, body)
	}
	for _, want := range []string{"current-user-principal", "addressbook-home-set"} {
		if !strings.Contains(body, want) {
			t.Errorf("discovery target answers without %s:\n%s", want, body)
		}
	}
}

// rootClaimingRoutes maps the bare root to alice's account, as a custom
// layout might.
type rootClaimingRoutes struct{ carddav.Routes }

func (r rootClaimingRoutes) Parse(ctx context.Context, path string) (carddav.Resource, error) {
	if path == "/" {
		return carddav.AccountResource("alice"), nil
	}
	return r.Routes.Parse(ctx, path)
}

func TestRootIsServedWhenRoutesClaimIt(t *testing.T) {
	h := handlerFor(t, newStore(t), carddav.Config{Routes: rootClaimingRoutes{carddav.DefaultRoutes("")}})

	// The redirect is a fallback for layouts with nothing at the root; a
	// layout that puts a resource there keeps it.
	w := do(h, "PROPFIND", "/")
	if w.Code != http.StatusMultiStatus {
		t.Errorf("status = %d, want %d\n%s", w.Code, http.StatusMultiStatus, w.Body.String())
	}
}

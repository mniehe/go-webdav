package caldav

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/mniehe/davkit/internal"
)

// Authenticator identifies whoever is making a request. Return ErrUnauthorized
// to make the server ask for credentials; any other error is a 500.
type Authenticator func(*http.Request) (Actor, error)

// DenialPolicy is what the server says when an actor may not do something.
type DenialPolicy uint8

const (
	// ConcealDenied answers 404, so an actor cannot tell a calendar they may not
	// see from one that does not exist. This is the default because the
	// alternative is an existence oracle: probe URLs, and the ones that come
	// back 403 are real.
	ConcealDenied DenialPolicy = iota

	// RevealDenied answers 403. Choose it when every actor is already trusted to
	// know what exists, and a clear refusal is worth more than concealment.
	RevealDenied
)

// Config configures a Handler.
type Config struct {
	// Authenticate is required.
	Authenticate Authenticator

	// WWWAuthenticate is sent verbatim in that header when Authenticate returns
	// ErrUnauthorized, for example `Basic realm="calendar"`. Required: without
	// it a client is told it is unauthorised and given no way to fix that.
	WWWAuthenticate string

	// Routes maps between URLs and resources. Nil uses DefaultRoutes("").
	Routes Routes

	// Denial decides what an actor is told when refused.
	Denial DenialPolicy

	// MaxSearchResults bounds a client's search. Exceeding it fails the request
	// rather than returning a partial answer that looks complete. Zero uses
	// DefaultMaxSearchResults. Listings and sync stream instead and are not
	// bounded by it.
	MaxSearchResults int

	// MaxCollectionScan and MaxCollectionBytes bound how much of one collection
	// a single request may materialise. Every request that reads a collection —
	// a query, free-busy, a Depth:1 PROPFIND, an initial sync — lists it whole
	// before answering, so without a ceiling a hostile client could force the
	// entire collection (a million items, or a few very large ones) into memory
	// and through the parser before any per-result limit applies. A listing
	// past either ceiling fails with 507 rather than a partial success. Zero
	// uses the defaults below.
	MaxCollectionScan  int
	MaxCollectionBytes int64
}

// DefaultMaxSearchResults bounds a search that did not configure one.
const DefaultMaxSearchResults = 10_000

// The scan budget a handler falls back to. Generous enough that no ordinary
// personal collection reaches it, small enough that a hostile collection is
// stopped before it exhausts memory: the per-item cap is already 10 MiB, so
// the byte ceiling bounds the sum to a few hundred large objects or tens of
// thousands of ordinary ones.
const (
	DefaultMaxCollectionScan  = 100_000
	DefaultMaxCollectionBytes = 256 << 20
)

// Handler serves CalDAV over a Backend.
type Handler struct {
	dav     internal.Handler
	adapter *adapter
	cfg     Config
}

// NewHandler validates the configuration and returns a handler over b.
func NewHandler(b Backend, cfg Config) (*Handler, error) {
	switch {
	case b == nil:
		return nil, fmt.Errorf("caldav: nil backend")
	case cfg.Authenticate == nil:
		return nil, fmt.Errorf("caldav: Config.Authenticate is required")
	case cfg.WWWAuthenticate == "":
		return nil, fmt.Errorf("caldav: Config.WWWAuthenticate is required, or a client cannot learn how to authenticate")
	}
	if cfg.Routes == nil {
		cfg.Routes = DefaultRoutes("")
	}
	if cfg.MaxSearchResults <= 0 {
		cfg.MaxSearchResults = DefaultMaxSearchResults
	}
	if cfg.MaxCollectionScan <= 0 {
		cfg.MaxCollectionScan = DefaultMaxCollectionScan
	}
	if cfg.MaxCollectionBytes <= 0 {
		cfg.MaxCollectionBytes = DefaultMaxCollectionBytes
	}

	h := &Handler{cfg: cfg}
	h.adapter = &adapter{backend: b, cfg: cfg, caps: capabilitiesOf(b)}
	h.dav = internal.Handler{Backend: h.adapter}
	return h, nil
}

type actorContextKey struct{}

// actorFrom recovers the actor ServeHTTP put in the context. It cannot fail:
// nothing reaches the adapter without passing through ServeHTTP first.
func actorFrom(ctx context.Context) Actor {
	actor, ok := ctx.Value(actorContextKey{}).(Actor)
	if !ok {
		return Actor{}
	}
	return actor
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	actor, err := h.cfg.Authenticate(r)
	if err != nil {
		if errors.Is(err, ErrUnauthorized) {
			w.Header().Set("WWW-Authenticate", h.cfg.WWWAuthenticate)
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		internal.ServeError(w, err)
		return
	}

	ctx := context.WithValue(r.Context(), actorContextKey{}, actor)
	r = r.WithContext(ctx)

	if h.redirectToPrincipal(w, r) {
		return
	}

	if ifErr := internal.RejectUnsupportedIf(r); ifErr != nil {
		internal.ServeError(w, ifErr)
		return
	}

	// internal.Handler does not know REPORT, so it is dispatched here — behind
	// the same path validation every other method gets.
	if r.Method == "REPORT" || r.Method == "MKCALENDAR" {
		if err := internal.CheckRequestPath(r); err != nil {
			internal.ServeError(w, err)
			return
		}
		handle := h.adapter.handleReport
		if r.Method == "MKCALENDAR" {
			handle = h.adapter.handleMkcalendar
		}
		if err := handle(w, r); err != nil {
			internal.ServeError(w, err)
		}
		return
	}
	h.dav.ServeHTTP(w, r)
}

// redirectToPrincipal bootstraps discovery (RFC 6764 §5): the well-known URI
// and the bare root both redirect to the authenticated actor's principal,
// whose PROPFIND answers current-user-principal and calendar-home-set.
// Redirecting straight there spares the context-path hop the RFC sketches.
//
// The well-known path is reserved by the RFC and always taken; the root is
// only taken when Routes does not claim it for a resource of its own.
func (h *Handler) redirectToPrincipal(w http.ResponseWriter, r *http.Request) bool {
	path := r.URL.EscapedPath()
	if path != "/.well-known/caldav" && path != "/" {
		return false
	}
	if path == "/" {
		if _, err := h.cfg.Routes.Parse(r.Context(), path); !errors.Is(err, ErrNotFound) {
			return false
		}
	}
	href, err := h.adapter.principalHref(r.Context())
	if err != nil {
		internal.ServeError(w, err)
		return true
	}
	http.Redirect(w, r, href, http.StatusMovedPermanently)
	return true
}

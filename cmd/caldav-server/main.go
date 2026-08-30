// caldav-server serves in-memory CalDAV and CardDAV backends for manual client
// testing: point DAVx5 or Thunderbird at it and walk discovery, sync, write
// and delete. Calendars live under /cal, address books under /card, and both
// well-known URIs route to the right handler. Everything lives in memory and
// vanishes on exit.
package main

import (
	"context"
	"crypto/subtle"
	"flag"
	"log"
	"net/http"
	"time"

	"github.com/mniehe/davkit/caldav"
	"github.com/mniehe/davkit/caldavmem"
	"github.com/mniehe/davkit/carddav"
	"github.com/mniehe/davkit/carddavmem"
)

func main() {
	var (
		addr     = flag.String("addr", ":8080", "listening address")
		account  = flag.String("account", "demo", "account name, also the username")
		password = flag.String("password", "demo", "basic-auth password")
	)
	flag.Parse()

	cal := caldavHandler(*account, *password)
	card := carddavHandler(*account, *password)

	mux := http.NewServeMux()
	mux.Handle("/.well-known/caldav", cal)
	mux.Handle("/.well-known/carddav", card)
	mux.Handle("/cal/", cal)
	mux.Handle("/card/", card)
	// The bare root goes to CalDAV; a client configured for contacts reaches
	// its handler through the well-known URI above.
	mux.Handle("/", cal)

	log.Printf("CalDAV (/cal) and CardDAV (/card) server for %q listening on %v", *account, *addr)
	srv := &http.Server{
		Addr:              *addr,
		Handler:           logRequests(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Fatal(srv.ListenAndServe())
}

func caldavHandler(account, password string) http.Handler {
	store := caldavmem.New()
	req := caldav.CreateCalendarRequest{
		Name:        caldav.MustSegment("default"),
		DisplayName: "Default",
	}
	if _, err := store.CompareAndCreateCalendar(context.Background(), caldav.AccountID(account), req, caldav.Unconditional()); err != nil {
		log.Fatalf("seeding the calendar: %v", err)
	}

	h, err := caldav.NewHandler(store, caldav.Config{
		Authenticate: func(r *http.Request) (caldav.Actor, error) {
			if !basicAuthOK(r, account, password) {
				return caldav.Actor{}, caldav.ErrUnauthorized
			}
			return caldav.Actor{Account: caldav.AccountID(account)}, nil
		},
		WWWAuthenticate: `Basic realm="caldav-dev"`,
		Routes:          caldav.DefaultRoutes("/cal"),
	})
	if err != nil {
		log.Fatalf("building the CalDAV handler: %v", err)
	}
	return h
}

func carddavHandler(account, password string) http.Handler {
	store := carddavmem.New()
	req := carddav.CreateAddressBookRequest{
		Name:        carddav.MustSegment("default"),
		DisplayName: "Default",
	}
	if _, err := store.CompareAndCreateAddressBook(context.Background(), carddav.AccountID(account), req, carddav.Unconditional()); err != nil {
		log.Fatalf("seeding the address book: %v", err)
	}

	h, err := carddav.NewHandler(store, carddav.Config{
		Authenticate: func(r *http.Request) (carddav.Actor, error) {
			if !basicAuthOK(r, account, password) {
				return carddav.Actor{}, carddav.ErrUnauthorized
			}
			return carddav.Actor{Account: carddav.AccountID(account)}, nil
		},
		WWWAuthenticate: `Basic realm="carddav-dev"`,
		Routes:          carddav.DefaultRoutes("/card"),
	})
	if err != nil {
		log.Fatalf("building the CardDAV handler: %v", err)
	}
	return h
}

func basicAuthOK(r *http.Request, account, password string) bool {
	user, pass, ok := r.BasicAuth()
	userOK := subtle.ConstantTimeCompare([]byte(user), []byte(account)) == 1
	passOK := subtle.ConstantTimeCompare([]byte(pass), []byte(password)) == 1
	return ok && userOK && passOK
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Printf("%s %s -> %d", r.Method, r.URL.Path, rec.status)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

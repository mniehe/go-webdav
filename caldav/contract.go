package caldav

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/mniehe/davkit/internal"
)

// errContract marks a backend read that broke the output contract. It always
// becomes a 500: serving anything built from the bad output — a listing
// missing a member, an ETag scoped to nothing — would be an ambiguous success.
var errContract = errors.New("backend contract violation")

func contractViolation(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{errContract}, args...)...)
}

// checkItemRevision rejects a zero revision from a backend that writes or
// syncs. Such a backend's ETags would fall back to content hashes, which
// parseETag cannot read back into a revision — so a conditional PUT the client
// builds from one could never be satisfied, stranding it. A revisionless
// backend (neither writing nor syncing) is the only one allowed the fallback.
func (a *adapter) checkItemRevision(rev Revision, name Segment) error {
	if rev == 0 && (a.caps.writesItems || a.caps.syncs) {
		return contractViolation("item %q came back with a zero revision from a writing or syncing backend", name)
	}
	return nil
}

// validateChangeBatch checks a sync backend's delta before its CoveredThrough
// is handed back to the client as a token. An unchecked batch can strand a
// client (a token that moves backward), loop it forever (more changes promised
// with no forward progress), or corrupt its view (an item reported twice or
// under an invalid name).
func validateChangeBatch(after Revision, batch ChangeBatch) error {
	if batch.CoveredThrough < after {
		return contractViolation("sync batch moved the position backward, from %d to %d", after, batch.CoveredThrough)
	}
	if batch.HasMore && batch.CoveredThrough <= after {
		return contractViolation("sync batch reports more changes but did not advance past %d", after)
	}
	seen := make(map[Segment]bool, len(batch.Changes))
	for i := range batch.Changes {
		name := batch.Changes[i].Item
		if name.IsZero() {
			return contractViolation("sync batch contains a change with an invalid item name")
		}
		if seen[name] {
			return contractViolation("sync batch reports item %q twice", name)
		}
		seen[name] = true
	}
	return nil
}

// The adapter reads through these wrappers rather than the Backend directly,
// so no read path can forget to check what came back.

func (a *adapter) getCalendar(ctx context.Context, ref CalendarRef) (Calendar, error) {
	cal, err := a.backend.GetCalendar(ctx, ref)
	if err != nil {
		return Calendar{}, err
	}
	if cal.ID == "" {
		return Calendar{}, contractViolation("calendar %q came back with a zero CalendarID", ref.Calendar)
	}
	if cal.Name != ref.Calendar {
		return Calendar{}, contractViolation("calendar %q came back named %q", ref.Calendar, cal.Name)
	}
	return cal, nil
}

func (a *adapter) getItem(ctx context.Context, ref ItemRef) (Item, error) {
	item, err := a.backend.GetItem(ctx, ref)
	if err != nil {
		return Item{}, err
	}
	if item.Name != ref.Item {
		return Item{}, contractViolation("item %q came back named %q", ref.Item, item.Name)
	}
	if err := a.checkItemRevision(item.Revision, item.Name); err != nil {
		return Item{}, err
	}
	return item, nil
}

func (a *adapter) listCalendars(ctx context.Context, account AccountID) ([]Calendar, error) {
	cals, err := a.backend.ListCalendars(ctx, account)
	if err != nil {
		return nil, err
	}
	seen := make(map[Segment]bool, len(cals))
	for i := range cals {
		if cals[i].Name.IsZero() {
			return nil, contractViolation("account %q listed a calendar with an invalid name", account)
		}
		if cals[i].ID == "" {
			return nil, contractViolation("calendar %q came back with a zero CalendarID", cals[i].Name)
		}
		if seen[cals[i].Name] {
			return nil, contractViolation("account %q listed calendar %q twice", account, cals[i].Name)
		}
		seen[cals[i].Name] = true
	}
	return cals, nil
}

// listItems drains the listing before returning it. Rendering inside the
// yield would hold the backend's transaction open for as long as the slowest
// reader takes; validating after the drain checks the listing as a whole.
//
// The drain stops once the collection passes the configured scan budget and
// fails the request with 507: every reader — query, free-busy, Depth:1
// PROPFIND, initial sync — comes through here, so this is the one ceiling that
// keeps a hostile collection from being materialised whole.
func (a *adapter) listItems(ctx context.Context, ref CalendarRef) ([]Item, Revision, error) {
	var items []Item
	var scanned int64
	overrun := false
	rev, err := a.backend.ListItems(ctx, ref, func(item Item) bool {
		items = append(items, item)
		scanned += int64(len(item.Content))
		if len(items) > a.cfg.MaxCollectionScan || scanned > a.cfg.MaxCollectionBytes {
			overrun = true
			return false
		}
		return true
	})
	if err != nil {
		return nil, 0, err
	}
	if overrun {
		return nil, 0, internal.NewPreconditionError(http.StatusInsufficientStorage, internal.NumberOfMatchesWithinLimitsName)
	}
	seen := make(map[Segment]bool, len(items))
	for i := range items {
		if items[i].Name.IsZero() {
			return nil, 0, contractViolation("calendar %q listed an item with an invalid name", ref.Calendar)
		}
		if seen[items[i].Name] {
			return nil, 0, contractViolation("calendar %q listed item %q twice", ref.Calendar, items[i].Name)
		}
		if err := a.checkItemRevision(items[i].Revision, items[i].Name); err != nil {
			return nil, 0, err
		}
		seen[items[i].Name] = true
	}
	return items, rev, nil
}

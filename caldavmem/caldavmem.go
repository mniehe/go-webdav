// Package caldavmem is a reference caldav backend that keeps everything in
// memory.
//
// It exists to be read. Every one of the four obligations on caldav.Backend
// shows up here in the smallest form that satisfies it, and the package's only
// test is the caldavtest conformance suite — so what you are reading is known
// to be a correct implementation, not merely a plausible one.
//
// It is also a usable test fixture: a server's own handler tests can run
// against a backend that provably meets the contract, instead of a hand-rolled
// stub that meets whatever the test remembered to check.
//
// What it is not is a store. Nothing is persisted, nothing is indexed, every
// listing walks the calendar, and one mutex serialises the lot. Content
// validation is not here either: the library parses and checks a body before
// any of these methods sees it.
package caldavmem

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/mniehe/davkit/caldav"
)

// Store is an in-memory set of calendars.
type Store struct {
	mu   sync.RWMutex
	cals map[caldav.CalendarRef]*calendar

	// issued counts every calendar ever created here, so an ID is never reused
	// even by a calendar recreated under a name that has been used before. A
	// fresh Store is a fresh universe; a persistent one would need a sequence
	// that survives restarts.
	issued uint64
}

type calendar struct {
	settings caldav.Calendar
	items    map[string]caldav.Item
	byID     map[string]caldav.Segment
	viewers  map[caldav.AccountID]bool

	rev caldav.Revision
	log []change

	// pruned is the revision below which history is gone. A client asking from
	// earlier than this cannot be answered with a batch, only with
	// ErrHistoryTooOld.
	pruned caldav.Revision
}

type change struct {
	rev     caldav.Revision
	item    caldav.Segment
	deleted bool
}

// New returns an empty store.
func New() *Store {
	return &Store{cals: map[caldav.CalendarRef]*calendar{}}
}

// Interfaces this backend implements. A compile error here is the whole point:
// the conformance suite skips what a backend does not implement, so a method
// signature drifting out of the interface must fail the build rather than
// quietly shrink the suite.
var (
	_ caldav.Backend         = (*Store)(nil)
	_ caldav.ItemWriter      = (*Store)(nil)
	_ caldav.SyncBackend     = (*Store)(nil)
	_ caldav.CalendarCreator = (*Store)(nil)
	_ caldav.CalendarUpdater = (*Store)(nil)
	_ caldav.CalendarDeleter = (*Store)(nil)
	_ caldav.SharingBackend  = (*Store)(nil)
	_ caldav.ItemTransferer  = (*Store)(nil)
)

func (s *Store) CalendarPermissions(_ context.Context, actor caldav.Actor, ref caldav.CalendarRef) (caldav.CalendarPermissions, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cal, ok := s.cals[ref]
	if !ok {
		return caldav.CalendarPermissions{}, nil
	}
	switch {
	case actor.Account == ref.Account:
		return caldav.OwnerPermissions(), nil
	case cal.viewers[actor.Account]:
		return caldav.ViewOnlyPermissions(), nil
	default:
		return caldav.CalendarPermissions{}, nil
	}
}

func (s *Store) AccountPermissions(_ context.Context, actor caldav.Actor, account caldav.AccountID) (caldav.AccountPermissions, error) {
	if actor.Account != account {
		return caldav.AccountPermissions{}, nil
	}
	return caldav.AccountPermissions{ListCalendars: true, CreateCalendars: true}, nil
}

func (s *Store) ListCalendars(_ context.Context, account caldav.AccountID) ([]caldav.Calendar, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cals := make([]caldav.Calendar, 0, len(s.cals))
	for ref, cal := range s.cals {
		if ref.Account == account {
			cals = append(cals, copyCalendar(&cal.settings))
		}
	}
	slices.SortFunc(cals, func(a, b caldav.Calendar) int {
		return cmp.Compare(a.Name.String(), b.Name.String())
	})
	return cals, nil
}

func (s *Store) GetCalendar(_ context.Context, ref caldav.CalendarRef) (caldav.Calendar, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cal, ok := s.cals[ref]
	if !ok {
		return caldav.Calendar{}, caldav.ErrNotFound
	}
	return copyCalendar(&cal.settings), nil
}

func (s *Store) GetItem(_ context.Context, ref caldav.ItemRef) (caldav.Item, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cal, ok := s.cals[ref.Calendar]
	if !ok {
		return caldav.Item{}, caldav.ErrParentNotFound
	}
	item, ok := cal.items[ref.Item.String()]
	if !ok {
		return caldav.Item{}, caldav.ErrNotFound
	}
	return copyItem(item), nil
}

func (s *Store) ListItems(_ context.Context, ref caldav.CalendarRef, yield func(caldav.Item) bool) (caldav.Revision, error) {
	// The read lock spans the iteration and the revision read, so they describe
	// the same state. A SQL backend uses one transaction the same way.
	s.mu.RLock()
	defer s.mu.RUnlock()

	cal, ok := s.cals[ref]
	if !ok {
		return 0, caldav.ErrNotFound
	}
	for _, item := range cal.items {
		if !yield(copyItem(item)) {
			break
		}
	}
	return cal.rev, nil
}

//nolint:gocritic // hugeParam: this signature is caldav.ItemWriter's, and a request travels by value so a backend cannot mutate what the handler still holds.
func (s *Store) CompareAndStoreItem(_ context.Context, ref caldav.ItemRef, req caldav.StoreItemRequest) (caldav.StoreItemResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cal, ok := s.cals[ref.Calendar]
	if !ok {
		return caldav.StoreItemResult{}, caldav.ErrParentNotFound
	}

	key := ref.Item.String()
	existing, exists := cal.items[key]

	// Which permission applies is only knowable now, with the target read.
	if exists && !req.MayReplace {
		return caldav.StoreItemResult{}, caldav.ErrForbidden
	}
	if !exists && !req.MayCreate {
		return caldav.StoreItemResult{}, caldav.ErrForbidden
	}

	if err := req.Preconditions.Check(currentRevision(existing, exists)); err != nil {
		return caldav.StoreItemResult{}, err
	}

	if owner, taken := cal.byID[req.ContentID]; taken && owner != ref.Item {
		return caldav.StoreItemResult{}, &caldav.DuplicateContentIDError{Existing: owner}
	}
	if exists {
		delete(cal.byID, existing.ContentID)
	}

	cal.bump()
	cal.items[key] = caldav.Item{
		Name:      ref.Item,
		ContentID: req.ContentID,
		Content:   slices.Clone(req.Content),
		Revision:  cal.rev,
	}
	cal.byID[req.ContentID] = ref.Item
	cal.log = append(cal.log, change{rev: cal.rev, item: ref.Item})

	return caldav.StoreItemResult{Revision: cal.rev, Created: !exists}, nil
}

func (s *Store) CompareAndDeleteItem(_ context.Context, ref caldav.ItemRef, pre caldav.Preconditions) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cal, ok := s.cals[ref.Calendar]
	if !ok {
		return caldav.ErrParentNotFound
	}

	existing, exists := cal.items[ref.Item.String()]
	if err := pre.Check(currentRevision(existing, exists)); err != nil {
		return err
	}
	if !exists {
		return caldav.ErrNotFound
	}

	delete(cal.items, ref.Item.String())
	delete(cal.byID, existing.ContentID)

	cal.bump()
	// The deletion and its record go in together. Nothing left behind in
	// current state says an item was ever here, so a log written afterwards can
	// be lost and no reader could tell.
	cal.log = append(cal.log, change{rev: cal.rev, item: ref.Item, deleted: true})
	return nil
}

func (s *Store) ListChanges(_ context.Context, ref caldav.CalendarRef, after caldav.Revision, maxChanges int) (caldav.ChangeBatch, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cal, ok := s.cals[ref]
	if !ok {
		return caldav.ChangeBatch{}, caldav.ErrNotFound
	}
	if after < cal.pruned || after > cal.rev {
		return caldav.ChangeBatch{}, caldav.ErrHistoryTooOld
	}

	batch := caldav.ChangeBatch{CoveredThrough: cal.rev}
	var picked []change
	for i := 0; i < len(cal.log); {
		if cal.log[i].rev <= after {
			i++
			continue
		}
		// The limit is a hint, so it is applied between revisions and never
		// within one: half a revision is not a state a client can resume from.
		if maxChanges > 0 && len(picked) >= maxChanges {
			batch.CoveredThrough = picked[len(picked)-1].rev
			batch.HasMore = true
			break
		}
		for rev := cal.log[i].rev; i < len(cal.log) && cal.log[i].rev == rev; i++ {
			picked = append(picked, cal.log[i])
		}
	}

	// One entry per item, in its final state over the interval covered.
	at := map[caldav.Segment]int{}
	for _, c := range picked {
		if idx, seen := at[c.item]; seen {
			batch.Changes[idx].Deleted = c.deleted
			continue
		}
		at[c.item] = len(batch.Changes)
		batch.Changes = append(batch.Changes, caldav.Change{Item: c.item, Deleted: c.deleted})
	}
	return batch, nil
}

//nolint:gocritic // hugeParam: this signature is caldav.CalendarCreator's, and the request travels by value for the same reason as CompareAndStoreItem's.
func (s *Store) CompareAndCreateCalendar(_ context.Context, account caldav.AccountID, req caldav.CreateCalendarRequest, pre caldav.Preconditions) (caldav.Calendar, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ref := caldav.CalendarRef{Account: account, Calendar: req.Name}
	existing, exists := s.cals[ref]

	var current *caldav.Revision
	if exists {
		rev := existing.rev
		current = &rev
	}
	if err := pre.Check(current); err != nil {
		return caldav.Calendar{}, err
	}
	if exists {
		return caldav.Calendar{}, caldav.ErrAlreadyExists
	}

	s.issued++
	cal := &calendar{
		settings: caldav.Calendar{
			ID:          caldav.CalendarID(fmt.Sprintf("caldavmem-%d", s.issued)),
			Name:        req.Name,
			DisplayName: req.DisplayName,
			Description: req.Description,
			Color:       req.Color,
			Timezone:    req.Timezone,
			Accepts:     req.Accepts,
		},
		items:   map[string]caldav.Item{},
		byID:    map[string]caldav.Segment{},
		viewers: map[caldav.AccountID]bool{},
	}
	cal.bump()
	s.cals[ref] = cal
	return copyCalendar(&cal.settings), nil
}

func (s *Store) CompareAndUpdateCalendar(_ context.Context, ref caldav.CalendarRef, patch caldav.CalendarPatch, pre caldav.Preconditions) (caldav.Calendar, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cal, ok := s.cals[ref]
	if !ok {
		return caldav.Calendar{}, caldav.ErrNotFound
	}
	rev := cal.rev
	if err := pre.Check(&rev); err != nil {
		return caldav.Calendar{}, err
	}

	set(&cal.settings.DisplayName, patch.DisplayName)
	set(&cal.settings.Description, patch.Description)
	set(&cal.settings.Color, patch.Color)
	set(&cal.settings.Timezone, patch.Timezone)
	if order, ok := patch.SortOrder.Value(); ok {
		cal.settings.SortOrder = &order
	} else if patch.SortOrder.Clears() {
		cal.settings.SortOrder = nil
	}

	cal.bump()
	return copyCalendar(&cal.settings), nil
}

func (s *Store) CompareAndDeleteCalendar(_ context.Context, ref caldav.CalendarRef, pre caldav.Preconditions) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cal, ok := s.cals[ref]
	if !ok {
		return caldav.ErrNotFound
	}
	rev := cal.rev
	if err := pre.Check(&rev); err != nil {
		return err
	}

	delete(s.cals, ref)
	return nil
}

func (s *Store) Shares(_ context.Context, ref caldav.CalendarRef) ([]caldav.Share, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cal, ok := s.cals[ref]
	if !ok {
		return nil, caldav.ErrNotFound
	}
	shares := make([]caldav.Share, 0, len(cal.viewers))
	for account := range cal.viewers {
		shares = append(shares, caldav.Share{Account: account, Permissions: caldav.ViewOnlyPermissions()})
	}
	slices.SortFunc(shares, func(a, b caldav.Share) int {
		return cmp.Compare(a.Account, b.Account)
	})
	return shares, nil
}

// Share grants an account read access to a calendar. Sharing is an application
// decision, so there is no protocol path to it; this is the equivalent of the
// button in your own UI.
func (s *Store) Share(ref caldav.CalendarRef, account caldav.AccountID) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cal, ok := s.cals[ref]
	if !ok {
		return caldav.ErrNotFound
	}
	cal.viewers[account] = true
	return nil
}

// PruneHistory discards change records below a revision. Afterwards a client
// syncing from an earlier position is told its position is stale, which is the
// only honest answer: the changes it missed are gone.
func (s *Store) PruneHistory(_ context.Context, ref caldav.CalendarRef, before caldav.Revision) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cal, ok := s.cals[ref]
	if !ok {
		return caldav.ErrNotFound
	}
	cal.log = slices.DeleteFunc(cal.log, func(c change) bool { return c.rev < before })
	cal.pruned = max(cal.pruned, before)
	return nil
}

func (c *calendar) bump() {
	c.rev++
	c.settings.Revision = c.rev
}

func currentRevision(item caldav.Item, exists bool) *caldav.Revision {
	if !exists {
		return nil
	}
	rev := item.Revision
	return &rev
}

// copyCalendar deep-copies the fields a caller could otherwise write through.
// Calendar.SortOrder is an exported pointer, so returning the struct by value
// still hands out the pointer: a caller could change stored state with no
// write, no precondition check and no revision bump.
func copyCalendar(cal *caldav.Calendar) caldav.Calendar {
	copied := *cal
	if copied.SortOrder != nil {
		order := *copied.SortOrder
		copied.SortOrder = &order
	}
	return copied
}

func copyItem(item caldav.Item) caldav.Item {
	item.Content = slices.Clone(item.Content)
	return item
}

func set[T any](dst, src *T) {
	if src != nil {
		*dst = *src
	}
}

//nolint:gocritic // hugeParam: this signature is caldav.ItemTransferer's, and the request travels by value for the same reason as CompareAndStoreItem's.
func (s *Store) CompareAndCopyItem(_ context.Context, src, dst caldav.ItemRef, req caldav.TransferItemRequest) (caldav.StoreItemResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transfer(src, dst, req, false)
}

//nolint:gocritic // hugeParam: this signature is caldav.ItemTransferer's, and the request travels by value for the same reason as CompareAndStoreItem's.
func (s *Store) CompareAndMoveItem(_ context.Context, src, dst caldav.ItemRef, req caldav.TransferItemRequest) (caldav.StoreItemResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transfer(src, dst, req, true)
}

// transfer is the whole of a copy or a move. It runs with the store already
// locked, which is the point: both calendars change together or neither does.
//
//nolint:gocritic // hugeParam: req is passed on from the interface signature unchanged.
func (s *Store) transfer(src, dst caldav.ItemRef, req caldav.TransferItemRequest, removeSource bool) (caldav.StoreItemResult, error) {
	from, ok := s.cals[src.Calendar]
	if !ok {
		return caldav.StoreItemResult{}, caldav.ErrParentNotFound
	}
	to, ok := s.cals[dst.Calendar]
	if !ok {
		return caldav.StoreItemResult{}, caldav.ErrParentNotFound
	}

	item, exists := from.items[src.Item.String()]
	if err := req.Source.Check(currentRevision(item, exists)); err != nil {
		return caldav.StoreItemResult{}, err
	}
	if !exists {
		return caldav.StoreItemResult{}, caldav.ErrNotFound
	}

	existing, occupied := to.items[dst.Item.String()]
	if occupied && !req.MayReplaceDestination {
		return caldav.StoreItemResult{}, caldav.ErrForbidden
	}
	if !occupied && !req.MayCreateDestination {
		return caldav.StoreItemResult{}, caldav.ErrForbidden
	}
	if err := req.Destination.Check(currentRevision(existing, occupied)); err != nil {
		return caldav.StoreItemResult{}, err
	}

	if src == dst {
		return caldav.StoreItemResult{Revision: item.Revision}, nil
	}

	if owner, taken := to.byID[item.ContentID]; taken && owner != dst.Item {
		// A move gives the source's identifier up, so renaming inside one
		// calendar is not a collision with itself. A copy keeps it, so it is.
		if !removeSource || from != to || owner != src.Item {
			return caldav.StoreItemResult{}, &caldav.DuplicateContentIDError{Existing: owner}
		}
	}

	if occupied {
		delete(to.byID, existing.ContentID)
	}
	if removeSource {
		delete(from.items, src.Item.String())
		delete(from.byID, item.ContentID)
		from.bump()
		from.log = append(from.log, change{rev: from.rev, item: src.Item, deleted: true})
	}

	to.bump()
	to.items[dst.Item.String()] = caldav.Item{
		Name:      dst.Item,
		ContentID: item.ContentID,
		Content:   slices.Clone(item.Content),
		Revision:  to.rev,
	}
	to.byID[item.ContentID] = dst.Item
	to.log = append(to.log, change{rev: to.rev, item: dst.Item})

	return caldav.StoreItemResult{Revision: to.rev, Created: !occupied}, nil
}

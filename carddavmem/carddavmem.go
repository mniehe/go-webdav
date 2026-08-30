// Package carddavmem is a reference carddav backend that keeps everything in
// memory.
//
// It exists to be read: every one of the four obligations on carddav.Backend
// shows up here in the smallest form that satisfies it. There is no CardDAV
// conformance suite yet — what exercises this package is the handler test
// suite that runs against it, so read it as a faithful transcription of
// caldavmem (which the caldavtest suite does prove), not as independently
// proven.
//
// It is also a usable test fixture for a server's own handler tests.
//
// What it is not is a store. Nothing is persisted, nothing is indexed, every
// listing walks the address book, and one mutex serialises the lot. Content
// validation is not here either: the library parses and checks a body before
// any of these methods sees it.
package carddavmem

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"sync"

	carddav "github.com/mniehe/davkit/carddav"
)

// Store is an in-memory set of address books.
type Store struct {
	mu   sync.RWMutex
	cals map[carddav.AddressBookRef]*addressBook

	// issued counts every address book ever created here, so an ID is never reused
	// even by a address book recreated under a name that has been used before. A
	// fresh Store is a fresh universe; a persistent one would need a sequence
	// that survives restarts.
	issued uint64
}

type addressBook struct {
	settings carddav.AddressBook
	items    map[string]carddav.Item
	byID     map[string]carddav.Segment
	viewers  map[carddav.AccountID]bool

	rev carddav.Revision
	log []change

	// pruned is the revision below which history is gone. A client asking from
	// earlier than this cannot be answered with a batch, only with
	// ErrHistoryTooOld.
	pruned carddav.Revision
}

type change struct {
	rev     carddav.Revision
	item    carddav.Segment
	deleted bool
}

// New returns an empty store.
func New() *Store {
	return &Store{cals: map[carddav.AddressBookRef]*addressBook{}}
}

// Interfaces this backend implements. A compile error here is the whole point:
// the conformance suite skips what a backend does not implement, so a method
// signature drifting out of the interface must fail the build rather than
// quietly shrink the suite.
var (
	_ carddav.Backend        = (*Store)(nil)
	_ carddav.ItemWriter     = (*Store)(nil)
	_ carddav.SyncBackend    = (*Store)(nil)
	_ carddav.BookCreator    = (*Store)(nil)
	_ carddav.BookUpdater    = (*Store)(nil)
	_ carddav.BookDeleter    = (*Store)(nil)
	_ carddav.SharingBackend = (*Store)(nil)
	_ carddav.ItemTransferer = (*Store)(nil)
)

func (s *Store) AddressBookPermissions(_ context.Context, actor carddav.Actor, ref carddav.AddressBookRef) (carddav.AddressBookPermissions, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cal, ok := s.cals[ref]
	if !ok {
		return carddav.AddressBookPermissions{}, nil
	}
	switch {
	case actor.Account == ref.Account:
		return carddav.OwnerPermissions(), nil
	case cal.viewers[actor.Account]:
		return carddav.ViewOnlyPermissions(), nil
	default:
		return carddav.AddressBookPermissions{}, nil
	}
}

func (s *Store) AccountPermissions(_ context.Context, actor carddav.Actor, account carddav.AccountID) (carddav.AccountPermissions, error) {
	if actor.Account != account {
		return carddav.AccountPermissions{}, nil
	}
	return carddav.AccountPermissions{ListBooks: true, CreateBooks: true}, nil
}

func (s *Store) ListAddressBooks(_ context.Context, account carddav.AccountID) ([]carddav.AddressBook, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cals := make([]carddav.AddressBook, 0, len(s.cals))
	for ref, cal := range s.cals {
		if ref.Account == account {
			cals = append(cals, cal.settings)
		}
	}
	slices.SortFunc(cals, func(a, b carddav.AddressBook) int {
		return cmp.Compare(a.Name.String(), b.Name.String())
	})
	return cals, nil
}

func (s *Store) GetAddressBook(_ context.Context, ref carddav.AddressBookRef) (carddav.AddressBook, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cal, ok := s.cals[ref]
	if !ok {
		return carddav.AddressBook{}, carddav.ErrNotFound
	}
	return cal.settings, nil
}

func (s *Store) GetItem(_ context.Context, ref carddav.ItemRef) (carddav.Item, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cal, ok := s.cals[ref.Book]
	if !ok {
		return carddav.Item{}, carddav.ErrParentNotFound
	}
	item, ok := cal.items[ref.Item.String()]
	if !ok {
		return carddav.Item{}, carddav.ErrNotFound
	}
	return copyItem(item), nil
}

func (s *Store) ListItems(_ context.Context, ref carddav.AddressBookRef, yield func(carddav.Item) bool) (carddav.Revision, error) {
	// The read lock spans the iteration and the revision read, so they describe
	// the same state. A SQL backend uses one transaction the same way.
	s.mu.RLock()
	defer s.mu.RUnlock()

	cal, ok := s.cals[ref]
	if !ok {
		return 0, carddav.ErrNotFound
	}
	for _, item := range cal.items {
		if !yield(copyItem(item)) {
			break
		}
	}
	return cal.rev, nil
}

//nolint:gocritic // hugeParam: this signature is carddav.ItemWriter's, and a request travels by value so a backend cannot mutate what the handler still holds.
func (s *Store) CompareAndStoreItem(_ context.Context, ref carddav.ItemRef, req carddav.StoreItemRequest) (carddav.StoreItemResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cal, ok := s.cals[ref.Book]
	if !ok {
		return carddav.StoreItemResult{}, carddav.ErrParentNotFound
	}

	key := ref.Item.String()
	existing, exists := cal.items[key]

	// Which permission applies is only knowable now, with the target read.
	if exists && !req.MayReplace {
		return carddav.StoreItemResult{}, carddav.ErrForbidden
	}
	if !exists && !req.MayCreate {
		return carddav.StoreItemResult{}, carddav.ErrForbidden
	}

	if err := req.Preconditions.Check(currentRevision(existing, exists)); err != nil {
		return carddav.StoreItemResult{}, err
	}

	if owner, taken := cal.byID[req.ContentID]; taken && owner != ref.Item {
		return carddav.StoreItemResult{}, &carddav.DuplicateContentIDError{Existing: owner}
	}
	if exists {
		delete(cal.byID, existing.ContentID)
	}

	cal.bump()
	cal.items[key] = carddav.Item{
		Name:      ref.Item,
		ContentID: req.ContentID,
		Content:   slices.Clone(req.Content),
		Revision:  cal.rev,
	}
	cal.byID[req.ContentID] = ref.Item
	cal.log = append(cal.log, change{rev: cal.rev, item: ref.Item})

	return carddav.StoreItemResult{Revision: cal.rev, Created: !exists}, nil
}

func (s *Store) CompareAndDeleteItem(_ context.Context, ref carddav.ItemRef, pre carddav.Preconditions) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cal, ok := s.cals[ref.Book]
	if !ok {
		return carddav.ErrParentNotFound
	}

	existing, exists := cal.items[ref.Item.String()]
	if err := pre.Check(currentRevision(existing, exists)); err != nil {
		return err
	}
	if !exists {
		return carddav.ErrNotFound
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

func (s *Store) ListChanges(_ context.Context, ref carddav.AddressBookRef, after carddav.Revision, maxChanges int) (carddav.ChangeBatch, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cal, ok := s.cals[ref]
	if !ok {
		return carddav.ChangeBatch{}, carddav.ErrNotFound
	}
	if after < cal.pruned || after > cal.rev {
		return carddav.ChangeBatch{}, carddav.ErrHistoryTooOld
	}

	batch := carddav.ChangeBatch{CoveredThrough: cal.rev}
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
	at := map[carddav.Segment]int{}
	for _, c := range picked {
		if idx, seen := at[c.item]; seen {
			batch.Changes[idx].Deleted = c.deleted
			continue
		}
		at[c.item] = len(batch.Changes)
		batch.Changes = append(batch.Changes, carddav.Change{Item: c.item, Deleted: c.deleted})
	}
	return batch, nil
}

//nolint:gocritic // hugeParam: this signature is carddav.BookCreator's, and the request travels by value for the same reason as CompareAndStoreItem's.
func (s *Store) CompareAndCreateAddressBook(_ context.Context, account carddav.AccountID, req carddav.CreateAddressBookRequest, pre carddav.Preconditions) (carddav.AddressBook, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ref := carddav.AddressBookRef{Account: account, Book: req.Name}
	existing, exists := s.cals[ref]

	var current *carddav.Revision
	if exists {
		rev := existing.rev
		current = &rev
	}
	if err := pre.Check(current); err != nil {
		return carddav.AddressBook{}, err
	}
	if exists {
		return carddav.AddressBook{}, carddav.ErrAlreadyExists
	}

	s.issued++
	cal := &addressBook{
		settings: carddav.AddressBook{
			ID:          carddav.BookID(fmt.Sprintf("carddavmem-%d", s.issued)),
			Name:        req.Name,
			DisplayName: req.DisplayName,
			Description: req.Description,
		},
		items:   map[string]carddav.Item{},
		byID:    map[string]carddav.Segment{},
		viewers: map[carddav.AccountID]bool{},
	}
	cal.bump()
	s.cals[ref] = cal
	return cal.settings, nil
}

func (s *Store) CompareAndUpdateAddressBook(_ context.Context, ref carddav.AddressBookRef, patch carddav.AddressBookPatch, pre carddav.Preconditions) (carddav.AddressBook, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cal, ok := s.cals[ref]
	if !ok {
		return carddav.AddressBook{}, carddav.ErrNotFound
	}
	rev := cal.rev
	if err := pre.Check(&rev); err != nil {
		return carddav.AddressBook{}, err
	}

	set(&cal.settings.DisplayName, patch.DisplayName)
	set(&cal.settings.Description, patch.Description)

	cal.bump()
	return cal.settings, nil
}

func (s *Store) CompareAndDeleteAddressBook(_ context.Context, ref carddav.AddressBookRef, pre carddav.Preconditions) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cal, ok := s.cals[ref]
	if !ok {
		return carddav.ErrNotFound
	}
	rev := cal.rev
	if err := pre.Check(&rev); err != nil {
		return err
	}

	delete(s.cals, ref)
	return nil
}

func (s *Store) Shares(_ context.Context, ref carddav.AddressBookRef) ([]carddav.Share, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cal, ok := s.cals[ref]
	if !ok {
		return nil, carddav.ErrNotFound
	}
	shares := make([]carddav.Share, 0, len(cal.viewers))
	for account := range cal.viewers {
		shares = append(shares, carddav.Share{Account: account, Permissions: carddav.ViewOnlyPermissions()})
	}
	slices.SortFunc(shares, func(a, b carddav.Share) int {
		return cmp.Compare(a.Account, b.Account)
	})
	return shares, nil
}

// Share grants an account read access to a address book. Sharing is an application
// decision, so there is no protocol path to it; this is the equivalent of the
// button in your own UI.
func (s *Store) Share(ref carddav.AddressBookRef, account carddav.AccountID) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cal, ok := s.cals[ref]
	if !ok {
		return carddav.ErrNotFound
	}
	cal.viewers[account] = true
	return nil
}

// PruneHistory discards change records below a revision. Afterwards a client
// syncing from an earlier position is told its position is stale, which is the
// only honest answer: the changes it missed are gone.
func (s *Store) PruneHistory(_ context.Context, ref carddav.AddressBookRef, before carddav.Revision) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cal, ok := s.cals[ref]
	if !ok {
		return carddav.ErrNotFound
	}
	cal.log = slices.DeleteFunc(cal.log, func(c change) bool { return c.rev < before })
	cal.pruned = max(cal.pruned, before)
	return nil
}

func (c *addressBook) bump() {
	c.rev++
	c.settings.Revision = c.rev
}

func currentRevision(item carddav.Item, exists bool) *carddav.Revision {
	if !exists {
		return nil
	}
	rev := item.Revision
	return &rev
}

func copyItem(item carddav.Item) carddav.Item {
	item.Content = slices.Clone(item.Content)
	return item
}

func set[T any](dst, src *T) {
	if src != nil {
		*dst = *src
	}
}

//nolint:gocritic // hugeParam: this signature is carddav.ItemTransferer's, and the request travels by value for the same reason as CompareAndStoreItem's.
func (s *Store) CompareAndCopyItem(_ context.Context, src, dst carddav.ItemRef, req carddav.TransferItemRequest) (carddav.StoreItemResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transfer(src, dst, req, false)
}

//nolint:gocritic // hugeParam: this signature is carddav.ItemTransferer's, and the request travels by value for the same reason as CompareAndStoreItem's.
func (s *Store) CompareAndMoveItem(_ context.Context, src, dst carddav.ItemRef, req carddav.TransferItemRequest) (carddav.StoreItemResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transfer(src, dst, req, true)
}

// transfer is the whole of a copy or a move. It runs with the store already
// locked, which is the point: both address books change together or neither does.
//
//nolint:gocritic // hugeParam: req is passed on from the interface signature unchanged.
func (s *Store) transfer(src, dst carddav.ItemRef, req carddav.TransferItemRequest, removeSource bool) (carddav.StoreItemResult, error) {
	from, ok := s.cals[src.Book]
	if !ok {
		return carddav.StoreItemResult{}, carddav.ErrParentNotFound
	}
	to, ok := s.cals[dst.Book]
	if !ok {
		return carddav.StoreItemResult{}, carddav.ErrParentNotFound
	}

	item, exists := from.items[src.Item.String()]
	if err := req.Source.Check(currentRevision(item, exists)); err != nil {
		return carddav.StoreItemResult{}, err
	}
	if !exists {
		return carddav.StoreItemResult{}, carddav.ErrNotFound
	}

	existing, occupied := to.items[dst.Item.String()]
	if occupied && !req.MayReplaceDestination {
		return carddav.StoreItemResult{}, carddav.ErrForbidden
	}
	if !occupied && !req.MayCreateDestination {
		return carddav.StoreItemResult{}, carddav.ErrForbidden
	}
	if err := req.Destination.Check(currentRevision(existing, occupied)); err != nil {
		return carddav.StoreItemResult{}, err
	}

	if src == dst {
		return carddav.StoreItemResult{Revision: item.Revision}, nil
	}

	if owner, taken := to.byID[item.ContentID]; taken && owner != dst.Item {
		// A move gives the source's identifier up, so renaming inside one
		// address book is not a collision with itself. A copy keeps it, so it is.
		if !removeSource || from != to || owner != src.Item {
			return carddav.StoreItemResult{}, &carddav.DuplicateContentIDError{Existing: owner}
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
	to.items[dst.Item.String()] = carddav.Item{
		Name:      dst.Item,
		ContentID: item.ContentID,
		Content:   slices.Clone(item.Content),
		Revision:  to.rev,
	}
	to.byID[item.ContentID] = dst.Item
	to.log = append(to.log, change{rev: to.rev, item: dst.Item})

	return carddav.StoreItemResult{Revision: to.rev, Created: !occupied}, nil
}

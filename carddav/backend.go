package carddav

import "context"

// Backend is the whole of a read-only contacts server.
//
// Everything beyond reading is an optional interface a Backend may also
// implement: ItemWriter, SyncBackend, BookCreator, BookUpdater, BookDeleter,
// SharingBackend, ItemTransferer. The handler type-asserts for each and
// declines the operations it cannot serve, so a backend never has to stub out
// something it does not do.
//
// Four obligations run through all of it, because only storage can meet them
// atomically:
//
//  1. Store the exact bytes you were given, and return them unchanged.
//  2. Keep ContentID unique within an address book, retiring the old one when
//     you replace an item.
//  3. Compare and mutate in one transaction — the permission choice, the
//     precondition check and the write together. Note what this does and does
//     not promise: the two permissions are computed before the call and only
//     the choice between them is transactional, so a share revoked while a
//     request is in flight does not cancel it.
//  4. Append a durable change record in that same transaction, if you implement
//     SyncBackend.
type Backend interface {
	Authorizer

	// ListAddressBooks returns the account's own address books. Books shared
	// with the account belong to whoever owns them and are not listed here.
	ListAddressBooks(ctx context.Context, account AccountID) ([]AddressBook, error)

	// GetAddressBook returns one address book, or ErrNotFound.
	GetAddressBook(ctx context.Context, ref AddressBookRef) (AddressBook, error)

	// GetItem returns one item, or ErrNotFound. ErrParentNotFound distinguishes
	// a missing address book from a missing item within one.
	GetItem(ctx context.Context, ref ItemRef) (Item, error)

	// ListItems yields every item in the address book, in one consistent read.
	// Call yield inside your transaction and stop when it returns false.
	//
	// The returned revision must describe exactly what was yielded — read it in
	// that same transaction, not before or after. A revision read afterwards
	// can cover a write the listing did not include, and a client that stores
	// it as its sync position never sees that write again.
	ListItems(ctx context.Context, ref AddressBookRef, yield func(Item) bool) (Revision, error)
}

// StoreItemRequest is a write, with everything the library extracted from the
// content already pulled out.
type StoreItemRequest struct {
	Content   []byte
	ContentID string // extracted by the library

	Preconditions Preconditions

	// MayCreate and MayReplace are this actor's two permissions for this
	// request. Which one applies depends on whether the target exists, which is
	// only knowable inside your transaction — so the library computes both and
	// you select. Refuse with ErrForbidden when the applicable one is false.
	MayCreate  bool
	MayReplace bool
}

// StoreItemResult is what a write produced.
type StoreItemResult struct {
	Revision Revision
	Created  bool // false means replaced
}

// ItemWriter stores and deletes items. Each method does the whole sequence in
// one transaction: select the applicable permission, check preconditions,
// enforce ContentID uniqueness, mutate, record the change.
type ItemWriter interface {
	// CompareAndStoreItem creates or replaces one item.
	//
	// Replacing retires the existing item's ContentID: you have that item in
	// hand, so existing.ContentID is the entry to remove before claiming the
	// new one. Return a *DuplicateContentIDError when the new one is already
	// another item's, ErrParentNotFound when the address book does not exist.
	CompareAndStoreItem(ctx context.Context, ref ItemRef, req StoreItemRequest) (StoreItemResult, error)

	// CompareAndDeleteItem removes one item, or returns ErrNotFound.
	CompareAndDeleteItem(ctx context.Context, ref ItemRef, pre Preconditions) error
}

// Change is one item's fate over an interval.
type Change struct {
	Item    Segment
	Deleted bool
}

// ChangeBatch is what happened over an interval of an address book's history.
type ChangeBatch struct {
	// Changes reports each item at most once, in its final state for the
	// interval covered. An item created and then deleted within the interval is
	// reported deleted, once.
	Changes []Change

	// CoveredThrough is the revision this batch completely covers: every change
	// after the requested revision and up to this one is included. It is what
	// makes a truncated batch resumable, so it must describe what was actually
	// returned rather than the address book's current revision.
	CoveredThrough Revision

	HasMore bool
}

// SyncBackend reports incremental change. Without it the handler does not
// advertise incremental sync, and clients refetch the whole address book on
// every poll.
//
// A change log cannot be reconstructed from current state: a deletion leaves
// nothing behind to notice. That is why the record has to be appended in the
// same transaction as the mutation, and why this is the one obligation with no
// fallback.
type SyncBackend interface {
	// ListChanges returns what happened after a revision, oldest first.
	//
	// maxChanges is a hint, not a limit: never split a revision across batches
	// to honour it. If one revision holds more changes than that, return them
	// all — the library handles the oversized batch.
	//
	// Return ErrHistoryTooOld when your history no longer reaches back that
	// far. The library tells the client its position is stale, and the client
	// starts over from a full listing on its next request. Prune history
	// whenever you like, as long as revisions older than the pruned point start
	// returning that error rather than a batch that silently omits them.
	ListChanges(ctx context.Context, ref AddressBookRef, after Revision, maxChanges int) (ChangeBatch, error)
}

// CreateAddressBookRequest is a new address book.
type CreateAddressBookRequest struct {
	Name        Segment
	DisplayName string
	Description string
}

// AddressBookPatch distinguishes "unchanged" from "set to empty": a nil field
// is untouched. It cannot change Name — that would move the address book.
type AddressBookPatch struct {
	DisplayName *string
	Description *string
}

// BookCreator makes new address books.
type BookCreator interface {
	// CompareAndCreateAddressBook creates one address book, in one transaction.
	//
	// Return ErrAlreadyExists when the account already has an address book of
	// that name. That is an ordinary answer with its own protocol response, not
	// a failure, and a handler cannot find it out safely by asking first: two
	// concurrent creations would both see nothing there.
	//
	// The new address book needs a BookID that has never been used before. See
	// BookID for why reusing one is a data-loss bug rather than a tidiness one.
	CompareAndCreateAddressBook(ctx context.Context, account AccountID, req CreateAddressBookRequest, pre Preconditions) (AddressBook, error)
}

// BookUpdater changes an address book's settings.
type BookUpdater interface {
	CompareAndUpdateAddressBook(ctx context.Context, ref AddressBookRef, patch AddressBookPatch, pre Preconditions) (AddressBook, error)
}

// BookDeleter removes an address book and everything in it.
type BookDeleter interface {
	CompareAndDeleteAddressBook(ctx context.Context, ref AddressBookRef, pre Preconditions) error
}

// Share describes one grant, for clients that display who an address book is
// shared with. Read-only: sharing is granted in your application, not over the
// wire.
type Share struct {
	Account     AccountID
	Permissions AddressBookPermissions
}

// SharingBackend reports who an address book is shared with.
type SharingBackend interface {
	Shares(ctx context.Context, ref AddressBookRef) ([]Share, error)
}

// TransferItemRequest is a copy or a move, from the destination's point of
// view. Both ends may be in different address books, and those books may belong
// to different accounts.
type TransferItemRequest struct {
	// Source is what the client expects to find where the item is coming from,
	// and Destination what it expects where the item is going. A conditional
	// request guards the source; an Overwrite refusal guards the destination.
	Source      Preconditions
	Destination Preconditions

	// MayCreateDestination and MayReplaceDestination are the actor's two
	// permissions at the destination, for the same reason StoreItemRequest
	// carries two: which applies depends on whether the destination exists, and
	// only the transaction knows that. Everything else — that the actor may read
	// the source, and on a move remove it — the handler settles before calling,
	// because none of it depends on state the transaction discovers.
	MayCreateDestination  bool
	MayReplaceDestination bool
}

// ItemTransferer moves and copies items. Without it the handler answers 405 to
// COPY and MOVE, and clients fall back to fetching, storing and deleting —
// three requests a client can be interrupted between, leaving the item in both
// places or neither.
//
// This cannot be assembled out of the other methods. GetItem, then
// CompareAndStoreItem, then CompareAndDeleteItem is three transactions: the
// source can change under you after the read, the destination can commit while
// the source deletion fails, and a move between address books has to advance
// two revision streams and append to two change logs at once. Duplicating an
// item silently is one of the worst things this library can do, so the
// operation is either atomic in storage or it is not offered.
type ItemTransferer interface {
	// CompareAndCopyItem writes the source's bytes to the destination, leaving
	// the source alone.
	//
	// The copy is a new item at the destination and takes its own ContentID
	// uniqueness check there. Within one address book that check always fails,
	// since the source already holds the ID — which is correct: an address book
	// cannot hold the same contact twice.
	CompareAndCopyItem(ctx context.Context, src, dst ItemRef, req TransferItemRequest) (StoreItemResult, error)

	// CompareAndMoveItem writes the source's bytes to the destination and
	// removes the source, retiring its ContentID, in one transaction. Both
	// address books record the change.
	//
	// A move onto the item's own reference is a no-op rather than a deletion.
	CompareAndMoveItem(ctx context.Context, src, dst ItemRef, req TransferItemRequest) (StoreItemResult, error)
}

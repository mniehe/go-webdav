package carddav

import "context"

// AddressBookPermissions is what an actor may do with one address book. The
// zero value denies everything, so a case you forget to write is a case that is
// refused.
//
// Unlike a calendar there is no availability tier: a contact has no busy-time
// shadow, so an actor either sees the cards or sees nothing.
type AddressBookPermissions struct {
	ViewDetails    bool
	CreateItems    bool
	ReplaceItems   bool
	DeleteItems    bool
	UpdateSettings bool // apply any AddressBookPatch
	DeleteBook     bool
}

// Any reports whether the actor may do anything at all with the address book.
// An actor with none of these cannot be told the address book exists.
func (p AddressBookPermissions) Any() bool {
	return p != AddressBookPermissions{}
}

// ViewOnlyPermissions can read the address book and everything in it, and
// change nothing.
func ViewOnlyPermissions() AddressBookPermissions {
	return AddressBookPermissions{ViewDetails: true}
}

// EditPermissions can read and change the items, but not the address book
// itself.
func EditPermissions() AddressBookPermissions {
	return AddressBookPermissions{
		ViewDetails:  true,
		CreateItems:  true,
		ReplaceItems: true,
		DeleteItems:  true,
	}
}

// OwnerPermissions can do everything, including deleting the address book.
func OwnerPermissions() AddressBookPermissions {
	p := EditPermissions()
	p.UpdateSettings = true
	p.DeleteBook = true
	return p
}

// AccountPermissions is what an actor may do with an account's address book
// list. The zero value denies everything.
type AccountPermissions struct {
	ListBooks   bool
	CreateBooks bool
}

// Any reports whether the actor may do anything at all with the account.
func (p AccountPermissions) Any() bool {
	return p != AccountPermissions{}
}

// Authorizer answers what an actor may do. Both questions are in domain
// language; the library maps them to protocol privileges itself.
//
// An actor with no permission at all is not an error: return the zero value.
// Reserve errors for a lookup that failed, which becomes a 500.
type Authorizer interface {
	AddressBookPermissions(ctx context.Context, actor Actor, ref AddressBookRef) (AddressBookPermissions, error)
	AccountPermissions(ctx context.Context, actor Actor, account AccountID) (AccountPermissions, error)
}

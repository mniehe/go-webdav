package carddav

// Revision is a position in an address book's history. Any ordering works so
// long as it never decreases: a row counter, a logical clock, a transaction ID.
//
// An address book and each of its items carry the revision at which they last
// changed. That is the only version marker the library needs — everything
// clients cache is derived from it. Leave it zero only if you implement neither
// writing nor sync; the library then falls back to hashing content.
type Revision uint64

// BookID identifies one incarnation of an address book. Generate a new one
// every time an address book is created and never reuse one, not even for a
// book created under a name that has been used before.
//
// Deleting an address book and creating another at the same name produces a
// different address book, and a client still holding a validator or a sync
// position from the old one has to be told so. Revisions cannot say it, because
// a new book starts counting from the beginning again — which is what this is
// for. Within one incarnation, revisions need only never decrease.
//
// It is never shown to a client and never appears in a URL, so any unique value
// will do: a row ID, a UUID, a sequence.
type BookID string

// AddressBook identifies itself by the name it was requested under.
type AddressBook struct {
	ID          BookID  // unique to this address book, never reused
	Name        Segment // stable; part of the URL
	DisplayName string  // what people see
	Description string
	MaxItemSize int64 // zero means no limit
	Revision    Revision
}

// Item is one entry in an address book.
//
// Two things about it must be unique within the address book, and they are
// different: Name is where it lives, ContentID is what the content calls
// itself. The library extracts ContentID for you, so you never parse Content.
type Item struct {
	Name      Segment
	ContentID string
	Content   []byte // exactly what you were given; return it unchanged
	Revision  Revision
}

package caldav

// Revision is a position in a calendar's history. Any ordering works so long as
// it never decreases: a row counter, a logical clock, a transaction ID.
//
// A calendar and each of its items carry the revision at which they last
// changed. That is the only version marker the library needs — everything
// clients cache is derived from it. Leave it zero only if you implement neither
// writing nor sync; the library then falls back to hashing content.
type Revision uint64

// ItemKind is what an item is. The library maps these to the wire format.
type ItemKind uint8

const (
	Event        ItemKind = iota + 1 // an appointment
	Task                             // something to do, with an optional due date
	Note                             // a dated journal entry
	Availability                     // published busy times
)

// allItemKinds is every kind, in declaration order. Kinds reports in this
// order, so an advertised set is stable between requests.
var allItemKinds = [...]ItemKind{Event, Task, Note, Availability}

func (k ItemKind) String() string {
	switch k {
	case Event:
		return "event"
	case Task:
		return "task"
	case Note:
		return "note"
	case Availability:
		return "availability"
	default:
		return "unknown item kind"
	}
}

// IsValid reports whether k is one of the declared kinds.
func (k ItemKind) IsValid() bool { return k >= Event && k <= Availability }

// ItemKinds is the set of kinds a calendar accepts. The zero value accepts all
// of them, so a calendar you never configured takes anything.
type ItemKinds struct {
	// restricted separates "accepts everything" from "accepts nothing", which a
	// bare mask cannot: both would be zero.
	restricted bool
	mask       uint8
}

// AllItemKinds accepts every kind. It is the zero value.
func AllItemKinds() ItemKinds { return ItemKinds{} }

// OnlyItemKinds accepts exactly the kinds given. With no arguments it accepts
// nothing, which is a calendar no client can write to.
func OnlyItemKinds(kinds ...ItemKind) ItemKinds {
	k := ItemKinds{restricted: true}
	for _, kind := range kinds {
		if kind.IsValid() {
			k.mask |= 1 << kind
		}
	}
	return k
}

// Allows reports whether the set accepts kind.
func (k ItemKinds) Allows(kind ItemKind) bool {
	if !kind.IsValid() {
		return false
	}
	if !k.restricted {
		return true
	}
	return k.mask&(1<<kind) != 0
}

// Kinds lists the accepted kinds in declaration order.
func (k ItemKinds) Kinds() []ItemKind {
	kinds := make([]ItemKind, 0, len(allItemKinds))
	for _, kind := range allItemKinds {
		if k.Allows(kind) {
			kinds = append(kinds, kind)
		}
	}
	return kinds
}

// CalendarID identifies one incarnation of a calendar. Generate a new one every
// time a calendar is created and never reuse one, not even for a calendar
// created under a name that has been used before.
//
// Deleting a calendar and creating another at the same name produces a
// different calendar, and a client still holding a validator or a sync position
// from the old one has to be told so. Revisions cannot say it, because a new
// calendar starts counting from the beginning again — which is what this is
// for. Within one incarnation, revisions need only never decrease.
//
// It is never shown to a client and never appears in a URL, so any unique value
// will do: a row ID, a UUID, a sequence.
type CalendarID string

// Calendar identifies itself by the name it was requested under.
type Calendar struct {
	ID          CalendarID // unique to this calendar, never reused
	Name        Segment    // stable; part of the URL
	DisplayName string     // what people see
	Description string
	Color       string
	SortOrder   *int
	Timezone    Timezone
	Accepts     ItemKinds
	MaxItemSize int64 // zero means no limit
	Revision    Revision
}

// Item is one entry in a calendar.
//
// Two things about it must be unique within the calendar, and they are
// different: Name is where it lives, ContentID is what the content calls
// itself. The library extracts ContentID for you, so you never parse Content.
type Item struct {
	Name      Segment
	ContentID string
	Content   []byte // exactly what you were given; return it unchanged
	Revision  Revision
}

package caldav

import (
	"errors"
	"fmt"
)

// Sentinel errors a backend returns. Match them with errors.Is; anything else
// becomes a 500.
var (
	ErrNotFound           = errors.New("caldav: not found")
	ErrParentNotFound     = errors.New("caldav: parent not found")
	ErrForbidden          = errors.New("caldav: operation not permitted")
	ErrPreconditionFailed = errors.New("caldav: precondition failed")
	ErrHistoryTooOld      = errors.New("caldav: change history does not reach that revision")
	ErrUnauthorized       = errors.New("caldav: not authenticated")
	ErrAlreadyExists      = errors.New("caldav: already exists")
)

// DuplicateContentIDError reports that another item in the calendar already
// calls itself by the content ID being stored.
type DuplicateContentIDError struct {
	Existing Segment
}

func (e *DuplicateContentIDError) Error() string {
	return fmt.Sprintf("caldav: content ID already used by %q", e.Existing)
}

// InvalidContentError reports content that does not parse.
type InvalidContentError struct {
	Err error
}

func (e *InvalidContentError) Error() string {
	return fmt.Sprintf("caldav: invalid content: %v", e.Err)
}

func (e *InvalidContentError) Unwrap() error { return e.Err }

// UnsupportedKindError reports an item the calendar does not accept.
type UnsupportedKindError struct {
	Kind ItemKind
}

func (e *UnsupportedKindError) Error() string {
	return fmt.Sprintf("caldav: calendar does not accept a %s", e.Kind)
}

// QuotaExceededError reports a write that would take the calendar over a limit
// the backend enforces.
type QuotaExceededError struct {
	Limit int64
}

func (e *QuotaExceededError) Error() string {
	return fmt.Sprintf("caldav: quota of %d bytes exceeded", e.Limit)
}

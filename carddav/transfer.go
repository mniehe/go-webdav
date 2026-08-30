package carddav

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/mniehe/davkit/internal"
)

func (a *adapter) Copy(r *http.Request, dest *internal.Href, recursive, overwrite bool) (bool, error) {
	return a.transfer(r, dest, overwrite, false)
}

func (a *adapter) Move(r *http.Request, dest *internal.Href, overwrite bool) (bool, error) {
	return a.transfer(r, dest, overwrite, true)
}

// transfer serves COPY and MOVE over ItemTransferer. Both are item-to-item;
// copying a whole address book is not offered.
//
// The handler settles everything that does not depend on state only the
// transaction can see: reading the source (and on a move, removing it) against
// the source address book's permissions, and the pair of destination
// permissions the backend selects between. The source's conditional headers
// and the Overwrite refusal both travel as preconditions, checked inside the
// backend's transaction like every other compare-and-mutate.
func (a *adapter) transfer(r *http.Request, dest *internal.Href, overwrite, move bool) (bool, error) {
	acc, err := a.resolve(r)
	if err != nil {
		return false, err
	}
	if acc.Kind != KindItem || !a.caps.transfersItems {
		return false, errUnsupportedMethod(r.Method)
	}
	if !acc.book.ViewDetails {
		return false, denyOperation("read the items in this address book")
	}
	if move && !acc.book.DeleteItems {
		return false, denyOperation("move items out of this address book")
	}

	ctx := r.Context()
	dst, err := a.cfg.Routes.Parse(ctx, (*url.URL)(dest).EscapedPath())
	if err != nil || dst.Kind != KindItem {
		// RFC 4918 §9.8.5: a destination that cannot exist — no route, or not
		// an item's place — is a missing parent, 409.
		return false, internal.HTTPErrorf(http.StatusConflict, "carddav: Destination cannot hold an item")
	}

	dstPerms := acc.book
	if dst.AddressBookRef() != acc.AddressBookRef() {
		perms, permErr := a.backend.AddressBookPermissions(ctx, actorFrom(ctx), dst.AddressBookRef())
		if permErr != nil {
			return false, fmt.Errorf("carddav: reading destination permissions: %w", permErr)
		}
		if !perms.Any() {
			return false, a.denyUnknown()
		}
		dstPerms = perms
	}

	srcBook, err := a.getAddressBook(ctx, acc.AddressBookRef())
	if err != nil {
		return false, backendError(err)
	}
	source, err := preconditionsFrom(bookScope(srcBook.ID), r.Header.Get("If-Match"), r.Header.Get("If-None-Match"))
	if err != nil {
		return false, internal.HTTPErrorf(http.StatusBadRequest, "carddav: %v", err)
	}

	destination := Unconditional()
	if !overwrite {
		destination = IfTargetMissing()
	}
	req := TransferItemRequest{
		Source:                source,
		Destination:           destination,
		MayCreateDestination:  dstPerms.CreateItems,
		MayReplaceDestination: dstPerms.ReplaceItems,
	}

	transferer := a.backend.(ItemTransferer) //nolint:errcheck // caps.transfersItems was resolved from this assertion at construction
	var result StoreItemResult
	if move {
		result, err = transferer.CompareAndMoveItem(ctx, acc.ItemRef(), dst.ItemRef(), req)
	} else {
		result, err = transferer.CompareAndCopyItem(ctx, acc.ItemRef(), dst.ItemRef(), req)
	}
	if err != nil {
		var dup *DuplicateContentIDError
		if errors.As(err, &dup) {
			return false, internal.NewPreconditionError(http.StatusConflict, noUIDConflictName)
		}
		// The source address book was resolved above, so a missing parent can
		// only be the destination's — RFC 4918 §9.8.5 answers that with 409.
		if errors.Is(err, ErrParentNotFound) {
			return false, internal.HTTPErrorf(http.StatusConflict, "carddav: the Destination address book does not exist")
		}
		return false, backendError(err)
	}
	return result.Created, nil
}

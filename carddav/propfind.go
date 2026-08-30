package carddav

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"

	"github.com/mniehe/davkit/internal"
)

func (a *adapter) PropFind(r *http.Request, pf *internal.PropFind, depth internal.Depth, emit func(*internal.Response) error) error {
	acc, err := a.resolve(r)
	if err != nil {
		return err
	}

	ctx := r.Context()
	switch acc.Kind {
	case KindAccount:
		return a.propFindAccount(ctx, acc, pf, depth, emit)
	case KindAddressBook:
		return a.propFindBook(ctx, acc, pf, depth, emit)
	default:
		return a.propFindItem(ctx, acc, pf, emit)
	}
}

func (a *adapter) propFindAccount(ctx context.Context, acc access, pf *internal.PropFind, depth internal.Depth, emit func(*internal.Response) error) error {
	resp, err := a.accountResponse(ctx, acc.Account, pf)
	if err != nil {
		return err
	}

	// The membership is resolved before the account's own row goes out: once
	// anything is emitted the document has started, and a refusal or a backend
	// contract violation could no longer fail the request outright.
	var books []AddressBook
	if depth != internal.DepthZero {
		if !acc.account.ListBooks {
			return denyOperation("list this account's address books")
		}
		if books, err = a.listAddressBooks(ctx, acc.Account); err != nil {
			return backendError(err)
		}
	}

	if writeErr := emit(resp); writeErr != nil {
		return writeErr
	}
	actor := actorFrom(ctx)
	for i := range books {
		ref := AddressBookRef{Account: acc.Account, Book: books[i].Name}
		perms, err := a.backend.AddressBookPermissions(ctx, actor, ref)
		if err != nil {
			return fmt.Errorf("carddav: reading address book permissions: %w", err)
		}
		// A grant over the account's book list is not a grant over each book
		// in it, so a member the actor cannot see is left out rather than
		// reported. Omission is the only concealment a multistatus has.
		if !perms.Any() {
			continue
		}
		resp, err := a.bookResponse(ctx, ref, &books[i], perms, pf)
		if err != nil {
			return err
		}
		if err := emit(resp); err != nil {
			return err
		}
	}
	return nil
}

func (a *adapter) propFindBook(ctx context.Context, acc access, pf *internal.PropFind, depth internal.Depth, emit func(*internal.Response) error) error {
	ref := acc.AddressBookRef()
	book, err := a.getAddressBook(ctx, ref)
	if err != nil {
		return backendError(err)
	}

	resp, err := a.bookResponse(ctx, ref, &book, acc.book, pf)
	if err != nil {
		return err
	}

	// Membership before the address book's own row, for the same reason as the
	// account listing: nothing may be emitted until the whole answer can be.
	var items []Item
	if depth != internal.DepthZero {
		if !acc.book.ViewDetails {
			return denyOperation("list the items in this address book")
		}
		if items, _, err = a.listItems(ctx, ref); err != nil {
			return backendError(err)
		}
	}

	if writeErr := emit(resp); writeErr != nil {
		return writeErr
	}
	scope := bookScope(book.ID)
	for i := range items {
		resp, err := a.itemResponse(ctx, ItemRef{Book: ref, Item: items[i].Name}, items[i], scope, pf)
		if err != nil {
			return err
		}
		if err := emit(resp); err != nil {
			return err
		}
	}
	return nil
}

func (a *adapter) propFindItem(ctx context.Context, acc access, pf *internal.PropFind, emit func(*internal.Response) error) error {
	if !acc.book.ViewDetails {
		return denyOperation("read the items in this address book")
	}

	ref := acc.ItemRef()
	item, err := a.getItem(ctx, ref)
	if err != nil {
		return backendError(err)
	}
	book, err := a.getAddressBook(ctx, ref.Book)
	if err != nil {
		return backendError(err)
	}

	resp, err := a.itemResponse(ctx, ref, item, bookScope(book.ID), pf)
	if err != nil {
		return err
	}
	return emit(resp)
}

func (a *adapter) accountResponse(ctx context.Context, account AccountID, pf *internal.PropFind) (*internal.Response, error) {
	self, err := a.cfg.Routes.Href(ctx, AccountResource(account))
	if err != nil {
		return nil, fmt.Errorf("carddav: rendering account href: %w", err)
	}
	principal, err := a.principalHref(ctx)
	if err != nil {
		return nil, err
	}

	// The account is both the principal and its address book home. Discovery
	// starts at current-user-principal, reads addressbook-home-set from it, and
	// enumerates that collection — so all three have to agree, and here they
	// are one URL.
	props := map[xml.Name]internal.PropFindFunc{
		internal.ResourceTypeName: internal.PropFindValue(
			internal.NewResourceType(internal.CollectionName, internal.PrincipalName)),
		internal.DisplayNameName:          internal.PropFindValue(&internal.DisplayName{Name: string(account)}),
		internal.PrincipalURLName:         internal.PropFindValue(&internal.PrincipalURL{Href: internal.Href{Path: self}}),
		internal.CurrentUserPrincipalName: internal.PropFindValue(&internal.CurrentUserPrincipal{Href: internal.Href{Path: principal}}),
		addressBookHomeSetName:            internal.PropFindValue(&addressBookHomeSet{Href: internal.Href{Path: self}}),
	}
	return internal.NewPropFindResponse(self, pf, props)
}

func (a *adapter) bookResponse(ctx context.Context, ref AddressBookRef, book *AddressBook, perms AddressBookPermissions, pf *internal.PropFind) (*internal.Response, error) {
	href, err := a.cfg.Routes.Href(ctx, AddressBookResource(ref))
	if err != nil {
		return nil, fmt.Errorf("carddav: rendering address book href: %w", err)
	}
	owner, err := a.cfg.Routes.Href(ctx, AccountResource(ref.Account))
	if err != nil {
		return nil, fmt.Errorf("carddav: rendering owner href: %w", err)
	}
	principal, err := a.principalHref(ctx)
	if err != nil {
		return nil, err
	}

	props := map[xml.Name]internal.PropFindFunc{
		internal.ResourceTypeName: internal.PropFindValue(
			internal.NewResourceType(internal.CollectionName, addressbookName)),
		internal.DisplayNameName:             internal.PropFindValue(&internal.DisplayName{Name: book.DisplayName}),
		internal.CurrentUserPrincipalName:    internal.PropFindValue(&internal.CurrentUserPrincipal{Href: internal.Href{Path: principal}}),
		internal.OwnerName:                   internal.PropFindValue(&internal.Owner{Href: &internal.Href{Path: owner}}),
		internal.SupportedPrivilegeSetName:   internal.PropFindValue(internal.NewSupportedPrivilegeSet()),
		internal.CurrentUserPrivilegeSetName: internal.PropFindValue(privilegeSet(privilegesFor(perms, a.caps))),
		internal.SupportedReportSetName:      internal.PropFindValue(internal.NewSupportedReportSet(a.supportedReports()...)),
		// RFC 6352 §6.2.2: both vCard versions go-vcard parses and encodes.
		supportedAddressDataName: internal.PropFindValue(&supportedAddressData{
			Types: []addressDataType{
				{ContentType: "text/vcard", Version: "3.0"},
				{ContentType: "text/vcard", Version: "4.0"},
			},
		}),
	}

	if book.Description != "" {
		props[addressBookDescriptionName] = internal.PropFindValue(&addressBookDescription{Description: book.Description})
	}
	if book.MaxItemSize > 0 {
		props[maxResourceSizeName] = internal.PropFindValue(&maxResourceSize{Size: book.MaxItemSize})
	}
	// Both properties answer the same question — has anything in here changed —
	// and both are a client's sync position, so they are only meaningful when
	// the backend can serve the delta they lead to.
	if a.caps.syncs {
		token := syncTokenFor(bookScope(book.ID), book.Revision)
		props[internal.SyncTokenName] = internal.PropFindValue(&internal.SyncToken{Token: token})
		props[internal.GetCTagName] = internal.PropFindValue(&internal.GetCTag{CTag: token})
	}

	// RFC 6578 §4 keeps sync-token out of allprop. The rest are kept out for the
	// same reason it is: each is a fixed tree or a server-wide constant that says
	// nothing about this collection, repeated once per member of a listing.
	internal.RemoveFromAllProp(pf, props,
		internal.SyncTokenName,
		internal.SupportedPrivilegeSetName,
		internal.CurrentUserPrivilegeSetName,
		internal.OwnerName,
		maxResourceSizeName,
	)
	return internal.NewPropFindResponse(href, pf, props)
}

func (a *adapter) itemResponse(ctx context.Context, ref ItemRef, item Item, scope uint64, pf *internal.PropFind) (*internal.Response, error) {
	href, err := a.cfg.Routes.Href(ctx, ItemResource(ref))
	if err != nil {
		return nil, fmt.Errorf("carddav: rendering item href: %w", err)
	}

	props := map[xml.Name]internal.PropFindFunc{
		internal.ResourceTypeName:     internal.PropFindValue(internal.NewResourceType()),
		internal.GetETagName:          internal.PropFindValue(&internal.GetETag{ETag: etagFor(scope, item)}),
		internal.GetContentTypeName:   internal.PropFindValue(&internal.GetContentType{Type: vcardMediaType}),
		internal.GetContentLengthName: internal.PropFindValue(&internal.GetContentLength{Length: int64(len(item.Content))}),
		addressDataName:               internal.PropFindValue(&addressData{Data: item.Content}),
	}

	// RFC 6352 §10.4: allprop must not carry address-data, or a request for a
	// collection's metadata answers with the whole collection's contents.
	internal.RemoveFromAllProp(pf, props, addressDataName)
	return internal.NewPropFindResponse(href, pf, props)
}

// principalHref is where the actor's own principal lives, which is what
// DAV:current-user-principal reports whatever resource is being described.
func (a *adapter) principalHref(ctx context.Context) (string, error) {
	href, err := a.cfg.Routes.Href(ctx, AccountResource(actorFrom(ctx).Account))
	if err != nil {
		return "", fmt.Errorf("carddav: rendering principal href: %w", err)
	}
	return href, nil
}

// supportedReports is the DAV:supported-report-set of an address book: the
// REPORTs this handler dispatches, never the ones it merely knows the name of.
func (a *adapter) supportedReports() []xml.Name {
	reports := []xml.Name{addressBookQueryName, addressBookMultigetName}
	if a.caps.syncs {
		reports = append(reports, syncCollectionName)
	}
	return reports
}

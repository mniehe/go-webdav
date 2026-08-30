package carddav

import (
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"

	"github.com/mniehe/davkit/internal"
)

// mkcolReq is RFC 5689 §5.1's extended MKCOL body.
type mkcolReq struct {
	XMLName xml.Name      `xml:"DAV: mkcol"`
	Prop    internal.Prop `xml:"set>prop"`
}

// Mkcol creates an address book via extended MKCOL (RFC 5689): a body carrying
// the CARDDAV:addressbook resourcetype, or no body at all.
//
// The target names an address book that must not exist, so resolution differs
// from every other method: the governing permission is the account's
// CreateBooks — an address book that is not there cannot carry permissions of
// its own.
func (a *adapter) Mkcol(r *http.Request) error {
	ctx := r.Context()

	res, err := a.cfg.Routes.Parse(ctx, r.URL.EscapedPath())
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return errNoSuchResource()
		}
		return fmt.Errorf("carddav: routing request path: %w", err)
	}
	if res.Kind != KindAddressBook {
		return errUnsupportedMethod(r.Method)
	}
	if !a.caps.createsBooks {
		return errUnsupportedMethod(r.Method)
	}

	perms, err := a.backend.AccountPermissions(ctx, actorFrom(ctx), res.Account)
	if err != nil {
		return fmt.Errorf("carddav: reading account permissions: %w", err)
	}
	if !perms.Any() {
		return a.denyUnknown()
	}
	if !perms.CreateBooks {
		return denyOperation("create address books in this account")
	}

	req := CreateAddressBookRequest{Name: res.Book}
	if !internal.IsRequestBodyEmpty(r) {
		var body mkcolReq
		if err := internal.DecodeXMLRequest(r, &body); err != nil {
			return err
		}
		if err := internal.BoundPropNames(&body.Prop); err != nil {
			return err
		}

		var settings bookSettings
		updates := internal.PropUpdatesFromProp(&body.Prop)
		rest := updates[:0]
		for i := range updates {
			// The resourcetype is the request's declaration of what to make,
			// not a settable property: this handler only makes address books,
			// so anything else has no URL to be created at.
			if updates[i].Name == internal.ResourceTypeName {
				var v internal.ResourceType
				if !internal.DecodePropUpdate(&updates[i], &v) ||
					!v.Is(internal.CollectionName) || !v.Is(addressbookName) {
					return internal.HTTPErrorf(http.StatusBadRequest, "carddav: MKCOL must request a CARDDAV:addressbook resource type")
				}
				continue
			}
			rest = append(rest, updates[i])
		}

		rejected := make(map[xml.Name]int)
		for i := range rest {
			if code := applyBookSetting(&settings, &rest[i]); code != http.StatusOK {
				rejected[rest[i].Name] = code
			}
		}
		if len(rejected) > 0 {
			// A property that cannot be set means the address book is not
			// created at all, each property reporting its own outcome.
			return internal.HTTPErrorf(http.StatusForbidden, "carddav: MKCOL body carries a property that cannot be set on an address book")
		}
		settings.applyToCreate(&req)
	}

	creator := a.backend.(BookCreator) //nolint:errcheck // caps.createsBooks was resolved from this assertion at construction
	if _, err := creator.CompareAndCreateAddressBook(ctx, res.Account, req, Unconditional()); err != nil {
		if errors.Is(err, ErrAlreadyExists) {
			// RFC 4918 §9.3: the request URI must be unmapped.
			return internal.NewPreconditionError(http.StatusForbidden, internal.ResourceMustBeNullName)
		}
		return backendError(err)
	}
	return nil
}

func (a *adapter) PropPatch(r *http.Request, pu *internal.PropertyUpdate) (*internal.Response, error) {
	acc, err := a.resolve(r)
	if err != nil {
		return nil, err
	}
	if acc.Kind != KindAddressBook || !a.caps.updatesBooks {
		return nil, errUnsupportedMethod(r.Method)
	}
	if !acc.book.UpdateSettings {
		return nil, denyOperation("change this address book's settings")
	}

	updates := pu.Updates()
	if len(updates) == 0 {
		// RFC 4918 §9.2 requires at least one instruction; a no-op write would
		// emit a <response> carrying neither status nor propstat, which §14.24
		// forbids.
		return nil, internal.HTTPErrorf(http.StatusBadRequest, "carddav: PROPPATCH requested no property changes")
	}

	var settings bookSettings
	rejected := make(map[xml.Name]int)
	for i := range updates {
		if code := applyBookSetting(&settings, &updates[i]); code != http.StatusOK {
			rejected[updates[i].Name] = code
		}
	}
	if len(rejected) > 0 {
		return internal.NewPropPatchFailure(r.URL.Path, updates, rejected)
	}

	updater := a.backend.(BookUpdater) //nolint:errcheck // caps.updatesBooks was resolved from this assertion at construction
	if _, err := updater.CompareAndUpdateAddressBook(r.Context(), acc.AddressBookRef(), settings.patch(), Unconditional()); err != nil {
		return nil, backendError(err)
	}
	return internal.NewPropPatchSuccess(r.URL.Path, updates)
}

// bookSettings accumulates the writable properties of a request before any of
// them is applied, because RFC 4918 §9.2 makes the application atomic.
type bookSettings struct {
	displayName *string
	description *string
}

func (s *bookSettings) patch() AddressBookPatch {
	return AddressBookPatch{
		DisplayName: s.displayName,
		Description: s.description,
	}
}

func (s *bookSettings) applyToCreate(req *CreateAddressBookRequest) {
	if s.displayName != nil {
		req.DisplayName = *s.displayName
	}
	if s.description != nil {
		req.Description = *s.description
	}
}

// applyBookSetting records one requested property change and returns that
// property's status. Anything not recognised as writable — an unknown dead
// property, or a protected live one — is 403, which fails the whole request.
func applyBookSetting(s *bookSettings, prop *internal.PropUpdate) int {
	switch prop.Name {
	case internal.DisplayNameName:
		var v internal.DisplayName
		if !internal.DecodePropUpdate(prop, &v) || len(v.Name) > internal.MaxPropValueSize {
			return http.StatusConflict
		}
		s.displayName = &v.Name
	case addressBookDescriptionName:
		var v addressBookDescription
		if !internal.DecodePropUpdate(prop, &v) || len(v.Description) > internal.MaxPropValueSize {
			return http.StatusConflict
		}
		s.description = &v.Description
	default:
		return http.StatusForbidden
	}
	return http.StatusOK
}

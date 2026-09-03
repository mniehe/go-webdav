package carddav

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/emersion/go-vcard"
	"github.com/mniehe/davkit/internal"
)

// handleReport dispatches a REPORT. internal.Handler does not know the method,
// so Handler.ServeHTTP routes it here before delegating.
//
// Every report is served against an address book collection. RFC 6352 also
// permits query and multiget against a single item, which no mainstream client
// sends; an item target answers 405 and Allow says the same.
func (a *adapter) handleReport(w http.ResponseWriter, r *http.Request) error {
	acc, err := a.resolve(r)
	if err != nil {
		return err
	}
	if acc.Kind != KindAddressBook {
		return errUnsupportedMethod(r.Method)
	}

	var report reportReq
	if err := internal.DecodeXMLRequestWithin(r, &report, internal.RequestNodeBudget(0)); err != nil {
		return err
	}

	// Every report reads the items; carddav has no analogue of caldav's
	// busy-time tier that could justify a weaker grant.
	if !acc.book.ViewDetails {
		return denyOperation("read the items in this address book")
	}

	switch {
	case report.Query != nil:
		if err := internal.BoundReportProp(report.Query.Prop, report.Query.AllProp, report.Query.PropName); err != nil {
			return err
		}
		return a.reportQuery(w, r, acc, report.Query)
	case report.Multiget != nil:
		if err := internal.BoundReportProp(report.Multiget.Prop, report.Multiget.AllProp, report.Multiget.PropName); err != nil {
			return err
		}
		if err := internal.BoundHrefs(report.Multiget.Hrefs, 0); err != nil {
			return err
		}
		if err := internal.BoundResponseWork(internal.SelectorEchoSize(report.Multiget.Prop, report.Multiget.AllProp, report.Multiget.PropName), len(report.Multiget.Hrefs), 0); err != nil {
			return err
		}
		return a.reportMultiget(w, r, acc, report.Multiget)
	case report.SyncCollection != nil:
		// sync-collection tolerates a missing selector (a fallback below), so
		// only the echo needs bounding here.
		if err := internal.BoundPropNames(report.SyncCollection.Prop); err != nil {
			return err
		}
		return a.reportSync(w, r, acc, report.SyncCollection)
	default:
		return internal.HTTPErrorf(http.StatusBadRequest, "carddav: expected addressbook-query, addressbook-multiget or sync-collection element in REPORT request")
	}
}

// reportObject is one item as a report serves it: the stored fact plus the
// parsed copy the engine works on.
type reportObject struct {
	href string
	etag internal.ETag
	card vcard.Card
}

// parseStored parses an item's bytes for matching. The library validated
// everything it stored, but a read-only backend can hold bytes it never saw —
// and those cannot be matched, so the answer is a loud contract violation
// rather than a listing that silently omits a member.
func (a *adapter) parseStored(ctx context.Context, ref ItemRef, item Item, scope uint64) (reportObject, error) {
	if !isSingleCard(item.Content) {
		return reportObject{}, fmt.Errorf("carddav: item %q holds bytes that are not one vCard", ref.Item)
	}
	card, err := vcard.NewDecoder(bytes.NewReader(item.Content)).Decode()
	if err != nil {
		return reportObject{}, fmt.Errorf("carddav: item %q holds bytes that do not parse as a vCard: %w", ref.Item, err)
	}
	href, err := a.cfg.Routes.Href(ctx, ItemResource(ref))
	if err != nil {
		return reportObject{}, fmt.Errorf("carddav: rendering item href: %w", err)
	}
	return reportObject{href: href, etag: etagFor(scope, item), card: card}, nil
}

// reportResponse renders one report row. Unlike itemResponse it serves the
// engine's output — projected or converted — rather than the stored bytes.
func reportResponse(obj reportObject, pf *internal.PropFind) (*internal.Response, error) {
	var buf bytes.Buffer
	if err := vcard.NewEncoder(&buf).Encode(obj.card); err != nil {
		return nil, fmt.Errorf("carddav: encoding report address-data: %w", err)
	}

	props := map[xml.Name]internal.PropFindFunc{
		internal.ResourceTypeName:     internal.PropFindValue(internal.NewResourceType()),
		internal.GetETagName:          internal.PropFindValue(&internal.GetETag{ETag: obj.etag}),
		internal.GetContentTypeName:   internal.PropFindValue(&internal.GetContentType{Type: vcardMediaType}),
		internal.GetContentLengthName: internal.PropFindValue(&internal.GetContentLength{Length: int64(buf.Len())}),
		addressDataName:               internal.PropFindValue(&addressData{Data: buf.Bytes()}),
	}
	internal.RemoveFromAllProp(pf, props, addressDataName)
	return internal.NewPropFindResponse(obj.href, pf, props)
}

// maxDataRequestProps bounds the property list one address-data element may
// name. Every name becomes part of a projection applied to each returned
// object, and the XML body limit alone allows far more names than a vCard has
// properties.
const maxDataRequestProps = 256

// dataRequestFrom pulls the address-data projection out of a report's prop
// selector. Absent means the whole object, as stored.
func dataRequestFrom(prop *internal.Prop) (addressDataReq, error) {
	if prop == nil {
		return addressDataReq{}, nil
	}
	var req addressDataReq
	err := prop.Decode(&req)
	if internal.IsNotFound(err) {
		return addressDataReq{}, nil
	}
	if err != nil {
		// The element being re-decoded is client XML, so a failure here is the
		// request's fault, not the server's.
		return addressDataReq{}, internal.HTTPErrorf(http.StatusBadRequest, "carddav: %v", err)
	}
	if req.Allprop != nil && len(req.Props) > 0 {
		return addressDataReq{}, internal.HTTPErrorf(http.StatusBadRequest, "carddav: only one of allprop or prop can be specified in address-data")
	}
	if len(req.Props) > maxDataRequestProps {
		return addressDataReq{}, internal.HTTPErrorf(http.StatusBadRequest, "carddav: address-data names more than %d properties", maxDataRequestProps)
	}
	for _, p := range req.Props {
		if len(p.Name) > internal.MaxPropNameSize {
			return addressDataReq{}, internal.HTTPErrorf(http.StatusBadRequest, "carddav: address-data name exceeds %d bytes", internal.MaxPropNameSize)
		}
	}
	// RFC 6352 §8.6.2 (CARDDAV:supported-address-data): the attributes must
	// name a media type the server can serve. Only upgrades are offered — a
	// 4.0 card downgraded to 3.0 would silently drop what 3.0 cannot express.
	if req.ContentType != "" && req.ContentType != vcard.MIMEType {
		return addressDataReq{}, internal.NewPreconditionError(http.StatusForbidden, supportedAddressDataName)
	}
	if req.Version != "" && !vcardVersions[req.Version] {
		return addressDataReq{}, internal.NewPreconditionError(http.StatusForbidden, supportedAddressDataName)
	}
	return req, nil
}

// cloneField copies a field deeply enough that rewriting the copy cannot touch
// the original's parameters.
func cloneField(field *vcard.Field) *vcard.Field {
	cloned := *field
	if field.Params != nil {
		cloned.Params = make(vcard.Params, len(field.Params))
		for k, v := range field.Params {
			cloned.Params[k] = append([]string(nil), v...)
		}
	}
	return &cloned
}

// copyCard clones a card deeply enough that converting or projecting the copy
// cannot touch the original's fields or parameters.
func copyCard(card vcard.Card) vcard.Card {
	out := make(vcard.Card, len(card))
	for name, fields := range card {
		cloned := make([]*vcard.Field, len(fields))
		for i, field := range fields {
			cloned[i] = cloneField(field)
		}
		out[name] = cloned
	}
	return out
}

// blankFields clones fields with their value data stripped (RFC 6352 §10.4.2
// novalue), leaving the source — commonly the backend's own cache — untouched.
func blankFields(fields []*vcard.Field) []*vcard.Field {
	out := make([]*vcard.Field, len(fields))
	for i, field := range fields {
		blanked := cloneField(field)
		blanked.Value = ""
		out[i] = blanked
	}
	return out
}

// shapeCard applies the client's version negotiation and property projection to
// one parsed card, leaving the original untouched.
func shapeCard(obj reportObject, req *addressDataReq) (reportObject, error) {
	card := obj.card

	if req.Version != "" && card.Value(vcard.FieldVersion) != req.Version {
		if req.Version != "4.0" {
			// The stored card is newer than the requested version, and a lossy
			// downgrade would be served as if it were the real resource.
			return reportObject{}, internal.NewPreconditionError(http.StatusForbidden, supportedAddressDataName)
		}
		card = copyCard(card)
		vcard.ToV4(card)
	}

	if req.Allprop == nil && len(req.Props) > 0 {
		projected := make(vcard.Card, len(req.Props)+1)
		// A card without VERSION does not encode, so the projection carries
		// the source's across whether or not it was asked for.
		if version, ok := card[vcard.FieldVersion]; ok {
			projected[vcard.FieldVersion] = version
		}
		for _, p := range req.Props {
			name := strings.ToUpper(p.Name)
			fields, ok := card[name]
			if !ok {
				continue
			}
			if p.NoValue {
				fields = blankFields(fields)
			}
			projected[name] = fields
		}
		card = projected
	}

	obj.card = card
	return obj, nil
}

func (a *adapter) reportQuery(w http.ResponseWriter, r *http.Request, acc access, query *addressbookQueryReq) error {
	ctx := r.Context()

	dataReq, err := dataRequestFrom(query.Prop)
	if err != nil {
		return err
	}
	if filterErr := validateFilter(&query.Filter); filterErr != nil {
		return filterErr
	}
	var limit *int
	if query.Limit != nil {
		if limit, err = internal.RequestedCount(query.Limit.NResults); err != nil {
			return err
		}
	}

	book, err := a.getAddressBook(ctx, acc.AddressBookRef())
	if err != nil {
		return backendError(err)
	}
	scope := bookScope(book.ID)

	items, _, err := a.listItems(ctx, acc.AddressBookRef())
	if err != nil {
		return backendError(err)
	}

	var matched []reportObject
	truncated := false
	for i := range items {
		obj, err := a.parseStored(ctx, ItemRef{Book: acc.AddressBookRef(), Item: items[i].Name}, items[i], scope)
		if err != nil {
			return err
		}
		ok, err := matchFilter(&query.Filter, obj.card)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if limit != nil && len(matched) >= *limit {
			// One match beyond the client's limit is all it takes to know the
			// answer is cut short; RFC 6352 §8.6.2 lets the server trim as
			// long as the truncation is marked.
			truncated = true
			break
		}
		matched = append(matched, obj)
		if len(matched) > a.cfg.MaxSearchResults {
			return internal.NewPreconditionError(http.StatusInsufficientStorage, internal.NumberOfMatchesWithinLimitsName)
		}
	}
	if err := internal.BoundResponseWork(internal.SelectorEchoSize(query.Prop, query.AllProp, query.PropName), len(matched), 0); err != nil {
		return err
	}

	pf := internal.PropFind{Prop: query.Prop, AllProp: query.AllProp, PropName: query.PropName}
	ms := internal.NewMultiStatusWriter(w, r.URL.Path, 0)
	defer ms.Abort() //nolint:errcheck // best-effort close if the document was already started

	for _, obj := range matched {
		shaped, err := shapeCard(obj, &dataReq)
		if err == nil {
			var resp *internal.Response
			if resp, err = reportResponse(shaped, &pf); err == nil {
				if writeErr := ms.Write(resp); writeErr != nil {
					return writeErr
				}
				continue
			}
		}
		if !ms.Started() {
			return err
		}
		return ms.Fail(err)
	}
	if truncated {
		if err := ms.Truncate(); err != nil {
			return err
		}
	}
	return ms.Close()
}

func (a *adapter) reportMultiget(w http.ResponseWriter, r *http.Request, acc access, multiget *addressbookMultigetReq) error {
	ctx := r.Context()

	dataReq, err := dataRequestFrom(multiget.Prop)
	if err != nil {
		return err
	}
	book, err := a.getAddressBook(ctx, acc.AddressBookRef())
	if err != nil {
		return backendError(err)
	}
	scope := bookScope(book.ID)

	pf := internal.PropFind{Prop: multiget.Prop, AllProp: multiget.AllProp, PropName: multiget.PropName}
	ms := internal.NewMultiStatusWriter(w, r.URL.Path, 0)
	defer ms.Abort() //nolint:errcheck // best-effort close if the document was already started

	for i := range multiget.Hrefs {
		href := &multiget.Hrefs[i]

		// RFC 6352 §8.7: hrefs must name members of the request collection.
		// Anything outside it is refused per-row and never fetched — the client
		// named the resource, so the refusal tells it nothing it did not supply.
		res, ok := a.resolveMember(ctx, r, acc, href)
		if !ok {
			resp := internal.NewErrorResponse(href.Path, internal.HTTPErrorf(http.StatusForbidden, "carddav: href %q is outside the request collection", href.Path))
			if err := ms.Write(resp); err != nil {
				return err
			}
			continue
		}

		item, err := a.getItem(ctx, res.ItemRef())
		if err == nil {
			var obj reportObject
			if obj, err = a.parseStored(ctx, res.ItemRef(), item, scope); err == nil {
				var shaped reportObject
				if shaped, err = shapeCard(obj, &dataReq); err == nil {
					var resp *internal.Response
					if resp, err = reportResponse(shaped, &pf); err == nil {
						if writeErr := ms.Write(resp); writeErr != nil {
							return writeErr
						}
						continue
					}
				}
			}
		}
		// A contract violation is the handler's problem, not the row's: the
		// rows around a 500 row would read as a complete answer.
		if errors.Is(err, errContract) {
			if !ms.Started() {
				return err
			}
			return ms.Fail(err)
		}
		if writeErr := ms.Write(internal.NewErrorResponse(res.href, backendError(err))); writeErr != nil {
			return writeErr
		}
	}
	return ms.Close()
}

// memberResource is a multiget href resolved to a member of the request
// collection, keeping the path the row is reported under.
type memberResource struct {
	Resource
	href string
}

func (a *adapter) resolveMember(ctx context.Context, r *http.Request, acc access, href *internal.Href) (memberResource, bool) {
	cleaned, ok := internal.ChildHref(r, r.URL.Path, href)
	if !ok {
		return memberResource{}, false
	}
	res, err := a.cfg.Routes.Parse(ctx, (*url.URL)(href).EscapedPath())
	if err != nil || res.Kind != KindItem || res.AddressBookRef() != acc.AddressBookRef() {
		return memberResource{}, false
	}
	return memberResource{Resource: res, href: cleaned}, true
}

func (a *adapter) reportSync(w http.ResponseWriter, r *http.Request, acc access, query *internal.SyncCollectionQuery) error {
	if !a.caps.syncs {
		return internal.HTTPErrorf(http.StatusNotImplemented, "carddav: sync-collection is not supported by this backend")
	}
	if err := internal.ValidateSyncRequest(r, query); err != nil {
		return err
	}
	limit, err := internal.RequestedLimit(query.Limit)
	if err != nil {
		return err
	}
	dataReq, err := dataRequestFrom(query.Prop)
	if err != nil {
		return err
	}

	ctx := r.Context()
	book, err := a.getAddressBook(ctx, acc.AddressBookRef())
	if err != nil {
		return backendError(err)
	}
	scope := bookScope(book.ID)

	updated, deleted, coveredThrough, truncated, err := a.syncDelta(ctx, acc.AddressBookRef(), scope, query.SyncToken, limit)
	if err != nil {
		return err
	}

	// RFC 6578 §3.7: a server that cannot hold the result at or below the
	// requested limit must fail rather than answer with more than was asked
	// for. Trimming here instead would leave the token describing members that
	// were dropped.
	if limit != nil && len(updated)+len(deleted) > *limit {
		return internal.NewPreconditionError(http.StatusInsufficientStorage, internal.NumberOfMatchesWithinLimitsName)
	}

	// A sync-collection request always carries a <prop>; fall back to allprop
	// so a malformed body without one still yields a usable response.
	pf := internal.PropFind{Prop: query.Prop}
	if query.Prop == nil {
		pf.AllProp = &struct{}{}
	}
	if err := internal.BoundResponseWork(internal.SelectorEchoSize(pf.Prop, pf.AllProp, pf.PropName), len(updated)+len(deleted), 0); err != nil {
		return err
	}

	ms := internal.NewMultiStatusWriter(w, r.URL.Path, 0)
	defer ms.Abort() //nolint:errcheck // best-effort close if the document was already started

	for i := range updated {
		obj, err := a.parseStored(ctx, updated[i].ref, updated[i].item, scope)
		if err == nil {
			obj, err = shapeCard(obj, &dataReq)
		}
		var resp *internal.Response
		if err == nil {
			resp, err = reportResponse(obj, &pf)
		}
		if err != nil {
			if !ms.Started() {
				return err
			}
			return ms.Fail(err)
		}
		if err := ms.Write(resp); err != nil {
			return err
		}
	}
	for _, gone := range deleted {
		// RFC 6578 §3.2: a removed member is a 404 row with no properties,
		// telling the client to drop it.
		resp := internal.Response{
			Hrefs:  []internal.Href{{Path: gone}},
			Status: &internal.Status{Code: http.StatusNotFound},
		}
		if err := ms.Write(&resp); err != nil {
			return err
		}
	}
	if truncated {
		if err := ms.Truncate(); err != nil {
			return err
		}
	}
	ms.SetSyncToken(syncTokenFor(scope, coveredThrough))
	return ms.Close()
}

type syncMember struct {
	ref  ItemRef
	item Item
}

// syncDelta answers "what changed since this token". An empty token is the
// initial sync: a full consistent listing, whose revision the new token
// describes. Anything unserviceable — foreign, garbled, older than retained
// history — MUST become DAV:valid-sync-token rather than a silent full
// listing, which carries no deletions and would leave removed items on the
// client forever.
func (a *adapter) syncDelta(ctx context.Context, ref AddressBookRef, scope uint64, token string, limit *int) (updated []syncMember, deleted []string, coveredThrough Revision, truncated bool, err error) {
	if token == "" {
		items, rev, listErr := a.listItems(ctx, ref)
		if listErr != nil {
			return nil, nil, 0, false, backendError(listErr)
		}
		for i := range items {
			updated = append(updated, syncMember{ref: ItemRef{Book: ref, Item: items[i].Name}, item: items[i]})
		}
		return updated, nil, rev, false, nil
	}

	after, ok := parseSyncToken(scope, token)
	if !ok {
		return nil, nil, 0, false, internal.NewInvalidSyncTokenError()
	}

	maxChanges := 0
	if limit != nil {
		maxChanges = *limit
	}
	sync := a.backend.(SyncBackend) //nolint:errcheck // caps.syncs was resolved from this assertion at construction
	batch, err := sync.ListChanges(ctx, ref, after, maxChanges)
	if err != nil {
		if errors.Is(err, ErrHistoryTooOld) {
			return nil, nil, 0, false, internal.NewInvalidSyncTokenError()
		}
		return nil, nil, 0, false, backendError(err)
	}
	if vErr := validateChangeBatch(after, batch); vErr != nil {
		return nil, nil, 0, false, vErr
	}

	for _, change := range batch.Changes {
		itemRef := ItemRef{Book: ref, Item: change.Item}
		if change.Deleted {
			href, hrefErr := a.cfg.Routes.Href(ctx, ItemResource(itemRef))
			if hrefErr != nil {
				return nil, nil, 0, false, fmt.Errorf("carddav: rendering deleted member href: %w", hrefErr)
			}
			deleted = append(deleted, href)
			continue
		}
		item, getErr := a.getItem(ctx, itemRef)
		if getErr != nil {
			return nil, nil, 0, false, backendError(getErr)
		}
		updated = append(updated, syncMember{ref: itemRef, item: item})
	}
	return updated, deleted, batch.CoveredThrough, batch.HasMore, nil
}

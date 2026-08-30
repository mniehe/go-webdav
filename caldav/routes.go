package caldav

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// ResourceKind is what a URL points at.
type ResourceKind uint8

const (
	KindAccount ResourceKind = iota + 1
	KindCalendar
	KindItem
)

func (k ResourceKind) String() string {
	switch k {
	case KindAccount:
		return "account"
	case KindCalendar:
		return "calendar"
	case KindItem:
		return "item"
	default:
		return "unknown resource kind"
	}
}

// Resource is what a URL names.
type Resource struct {
	Kind     ResourceKind
	Account  AccountID
	Calendar Segment // zero unless Kind is KindCalendar or KindItem
	Item     Segment // zero unless Kind is KindItem
}

func AccountResource(account AccountID) Resource {
	return Resource{Kind: KindAccount, Account: account}
}

func CalendarResource(ref CalendarRef) Resource {
	return Resource{Kind: KindCalendar, Account: ref.Account, Calendar: ref.Calendar}
}

func ItemResource(ref ItemRef) Resource {
	return Resource{
		Kind:     KindItem,
		Account:  ref.Calendar.Account,
		Calendar: ref.Calendar.Calendar,
		Item:     ref.Item,
	}
}

// CalendarRef is the calendar this resource is in, or is.
func (r Resource) CalendarRef() CalendarRef {
	return CalendarRef{Account: r.Account, Calendar: r.Calendar}
}

// ItemRef is meaningful only when Kind is KindItem.
func (r Resource) ItemRef() ItemRef {
	return ItemRef{Calendar: r.CalendarRef(), Item: r.Item}
}

// Routes maps between URLs and resources, in both directions. Implement it to
// serve a layout other than the default — tenant prefixes, opaque account IDs,
// URLs migrated from another server. Storage stays URL-free either way.
type Routes interface {
	// Parse maps a request path to the resource it names, or returns
	// ErrNotFound.
	//
	// The path is the escaped form, http.Request.URL.EscapedPath(). Parsing the
	// decoded path instead would let a client send a name containing %2F and
	// have it split into two segments, so a calendar could be addressed as an
	// item of another.
	Parse(ctx context.Context, path string) (Resource, error)

	// Href renders a resource as a path, escaped the same way. An error here is
	// a misconfiguration rather than a client mistake, and becomes a 500.
	Href(ctx context.Context, res Resource) (string, error)
}

// DefaultRoutes serves /<account>/<calendar>/<item> under a prefix, with the
// account and its calendar list at the same URL. Pass "" for no prefix.
func DefaultRoutes(prefix string) Routes {
	prefix = strings.TrimSuffix(prefix, "/")
	if prefix != "" && !strings.HasPrefix(prefix, "/") {
		prefix = "/" + prefix
	}
	return defaultRoutes{prefix: prefix}
}

type defaultRoutes struct {
	prefix string
}

func (d defaultRoutes) Parse(_ context.Context, path string) (Resource, error) {
	rest, ok := strings.CutPrefix(path, d.prefix)
	if !ok || !strings.HasPrefix(rest, "/") {
		return Resource{}, ErrNotFound
	}

	rest = strings.TrimPrefix(rest, "/")
	collection := strings.HasSuffix(rest, "/")
	rest = strings.TrimSuffix(rest, "/")
	if rest == "" {
		return Resource{}, ErrNotFound
	}

	raw := strings.Split(rest, "/")
	if len(raw) > 3 {
		return Resource{}, ErrNotFound
	}

	// Unescape once, per segment, then validate. A segment that decodes to
	// contain a slash, a dot segment or a control character is refused here
	// rather than being allowed to mean something further in.
	segments := make([]Segment, len(raw))
	for i, escaped := range raw {
		decoded, err := url.PathUnescape(escaped)
		if err != nil {
			return Resource{}, ErrNotFound
		}
		seg, err := ParseSegment(decoded)
		if err != nil {
			return Resource{}, ErrNotFound
		}
		segments[i] = seg
	}

	account := AccountID(segments[0].String())
	switch len(segments) {
	case 1:
		return AccountResource(account), nil
	case 2:
		return CalendarResource(CalendarRef{Account: account, Calendar: segments[1]}), nil
	default:
		if collection {
			return Resource{}, ErrNotFound
		}
		return ItemResource(ItemRef{
			Calendar: CalendarRef{Account: account, Calendar: segments[1]},
			Item:     segments[2],
		}), nil
	}
}

func (d defaultRoutes) Href(_ context.Context, res Resource) (string, error) {
	if res.Kind != KindAccount && res.Kind != KindCalendar && res.Kind != KindItem {
		return "", fmt.Errorf("caldav: cannot render a %s as a URL", res.Kind)
	}
	account, err := ParseSegment(string(res.Account))
	if err != nil {
		return "", fmt.Errorf("caldav: account %q cannot be a URL segment: %w", res.Account, err)
	}

	href := d.prefix + "/" + url.PathEscape(account.String()) + "/"
	if res.Kind == KindAccount {
		return href, nil
	}

	if res.Calendar.IsZero() {
		return "", fmt.Errorf("caldav: %s resource has no calendar", res.Kind)
	}
	href += url.PathEscape(res.Calendar.String()) + "/"
	if res.Kind == KindCalendar {
		return href, nil
	}

	if res.Item.IsZero() {
		return "", fmt.Errorf("caldav: item resource has no item")
	}
	return href + url.PathEscape(res.Item.String()), nil
}

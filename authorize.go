package webdav

import (
	"context"

	"github.com/mniehe/davkit/internal"
)

// Operation names what a request is trying to do. It is what an
// AuthorizationBackend is asked to admit or refuse.
//
// This is an operation rather than an RFC 3744 privilege because the privilege
// a method needs is not always fixed. PUT is controlled by DAV:write-content
// when the target exists and by DAV:bind on the parent collection when it does
// not (RFC 3744 §3.3, and the table in Appendix B). Only the Backend can tell
// those apart, and it can do so atomically with the write; a handler that
// looked first would both race and turn every PUT into a probe for whether a
// resource is there.
type Operation uint8

const (
	// OperationRead covers GET, HEAD, OPTIONS, PROPFIND and every REPORT.
	// Appendix B gives all of them DAV:read, and REPORT needs it "on all
	// referenced resources", not only on the request URI.
	OperationRead Operation = iota

	// OperationPut covers PUT: DAV:write-content on the target when it exists,
	// DAV:bind on the parent collection when it does not.
	OperationPut

	// OperationDelete covers DELETE: DAV:unbind on the parent collection.
	OperationDelete

	// OperationPropPatch covers PROPPATCH: DAV:write-properties.
	OperationPropPatch

	// OperationMkcol covers MKCOL and MKCALENDAR: DAV:bind on the parent
	// collection.
	OperationMkcol

	// OperationReadACL covers reading the DAV:acl property, which names every
	// principal a resource is shared with (RFC 3744 §5.5).
	OperationReadACL

	// OperationReadCurrentUserPrivilegeSet covers reading
	// DAV:current-user-privilege-set. RFC 3744 §3.7 keeps this separate from
	// read-acl so that most users may see their own permissions while few may
	// see the whole list.
	OperationReadCurrentUserPrivilegeSet
)

func (op Operation) String() string {
	switch op {
	case OperationRead:
		return "read"
	case OperationPut:
		return "put"
	case OperationDelete:
		return "delete"
	case OperationPropPatch:
		return "proppatch"
	case OperationMkcol:
		return "mkcol"
	case OperationReadACL:
		return "read-acl"
	case OperationReadCurrentUserPrivilegeSet:
		return "read-current-user-privilege-set"
	}
	return "unknown"
}

// AuthorizationBackend decides whether the current principal may carry out an
// operation. Both caldav.Backend and carddav.Backend require it.
//
// The operation is asked about calendars, address books and the objects inside
// them. It is not asked about the discovery resources above them -- the root,
// the principal, and the home set -- because those are synthesised from the
// caller.s own identity, serve no other principal.s path, and list only members
// that are authorized one at a time. Requiring a grant there would mean every
// backend had to grant read on paths that expose nothing, and forgetting it
// would break client bootstrap rather than deny any data.
//
// The operation is asked about the resource named by path, whether or not that
// resource exists. Where RFC 3744 places a privilege on the parent collection
// -- bind for creation, unbind for removal -- the Backend resolves the parent
// itself, because it is the only party that knows how paths map onto storage.
//
// A nil return admits the operation. Returning an error refuses it, and the
// error travels to the client as it is: use NewNeedPrivilegesError for the
// RFC 3744 §7.1.1 answer, or a 404 where admitting the resource exists would
// itself disclose something.
type AuthorizationBackend interface {
	Authorize(ctx context.Context, path string, op Operation) error
}

// BulkAuthorizationBackend is an optional interface for answering about many
// resources at once. A REPORT or a Depth-1 PROPFIND authorises every member it
// is about to report, which is one question per member; a Backend that can
// answer them in a single query should implement this.
//
// The returned slice must have one entry per path, in order. A nil entry admits
// that path.
//
// Implementing it is an optimisation, never a relaxation: a Backend that does
// not gets the same questions asked one at a time.
type BulkAuthorizationBackend interface {
	AuthorizationBackend
	AuthorizeMany(ctx context.Context, paths []string, op Operation) []error
}

// AuthorizeMany asks about every path, using the Backend's bulk implementation
// when it has one and Authorize in a loop when it does not. The result has one
// entry per path, in order.
func AuthorizeMany(ctx context.Context, b AuthorizationBackend, paths []string, op Operation) []error {
	if bulk, ok := b.(BulkAuthorizationBackend); ok {
		results := bulk.AuthorizeMany(ctx, paths, op)
		if len(results) == len(paths) {
			return results
		}
		// A Backend that answered the wrong number of questions has told us
		// nothing about any of them.
		results = make([]error, len(paths))
		for i := range results {
			results[i] = internal.HTTPErrorf(500, "webdav: backend returned %d authorization results for %d paths", len(results), len(paths))
		}
		return results
	}
	results := make([]error, len(paths))
	for i, p := range paths {
		results[i] = b.Authorize(ctx, p, op)
	}
	return results
}

// NewNeedPrivilegesError refuses a request with 403 and the RFC 3744 §7.1.1
// DAV:need-privileges precondition naming the privilege that was missing.
//
// href names the resource the privilege is needed on, which for bind and unbind
// is the parent collection rather than the target.
func NewNeedPrivilegesError(href string, privilege Privilege) error {
	return internal.NewNeedPrivilegesError(href, string(privilege))
}

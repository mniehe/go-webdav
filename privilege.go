package webdav

// Privilege is an access-control privilege (RFC 3744 §3). The constants below
// name the privileges the RFC itself defines; a server may report others from
// its own namespace, which is why this is an open string type rather than an
// enumeration.
type Privilege string

const (
	// PrivilegeRead controls GET and PROPFIND.
	PrivilegeRead Privilege = "read"
	// PrivilegeWrite aggregates PrivilegeWriteContent, PrivilegeWriteProperties,
	// PrivilegeBind and PrivilegeUnbind (RFC 3744 §3.2).
	PrivilegeWrite Privilege = "write"
	// PrivilegeWriteProperties controls PROPPATCH.
	PrivilegeWriteProperties Privilege = "write-properties"
	// PrivilegeWriteContent controls PUT on an existing resource.
	PrivilegeWriteContent Privilege = "write-content"
	// PrivilegeBind controls adding members to a collection.
	PrivilegeBind Privilege = "bind"
	// PrivilegeUnbind controls removing members from a collection.
	PrivilegeUnbind Privilege = "unbind"
	// PrivilegeReadACL controls reading DAV:acl.
	PrivilegeReadACL Privilege = "read-acl"
	// PrivilegeWriteACL controls the ACL method.
	PrivilegeWriteACL Privilege = "write-acl"
	// PrivilegeReadCurrentUserPrivilegeSet controls reading
	// DAV:current-user-privilege-set.
	PrivilegeReadCurrentUserPrivilegeSet Privilege = "read-current-user-privilege-set"
	// PrivilegeAll aggregates every privilege that applies to the resource
	// (RFC 3744 §3.11).
	PrivilegeAll Privilege = "all"
)

// aggregates maps each aggregate privilege to the privileges it contains,
// per RFC 3744 §3.2 and §3.11.
var aggregates = map[Privilege][]Privilege{
	PrivilegeWrite: {PrivilegeWriteContent, PrivilegeWriteProperties, PrivilegeBind, PrivilegeUnbind},
	PrivilegeAll: {
		PrivilegeRead, PrivilegeWrite, PrivilegeWriteContent, PrivilegeWriteProperties,
		PrivilegeBind, PrivilegeUnbind, PrivilegeReadACL, PrivilegeWriteACL,
		PrivilegeReadCurrentUserPrivilegeSet,
	},
}

// abstract names the privileges this server publishes as abstract in
// DAV:supported-privilege-set. RFC 3744 §5.3 says they cannot be granted on
// their own, so they must not appear in an ACE or in the current user.s
// privilege set even though a Backend may use them as shorthand for a grant.
var abstract = map[Privilege]bool{PrivilegeAll: true}

// PrivilegeSet is the set of privileges the authenticated user holds on a
// resource, which the server advertises as DAV:current-user-privilege-set.
//
// This is what the client is TOLD it may do. What it may actually do is decided
// by AuthorizationBackend.Authorize, which every request passes through first.
// The two answer the same question and a Backend must keep them agreeing:
// advertising more than Authorize admits produces clients that fail
// confusingly, and advertising less produces clients that hide features the
// user has.
//
// The zero value grants nothing, so a Backend that never sets one advertises a
// resource clients treat as unusable.
type PrivilegeSet []Privilege

// Has reports whether the set grants p, following the aggregation the RFC
// requires: holding DAV:write also grants the four privileges it contains, and
// holding DAV:all grants everything.
func (ps PrivilegeSet) Has(p Privilege) bool {
	for _, granted := range ps {
		if granted == p {
			return true
		}
		for _, contained := range aggregates[granted] {
			if contained == p {
				return true
			}
		}
	}
	return false
}

// CanWrite reports whether the set allows modifying the resource, which is the
// question the handlers ask before admitting a mutation.
func (ps PrivilegeSet) CanWrite() bool { return ps.Has(PrivilegeWrite) }

// Expanded returns the privileges this set grants on the wire: every aggregate
// replaced by itself and the privileges it contains, without duplicates, and
// without the abstract ones.
//
// DAV:current-user-privilege-set carries the exact set the server grants
// (RFC 3744 §5.4) and clients test it for the specific privilege they need, so
// reporting only DAV:all would leave a client looking for DAV:write concluding
// it may not write. DAV:all itself is dropped: this server publishes it as
// abstract in DAV:supported-privilege-set, and §5.3 says an abstract privilege
// is never one a principal holds.
func (ps PrivilegeSet) Expanded() []Privilege {
	var out []Privilege
	seen := make(map[Privilege]bool, len(ps))
	add := func(p Privilege) {
		if !seen[p] && !abstract[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, granted := range ps {
		add(granted)
		for _, contained := range aggregates[granted] {
			add(contained)
		}
	}
	return out
}

// Pseudo names one of the pseudo-principals of RFC 3744 §5.5.1, which match a
// class of user rather than a particular one.
type Pseudo string

const (
	// PseudoAll matches every user, authenticated or not.
	PseudoAll Pseudo = "all"
	// PseudoAuthenticated matches any authenticated user.
	PseudoAuthenticated Pseudo = "authenticated"
	// PseudoUnauthenticated matches only unauthenticated users, which is how a
	// publicly readable collection is expressed.
	PseudoUnauthenticated Pseudo = "unauthenticated"
	// PseudoSelf matches the principal the resource itself describes.
	PseudoSelf Pseudo = "self"
)

// PrincipalRef identifies who an ACE applies to. Either Href names a principal
// resource, or Pseudo names a class of user; setting both is meaningless and the
// href wins.
type PrincipalRef struct {
	Href   string
	Pseudo Pseudo
}

// ACE is one access control entry: the privileges a principal holds on a
// resource (RFC 3744 §5.5).
//
// Only grants are represented. This library never reports a deny ACE, which
// keeps every access control list it produces inside the DAV:grant-only
// precondition of §8.1.1 and avoids the ordering subtleties that make mixed
// grant/deny lists hard to reason about.
type ACE struct {
	Principal PrincipalRef
	Grant     PrivilegeSet

	// Protected marks an entry the server will not remove, such as the owner's
	// own access.
	Protected bool

	// Inherited is the path of the resource this entry comes from, when the
	// entry is not defined on the resource itself.
	Inherited string
}

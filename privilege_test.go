// package webdav_test so these assertions use only what a consumer can reach.
package webdav_test

import (
	"testing"

	"github.com/mniehe/davkit"
)

// RFC 3744 §3.2: "DAV:write MUST contain DAV:bind, DAV:unbind,
// DAV:write-properties and DAV:write-content". §3.11 makes DAV:all the
// aggregate of the whole set. A backend that grants the aggregate must not have
// to spell out its members.
func TestPrivilegeSetAggregation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		set   webdav.PrivilegeSet
		grant webdav.Privilege
		want  bool
	}{
		{"write contains write-content", webdav.PrivilegeSet{webdav.PrivilegeWrite}, webdav.PrivilegeWriteContent, true},
		{"write contains write-properties", webdav.PrivilegeSet{webdav.PrivilegeWrite}, webdav.PrivilegeWriteProperties, true},
		{"write contains bind", webdav.PrivilegeSet{webdav.PrivilegeWrite}, webdav.PrivilegeBind, true},
		{"write contains unbind", webdav.PrivilegeSet{webdav.PrivilegeWrite}, webdav.PrivilegeUnbind, true},

		// Write does not imply the ACL privileges: a sharee with write access
		// must not be able to reshare.
		{"write does not contain write-acl", webdav.PrivilegeSet{webdav.PrivilegeWrite}, webdav.PrivilegeWriteACL, false},
		{"write does not imply read", webdav.PrivilegeSet{webdav.PrivilegeWrite}, webdav.PrivilegeRead, false},

		{"all contains read", webdav.PrivilegeSet{webdav.PrivilegeAll}, webdav.PrivilegeRead, true},
		{"all contains write", webdav.PrivilegeSet{webdav.PrivilegeAll}, webdav.PrivilegeWrite, true},
		{"all contains write-acl", webdav.PrivilegeSet{webdav.PrivilegeAll}, webdav.PrivilegeWriteACL, true},
		{"all contains unbind", webdav.PrivilegeSet{webdav.PrivilegeAll}, webdav.PrivilegeUnbind, true},

		{"read alone grants only read", webdav.PrivilegeSet{webdav.PrivilegeRead}, webdav.PrivilegeWrite, false},
		{"an unheld privilege is not granted", webdav.PrivilegeSet{webdav.PrivilegeRead}, webdav.PrivilegeWriteContent, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.set.Has(tc.grant); got != tc.want {
				t.Errorf("PrivilegeSet%v.Has(%q) = %v, want %v", tc.set, tc.grant, got, tc.want)
			}
		})
	}
}

// The zero value must grant nothing. A backend that forgets to report
// privileges should produce a resource that is visibly unusable rather than one
// that is silently writable.
func TestZeroPrivilegeSetGrantsNothing(t *testing.T) {
	var ps webdav.PrivilegeSet
	for _, p := range []webdav.Privilege{
		webdav.PrivilegeRead, webdav.PrivilegeWrite, webdav.PrivilegeWriteContent, webdav.PrivilegeAll,
	} {
		if ps.Has(p) {
			t.Errorf("the zero PrivilegeSet granted %q", p)
		}
	}
	if ps.CanWrite() {
		t.Error("the zero PrivilegeSet reported CanWrite")
	}
}

func TestCanWriteFollowsTheWriteAggregate(t *testing.T) {
	if !(webdav.PrivilegeSet{webdav.PrivilegeWrite}).CanWrite() {
		t.Error("a set holding DAV:write cannot write")
	}
	if !(webdav.PrivilegeSet{webdav.PrivilegeAll}).CanWrite() {
		t.Error("a set holding DAV:all cannot write")
	}
	if (webdav.PrivilegeSet{webdav.PrivilegeRead}).CanWrite() {
		t.Error("a read-only set reported CanWrite")
	}
	// Holding every member of the aggregate is not the same as holding the
	// aggregate, and the handlers ask for the aggregate.
	if (webdav.PrivilegeSet{webdav.PrivilegeWriteContent, webdav.PrivilegeWriteProperties}).CanWrite() {
		t.Error("partial write privileges reported CanWrite")
	}
}

// A client tests DAV:current-user-privilege-set for the one privilege it needs,
// so an aggregate has to reach the wire as its members too.
func TestExpandedResolvesAggregates(t *testing.T) {
	has := func(set webdav.PrivilegeSet, want webdav.Privilege) bool {
		for _, p := range set.Expanded() {
			if p == want {
				return true
			}
		}
		return false
	}

	all := webdav.PrivilegeSet{webdav.PrivilegeAll}
	for _, p := range []webdav.Privilege{
		webdav.PrivilegeRead, webdav.PrivilegeWrite, webdav.PrivilegeWriteContent,
		webdav.PrivilegeBind, webdav.PrivilegeUnbind, webdav.PrivilegeWriteACL,
	} {
		if !has(all, p) {
			t.Errorf("expanding DAV:all did not yield %q", p)
		}
	}

	writer := webdav.PrivilegeSet{webdav.PrivilegeRead, webdav.PrivilegeWrite}
	if !has(writer, webdav.PrivilegeWriteContent) {
		t.Error("expanding DAV:write did not yield write-content")
	}
	if has(writer, webdav.PrivilegeWriteACL) {
		t.Error("expanding DAV:write yielded write-acl, so a sharee could reshare")
	}

	// Expansion must not repeat a privilege named both directly and via an
	// aggregate.
	both := webdav.PrivilegeSet{webdav.PrivilegeWrite, webdav.PrivilegeBind}
	var binds int
	for _, p := range both.Expanded() {
		if p == webdav.PrivilegeBind {
			binds++
		}
	}
	if binds != 1 {
		t.Errorf("bind appeared %d times in the expanded set, want 1", binds)
	}
}

// RFC 3744 §5.3: an abstract privilege cannot be granted on its own, so it must
// not reach the wire as one a principal holds. This server publishes DAV:all as
// abstract, and Expanded is the only path from a Backend's shorthand to the
// DAV:privilege elements a client reads.
func TestExpandedDropsAbstractPrivileges(t *testing.T) {
	for _, p := range (webdav.PrivilegeSet{webdav.PrivilegeAll}).Expanded() {
		if p == webdav.PrivilegeAll {
			t.Error("Expanded reported the abstract DAV:all as a held privilege")
		}
	}
	// Dropping it must not drop what it contains.
	if !(webdav.PrivilegeSet{webdav.PrivilegeAll}).Has(webdav.PrivilegeWriteACL) {
		t.Error("DAV:all stopped granting write-acl")
	}
}

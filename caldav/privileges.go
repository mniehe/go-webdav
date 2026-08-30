package caldav

import (
	"encoding/xml"

	"github.com/mniehe/davkit/internal"
)

// The privileges this handler reports. Most are RFC 3744's, in the DAV:
// namespace; read-free-busy is RFC 4791 §6.1.1 and lives in the CalDAV one,
// which is what makes it expressible at all — DAV:read is all-or-nothing, and
// a free-busy share is neither.
var (
	privRead                        = xml.Name{Space: internal.Namespace, Local: "read"}
	privReadFreeBusy                = xml.Name{Space: namespace, Local: "read-free-busy"}
	privReadCurrentUserPrivilegeSet = xml.Name{Space: internal.Namespace, Local: "read-current-user-privilege-set"}
	privWrite                       = xml.Name{Space: internal.Namespace, Local: "write"}
	privWriteContent                = xml.Name{Space: internal.Namespace, Local: "write-content"}
	privWriteProperties             = xml.Name{Space: internal.Namespace, Local: "write-properties"}
	privBind                        = xml.Name{Space: internal.Namespace, Local: "bind"}
	privUnbind                      = xml.Name{Space: internal.Namespace, Local: "unbind"}
)

// privilegesFor renders what an actor may do as the privileges the RFCs name
// for it. Order is fixed so the same permissions always produce the same
// property.
//
// The translation is one-way on purpose. Domain permissions are the truth and
// privileges are how they are described to a client, so a privilege only ever
// appears here because a permission the handler actually enforces put it there.
//
// The set is expanded rather than aggregated: RFC 3744 §5.4 defines
// current-user-privilege-set as the privileges the server grants, and a client
// tests it for the one privilege it needs rather than walking the containment
// tree itself.
func privilegesFor(perms CalendarPermissions, caps capabilities) []xml.Name {
	var names []xml.Name
	add := func(name xml.Name) { names = append(names, name) }

	if perms.ViewDetails {
		add(privRead)
	}
	// RFC 4791 §6.1.1 aggregates read-free-busy into DAV:read, so an actor who
	// can read the items holds both. Reporting DAV:read for a free-busy share
	// would be the drift this whole mapping exists to prevent: GET refuses it.
	if perms.ViewAvailability {
		add(privReadFreeBusy)
	}
	// RFC 3744 §3.7 keeps this separate from DAV:read so a principal can see its
	// own access without being able to read the collection. Anyone who reaches
	// this property is served it, so it is always reported.
	add(privReadCurrentUserPrivilegeSet)
	// DAV:read-acl is deliberately never advertised: the handler serves no
	// DAV:acl property, so reporting the privilege would promise a read that
	// always answers 404. It returns here once the ACL surface exists.

	// Every write privilege is intersected with the capability that would carry
	// it out. Advertising DAV:bind on a backend with no ItemWriter would promise
	// a PUT that answers 405.
	replace := perms.ReplaceItems && caps.writesItems
	create := perms.CreateItems && caps.writesItems
	remove := perms.DeleteItems && caps.writesItems
	settings := perms.UpdateSettings && caps.updatesCalendars

	if replace {
		add(privWriteContent)
	}
	if settings {
		add(privWriteProperties)
	}
	if create {
		add(privBind)
	}
	if remove {
		add(privUnbind)
	}
	// DAV:write aggregates the four above (RFC 3744 §3.2), so it is granted only
	// when all four are. Clients read it to decide whether to offer editing at
	// all; reporting it on a partial grant would offer an edit that fails.
	if replace && settings && create && remove {
		add(privWrite)
	}
	return names
}

// privilegeSet renders the property. internal.NewCurrentUserPrivilegeSet takes
// bare locals and so can only name DAV: privileges, which read-free-busy is not.
func privilegeSet(names []xml.Name) *internal.CurrentUserPrivilegeSet {
	set := &internal.CurrentUserPrivilegeSet{Privilege: make([]internal.Privilege, 0, len(names))}
	for _, name := range names {
		set.Privilege = append(set.Privilege, internal.Privilege{
			Raw: []internal.RawXMLValue{*internal.NewRawXMLElement(name, nil, nil)},
		})
	}
	return set
}

// capabilities is which optional interfaces a backend implements. It is
// resolved once, at construction, so that no request pays for the type
// assertions and no two requests can disagree about what the backend can do.
type capabilities struct {
	writesItems      bool
	syncs            bool
	createsCalendars bool
	updatesCalendars bool
	deletesCalendars bool
	transfersItems   bool
}

func capabilitiesOf(b Backend) capabilities {
	var caps capabilities
	_, caps.writesItems = b.(ItemWriter)
	_, caps.syncs = b.(SyncBackend)
	_, caps.createsCalendars = b.(CalendarCreator)
	_, caps.updatesCalendars = b.(CalendarUpdater)
	_, caps.deletesCalendars = b.(CalendarDeleter)
	_, caps.transfersItems = b.(ItemTransferer)
	return caps
}

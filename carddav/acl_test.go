package carddav_test

import (
	"context"
	"encoding/xml"
	"testing"

	"github.com/mniehe/davkit/carddav"
	"github.com/mniehe/davkit/carddavmem"
)

// sharingBackend implements carddav.SharingBackend on top of the memory store,
// the only way to switch the shares capability on in a test.
type sharingBackend struct{ *carddavmem.Store }

func (sharingBackend) Shares(context.Context, carddav.AddressBookRef) ([]carddav.Share, error) {
	return nil, nil
}

// A backend that can report shares must not cause DAV:read-acl to be
// advertised: the handler serves no DAV:acl property, so the privilege would
// promise a read that always answers 404.
func TestPropFindDoesNotAdvertiseReadACL(t *testing.T) {
	h := handlerFor(t, sharingBackend{newStore(t)}, carddav.Config{})

	assertPrivileges(t, h, "/alice/work/",
		[]xml.Name{davName("read"), davName("write")},
		[]xml.Name{davName("read-acl")})
}

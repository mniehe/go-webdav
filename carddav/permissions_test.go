package carddav

import "testing"

func TestZeroPermissionsDenyEverything(t *testing.T) {
	if (AddressBookPermissions{}).Any() {
		t.Error("the zero AddressBookPermissions grants something")
	}
	if (AccountPermissions{}).Any() {
		t.Error("the zero AccountPermissions grants something")
	}
}

func TestPermissionConstructors(t *testing.T) {
	view := ViewOnlyPermissions()
	if !view.ViewDetails {
		t.Error("ViewOnlyPermissions cannot read")
	}
	if view.CreateItems || view.ReplaceItems || view.DeleteItems || view.UpdateSettings || view.DeleteBook {
		t.Errorf("ViewOnlyPermissions can write: %+v", view)
	}

	edit := EditPermissions()
	if !edit.ViewDetails || !edit.CreateItems || !edit.ReplaceItems || !edit.DeleteItems {
		t.Errorf("EditPermissions cannot edit: %+v", edit)
	}
	if edit.DeleteBook || edit.UpdateSettings {
		t.Errorf("EditPermissions can change the address book itself: %+v", edit)
	}

	owner := OwnerPermissions()
	if !owner.UpdateSettings || !owner.DeleteBook || !owner.CreateItems {
		t.Errorf("OwnerPermissions cannot do everything: %+v", owner)
	}
}

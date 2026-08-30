package caldav

import "testing"

func TestViewDetailsImpliesViewAvailability(t *testing.T) {
	got := CalendarPermissions{ViewDetails: true}.Normalised()
	if !got.ViewAvailability {
		t.Error("ViewDetails alone does not normalise to ViewAvailability, so an authorizer has to know to set both")
	}

	if (CalendarPermissions{ViewAvailability: true}).Normalised().ViewDetails {
		t.Error("ViewAvailability implied ViewDetails; that is the wrong direction and leaks the items themselves")
	}
}

func TestZeroPermissionsDenyEverything(t *testing.T) {
	if (CalendarPermissions{}).Any() {
		t.Error("the zero CalendarPermissions grants something")
	}
	if (AccountPermissions{}).Any() {
		t.Error("the zero AccountPermissions grants something")
	}
	if (CalendarPermissions{}).Normalised().Any() {
		t.Error("normalising the zero value granted something")
	}
}

func TestPermissionConstructors(t *testing.T) {
	view := ViewOnlyPermissions()
	if !view.ViewDetails || !view.ViewAvailability {
		t.Error("ViewOnlyPermissions cannot read")
	}
	if view.CreateItems || view.ReplaceItems || view.DeleteItems || view.UpdateSettings || view.DeleteCalendar {
		t.Errorf("ViewOnlyPermissions can write: %+v", view)
	}

	busy := AvailabilityOnlyPermissions()
	if busy.ViewDetails {
		t.Error("AvailabilityOnlyPermissions can see the items themselves")
	}
	if !busy.ViewAvailability {
		t.Error("AvailabilityOnlyPermissions cannot see busy times")
	}

	edit := EditPermissions()
	if !edit.CreateItems || !edit.ReplaceItems || !edit.DeleteItems {
		t.Errorf("EditPermissions cannot edit: %+v", edit)
	}
	if edit.DeleteCalendar || edit.UpdateSettings {
		t.Errorf("EditPermissions can change the calendar itself: %+v", edit)
	}

	owner := OwnerPermissions()
	if !owner.UpdateSettings || !owner.DeleteCalendar || !owner.CreateItems {
		t.Errorf("OwnerPermissions cannot do everything: %+v", owner)
	}
}

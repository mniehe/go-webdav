package caldav

import (
	"slices"
	"testing"
)

func TestItemKindsZeroValueAcceptsEverything(t *testing.T) {
	var zero ItemKinds
	for _, kind := range allItemKinds {
		if !zero.Allows(kind) {
			t.Errorf("the zero ItemKinds rejects %s; a calendar nobody configured must take anything", kind)
		}
	}
	if got := zero.Kinds(); !slices.Equal(got, allItemKinds[:]) {
		t.Errorf("Kinds() = %v, want %v", got, allItemKinds)
	}
	if zero != AllItemKinds() {
		t.Error("AllItemKinds is not the zero value")
	}
}

func TestOnlyItemKinds(t *testing.T) {
	only := OnlyItemKinds(Task, Event)

	if !only.Allows(Event) || !only.Allows(Task) {
		t.Error("a listed kind was rejected")
	}
	if only.Allows(Note) || only.Allows(Availability) {
		t.Error("an unlisted kind was accepted")
	}
	if got, want := only.Kinds(), []ItemKind{Event, Task}; !slices.Equal(got, want) {
		t.Errorf("Kinds() = %v, want %v — declaration order, not argument order", got, want)
	}
}

func TestOnlyItemKindsWithNoneAcceptsNothing(t *testing.T) {
	none := OnlyItemKinds()
	for _, kind := range allItemKinds {
		if none.Allows(kind) {
			t.Errorf("OnlyItemKinds() accepts %s; an empty restriction is not the same as no restriction", kind)
		}
	}
	if got := none.Kinds(); len(got) != 0 {
		t.Errorf("Kinds() = %v, want none", got)
	}
}

func TestItemKindValidity(t *testing.T) {
	for _, kind := range allItemKinds {
		if !kind.IsValid() {
			t.Errorf("%d is not valid but is in allItemKinds", kind)
		}
		if kind.String() == "unknown item kind" {
			t.Errorf("%d has no name", kind)
		}
	}
	for _, kind := range []ItemKind{0, Availability + 1, 255} {
		if kind.IsValid() {
			t.Errorf("%d reports valid", kind)
		}
		if AllItemKinds().Allows(kind) {
			t.Errorf("AllItemKinds accepts %d, which is not a kind at all", kind)
		}
	}
}

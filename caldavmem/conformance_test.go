package caldavmem_test

import (
	"context"
	"testing"

	"github.com/mniehe/davkit/caldav"
	"github.com/mniehe/davkit/caldavmem"
	"github.com/mniehe/davkit/caldavtest"
)

type harness struct {
	store *caldavmem.Store
}

func newHarness(_ context.Context, _ *testing.T) (caldavtest.Harness, error) {
	return &harness{store: caldavmem.New()}, nil
}

// Seed goes through the same public methods a server would, so a fixture can
// only reach a state a client could have reached.
func (h *harness) Seed(ctx context.Context, f caldavtest.Fixture) error {
	for i := range f.Calendars {
		fc := &f.Calendars[i]
		if _, err := h.store.CompareAndCreateCalendar(ctx, fc.Ref.Account, caldav.CreateCalendarRequest{
			Name:        fc.Ref.Calendar,
			DisplayName: fc.Settings.DisplayName,
			Description: fc.Settings.Description,
			Color:       fc.Settings.Color,
			Timezone:    fc.Settings.Timezone,
			Accepts:     fc.Settings.Accepts,
		}, caldav.IfTargetMissing()); err != nil {
			return err
		}
		for _, viewer := range fc.Viewers {
			if err := h.store.Share(fc.Ref, viewer); err != nil {
				return err
			}
		}
		for _, item := range fc.Items {
			ref := caldav.ItemRef{Calendar: fc.Ref, Item: item.Name}
			if _, err := h.store.CompareAndStoreItem(ctx, ref, caldav.StoreItemRequest{
				Content:       item.Content,
				ContentID:     item.ContentID,
				Kind:          caldav.Event,
				Preconditions: caldav.IfTargetMissing(),
				MayCreate:     true,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (h *harness) Backend(context.Context) (caldav.Backend, error) { return h.store, nil }

func (h *harness) Close() {}

func (h *harness) PruneHistory(ctx context.Context, ref caldav.CalendarRef, before caldav.Revision) error {
	return h.store.PruneHistory(ctx, ref, before)
}

// TestConformance requires every capability except Reopener. Nothing here
// outlives the process, so a Reopen would hand back the same maps and pass
// without proving anything — the honest answer is not to claim durability.
func TestConformance(t *testing.T) {
	caldavtest.Conformance(t, newHarness, caldavtest.Require(
		caldavtest.CapItemWriter,
		caldavtest.CapSyncBackend,
		caldavtest.CapCalendarCreator,
		caldavtest.CapCalendarUpdater,
		caldavtest.CapCalendarDeleter,
		caldavtest.CapSharingBackend,
		caldavtest.CapPruner,
	))
}

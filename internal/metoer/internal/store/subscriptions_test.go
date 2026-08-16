package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/metoer/internal/model"
	"src.solsynth.dev/sosys/metoer/internal/store"
)

// TestListActivatedSubscriptionsAppFilter pins the delivery-time app scoping:
// a notification for a non-default app must only reach subscriptions
// registered under that app; a default-app notification also reaches legacy
// NULL/empty app_id rows; no resolved app id disables the filter.
func TestListActivatedSubscriptionsAppFilter(t *testing.T) {
	database := contractDatabase(t)
	st := store.New(database)
	ctx := context.Background()
	account := uuid.New()
	now := time.Now().UTC()

	appA := "dev.solsynth.solian"
	appB := "dev.solsynth.maid"
	empty := ""
	base := func(device string) store.PushSubscriptionEntity {
		return store.PushSubscriptionEntity{
			ID:             uuid.New(),
			EntityBase:     store.EntityBase{CreatedAt: now, UpdatedAt: now},
			AccountID:      account,
			DeviceID:       device,
			DeviceToken:    "token-" + device,
			Provider:       int(model.PushProviderGoogle),
			CountDelivered: 0,
			IsActivated:    true,
		}
	}
	rows := []store.PushSubscriptionEntity{
		func() store.PushSubscriptionEntity { e := base("dev-a"); e.AppID = &appA; return e }(),
		func() store.PushSubscriptionEntity { e := base("dev-b"); e.AppID = &appB; return e }(),
		func() store.PushSubscriptionEntity { e := base("dev-legacy"); e.AppID = nil; return e }(),
		func() store.PushSubscriptionEntity { e := base("dev-empty"); e.AppID = &empty; return e }(),
		func() store.PushSubscriptionEntity { e := base("dev-off"); e.AppID = &appA; e.IsActivated = false; return e }(),
	}
	for _, row := range rows {
		if err := database.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}

	deviceSet := func(got []*model.PushSubscription) map[string]bool {
		ids := map[string]bool{}
		for _, sub := range got {
			ids[sub.DeviceId] = true
		}
		return ids
	}

	// Notification targets a non-default app: only that app's subscriptions.
	got, err := st.ListActivatedSubscriptions(ctx, account, &appB, &appA)
	if err != nil {
		t.Fatal(err)
	}
	if ids := deviceSet(got); len(got) != 1 || !ids["dev-b"] {
		t.Fatalf("non-default app filter: got %v, want only dev-b", ids)
	}

	// Notification targets the default app: its rows plus legacy NULL/empty.
	got, err = st.ListActivatedSubscriptions(ctx, account, &appA, &appA)
	if err != nil {
		t.Fatal(err)
	}
	if ids := deviceSet(got); len(got) != 3 || !ids["dev-a"] || !ids["dev-legacy"] || !ids["dev-empty"] {
		t.Fatalf("default app filter: got %v, want dev-a + dev-legacy + dev-empty", ids)
	}

	// No app targeting: every activated subscription (inactive excluded).
	got, err = st.ListActivatedSubscriptions(ctx, account, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ids := deviceSet(got); len(got) != 4 || ids["dev-off"] {
		t.Fatalf("unfiltered: got %v, want the four activated rows", ids)
	}
}

package redis

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestClearActorPermissionCachePurgesPermissionValues(t *testing.T) {
	ctx := context.Background()
	rc, err := Connect(ctx, "localhost:6379", "", 0)
	if err != nil {
		t.Skipf("redis unavailable: %v", err)
	}
	t.Cleanup(func() { _ = rc.Raw.Close() })

	actorID := uuid.NewString()
	actor := "group:cache-test-" + actorID + "*bar"
	other := "group:cache-test-" + actorID + "Xbar"
	actorKey := "perm:0:" + actor + ":permissions.manage"
	otherKey := "perm:0:" + other + ":permissions.manage"
	t.Cleanup(func() {
		_ = rc.Cache.Remove(ctx, actorKey)
		_ = rc.Cache.Remove(ctx, otherKey)
	})
	if err := rc.Cache.Set(ctx, actorKey, true, time.Minute); err != nil {
		t.Fatalf("seed actor permission cache: %v", err)
	}
	if err := rc.Cache.Set(ctx, otherKey, true, time.Minute); err != nil {
		t.Fatalf("seed other permission cache: %v", err)
	}

	rc.ClearActorPermissionCache(ctx, actor)

	if found, err := rc.Cache.Get(ctx, actorKey, new(bool)); err != nil {
		t.Fatalf("read actor permission cache: %v", err)
	} else if found {
		t.Fatal("actor permission cache entry was not purged")
	}
	if found, err := rc.Cache.Get(ctx, otherKey, new(bool)); err != nil {
		t.Fatalf("read other permission cache: %v", err)
	} else if !found {
		t.Fatal("permission cache entry for another actor was purged")
	}
}

package cache

import (
	"context"
	"testing"
	"time"
)

func TestMemoryCacheSetGet(t *testing.T) {
	c := NewMemoryCacheService(100)
	defer c.Close()

	ctx := context.Background()

	type val struct {
		Name string `json:"name"`
	}

	if err := c.Set(ctx, "test:key", val{Name: "hello"}, time.Minute); err != nil {
		t.Fatalf("set: %v", err)
	}

	var got val
	found, err := c.Get(ctx, "test:key", &got)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !found {
		t.Fatal("expected cache hit")
	}
	if got.Name != "hello" {
		t.Errorf("expected 'hello', got %q", got.Name)
	}
}

func TestMemoryCacheMiss(t *testing.T) {
	c := NewMemoryCacheService(100)
	defer c.Close()

	ctx := context.Background()

	var got string
	found, err := c.Get(ctx, "nonexistent", &got)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if found {
		t.Fatal("expected cache miss")
	}
}

func TestMemoryCacheRemove(t *testing.T) {
	c := NewMemoryCacheService(100)
	defer c.Close()

	ctx := context.Background()

	if err := c.Set(ctx, "test:key", "value", time.Minute); err != nil {
		t.Fatalf("set: %v", err)
	}

	if err := c.Remove(ctx, "test:key"); err != nil {
		t.Fatalf("remove: %v", err)
	}

	var got string
	found, err := c.Get(ctx, "test:key", &got)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if found {
		t.Fatal("expected cache miss after remove")
	}
}

func TestMemoryCacheTTLExpiration(t *testing.T) {
	c := NewMemoryCacheService(100)
	defer c.Close()

	ctx := context.Background()

	if err := c.Set(ctx, "test:ttl", "value", 50*time.Millisecond); err != nil {
		t.Fatalf("set: %v", err)
	}

	// Should be present immediately
	var got string
	found, err := c.Get(ctx, "test:ttl", &got)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !found {
		t.Fatal("expected cache hit before expiration")
	}

	// Wait for expiration
	time.Sleep(100 * time.Millisecond)

	found, err = c.Get(ctx, "test:ttl", &got)
	if err != nil {
		t.Fatalf("get after expiry: %v", err)
	}
	if found {
		t.Fatal("expected cache miss after expiration")
	}
}

func TestMemoryCacheNoTTL(t *testing.T) {
	c := NewMemoryCacheService(100)
	defer c.Close()

	ctx := context.Background()

	// Zero TTL means no expiration
	if err := c.Set(ctx, "test:nottl", "value", 0); err != nil {
		t.Fatalf("set: %v", err)
	}

	var got string
	found, err := c.Get(ctx, "test:nottl", &got)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !found {
		t.Fatal("expected cache hit for no-TTL key")
	}
	if got != "value" {
		t.Errorf("expected 'value', got %q", got)
	}
}

func TestMemoryCacheEviction(t *testing.T) {
	c := NewMemoryCacheService(3)
	defer c.Close()

	ctx := context.Background()

	for i := 0; i < 5; i++ {
		key := string(rune('a'+i)) + ":key"
		if err := c.Set(ctx, key, i, time.Minute); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}

	// First two keys should be evicted (LRU)
	var got int
	found, _ := c.Get(ctx, "a:key", &got)
	if found {
		t.Error("expected 'a:key' to be evicted")
	}

	found, _ = c.Get(ctx, "b:key", &got)
	if found {
		t.Error("expected 'b:key' to be evicted")
	}

	// Last three should still be present
	for i := 2; i < 5; i++ {
		key := string(rune('a'+i)) + ":key"
		found, _ = c.Get(ctx, key, &got)
		if !found {
			t.Errorf("expected %q to be present", key)
		}
	}
}

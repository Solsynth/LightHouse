package auth

import (
	"context"
	"time"

	"src.solsynth.dev/sosys/lighthouse/shared/pkg/cache"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	profileCacheTTL        = 5 * time.Minute
	lastSeenTouchThrottle  = time.Minute
)

// HydrateProfile fetches the account profile via gRPC and caches it.
// If the profile is already cached, it is loaded from cache instead.
func HydrateProfile(ctx context.Context, c cache.CacheService, profiles gen.DyProfileServiceClient, account *gen.DyAccount) error {
	if account == nil || account.Id == "" {
		return nil
	}
	if profiles == nil {
		return nil
	}

	cacheKey := ProfileCacheKey(account.Id)

	// Check cache
	if c != nil {
		var cachedProfile gen.DyAccountProfile
		found, err := c.Get(ctx, cacheKey, &cachedProfile)
		if err == nil && found {
			account.Profile = &cachedProfile
			return nil
		}
	}

	// Cache miss — fetch via gRPC
	profile, err := profiles.GetProfile(ctx, &gen.DyGetProfileRequest{AccountId: account.Id})
	if err != nil {
		st, ok := status.FromError(err)
		if ok && (st.Code() == codes.NotFound || st.Code() == codes.Unimplemented) {
			account.Profile = &gen.DyAccountProfile{}
			return nil
		}
		return err
	}

	account.Profile = profile
	if c != nil {
		_ = c.Set(ctx, cacheKey, profile, profileCacheTTL)
	}
	return nil
}

// TouchProfileLastSeen updates the account's last_seen_at timestamp,
// throttled to once per minute per account.
func TouchProfileLastSeen(ctx context.Context, c cache.CacheService, profiles gen.DyProfileServiceClient, accountID string) error {
	if accountID == "" || profiles == nil {
		return nil
	}

	throttleKey := LastSeenTouchKey(accountID)

	// Check throttle
	if c != nil {
		var touched bool
		found, err := c.Get(ctx, throttleKey, &touched)
		if err == nil && found && touched {
			return nil
		}
	}

	// Update last_seen_at
	now := timestamppb.Now()
	_, err := profiles.UpdateProfile(ctx, &gen.DyUpdateProfileRequest{
		AccountId: accountID,
		Profile: &gen.DyAccountProfile{
			LastSeenAt: now,
		},
		UpdateMask: &fieldmaskpb.FieldMask{
			Paths: []string{"last_seen_at"},
		},
	})
	if err != nil {
		st, ok := status.FromError(err)
		if ok && (st.Code() == codes.NotFound || st.Code() == codes.Unimplemented) {
			return nil
		}
		return err
	}

	// Set throttle key
	if c != nil {
		_ = c.Set(ctx, throttleKey, true, lastSeenTouchThrottle)
	}
	return nil
}

// HydrateAndTouch is a convenience that calls HydrateProfile then TouchProfileLastSeen.
func HydrateAndTouch(ctx context.Context, c cache.CacheService, profiles gen.DyProfileServiceClient, result *AuthResult) error {
	if result.Account == nil {
		return nil
	}
	if err := HydrateProfile(ctx, c, profiles, result.Account); err != nil {
		return err
	}
	return TouchProfileLastSeen(ctx, c, profiles, result.Account.Id)
}

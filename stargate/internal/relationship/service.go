// Package relationship is the read/check half of the social graph. It is the
// Go port of Passport's RelationshipService reads: the same Redis cache keys
// the C# fleet uses (accounts:friends:, accounts:blocked:, accounts:muted:,
// accounts:close_friends: with the :False/:True direction suffix) back both
// the HTTP controller and the gRPC surface, so sibling services never read a
// cache the other half forgot to purge.
//
// The module owns its table (account_relationships, see
// internal/modules/relationships). Nothing outside this package queries it.
package relationship

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/redis"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// Cache prefixes and TTL mirror RelationshipService (C#) exactly: the C#
// fleet and this service share the same Redis keys, so a relationship write
// in Stargate must invalidate what Passport's caches hold, and vice versa.
const (
	CacheFriends = "accounts:friends:"
	CacheBlocked = "accounts:blocked:"
	CacheMuted   = "accounts:muted:"
	CacheClose   = "accounts:close_friends:"
	CacheTTL     = time.Hour
)

// Service answers relationship reads against the module-owned table.
type Service struct {
	Store *store.Store
	Redis *redis.Client
	Log   *slog.Logger
}

// New builds the read service. Redis is optional: a nil client disables the
// cache and every read goes to the database.
func New(st *store.Store, rc *redis.Client, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{Store: st, Redis: rc, Log: log}
}

// Available reports whether the service can be used. A nil service (disabled
// relationships module) is never available.
func (s *Service) Available() bool { return s != nil && s.Store != nil }

// GetRelationship loads one directed row. status/ignoreExpired/includeDeleted
// mirror BuildRelationshipQuery. A missing row returns store.ErrNotFound.
func (s *Service) GetRelationship(ctx context.Context, accountID, relatedID uuid.UUID, status *model.RelationshipStatus, ignoreExpired, includeDeleted bool) (*model.Relationship, error) {
	return s.Store.GetRelationship(ctx, accountID, relatedID, status, ignoreExpired, includeDeleted)
}

// HasExisting reports whether a row exists in either direction
// (RelationshipService.HasExistingRelationship).
func (s *Service) HasExisting(ctx context.Context, accountID, relatedID uuid.UUID) (bool, error) {
	return s.Store.HasExistingRelationship(ctx, accountID, relatedID)
}

// HasStatus reports whether a directed row carries exactly status. This is the
// strict check BuildRelationshipQuery performs: Friends does NOT include
// CloseFriend rows (use ListRelated with RelationshipFriends for the
// friends-family membership).
func (s *Service) HasStatus(ctx context.Context, accountID, relatedID uuid.UUID, status model.RelationshipStatus) (bool, error) {
	rel, err := s.Store.GetRelationship(ctx, accountID, relatedID, &status, false, false)
	if err != nil {
		if err == store.ErrNotFound {
			return false, nil
		}
		return false, err
	}
	return rel != nil, nil
}

// IsBlockedEitherDirection mirrors RelationshipService.IsBlockedEitherDirection:
// either side blocking the other, non-expired, under the shared either cache.
func (s *Service) IsBlockedEitherDirection(ctx context.Context, accountID, relatedID uuid.UUID) (bool, error) {
	if accountID == uuid.Nil || relatedID == uuid.Nil || accountID == relatedID {
		return false, nil
	}
	smaller, larger := accountID.String(), relatedID.String()
	if smaller > larger {
		smaller, larger = larger, smaller
	}
	key := CacheBlocked + "either:" + smaller + ":" + larger
	if s.Redis != nil && s.Redis.Cache != nil {
		var cached bool
		if found, err := s.Redis.Cache.Get(ctx, key, &cached); err == nil && found {
			return cached, nil
		}
	}
	blocked, err := s.Store.IsBlockedEitherDirection(ctx, accountID, relatedID)
	if err != nil {
		return false, err
	}
	if s.Redis != nil && s.Redis.Cache != nil {
		if err := s.Redis.Cache.Set(ctx, key, blocked, CacheTTL); err != nil {
			s.Log.Warn("set blocked-either cache", "key", key, "error", err)
		}
	}
	return blocked, nil
}

// ListRelated returns the ids on the far side of the account's non-expired
// rows with status. isRelated flips the direction: true lists the accounts
// that have accountID on their related side. RelationshipFriends matches
// Friends OR CloseFriend rows (GetCachedRelationships), which is the
// friends-family list the fleet expects from ListFriends.
func (s *Service) ListRelated(ctx context.Context, accountID uuid.UUID, status model.RelationshipStatus, isRelated bool) ([]string, error) {
	key := cacheKey(status, accountID, isRelated)
	if s.Redis != nil && s.Redis.Cache != nil {
		var cached []string
		if found, err := s.Redis.Cache.Get(ctx, key, &cached); err == nil && found {
			return cached, nil
		}
	}
	ids, err := s.Store.ListRelatedAccountIDs(ctx, accountID, status, isRelated)
	if err != nil {
		return nil, err
	}
	if s.Redis != nil && s.Redis.Cache != nil {
		if err := s.Redis.Cache.Set(ctx, key, ids, CacheTTL); err != nil {
			s.Log.Warn("set relationship cache", "key", key, "error", err)
		}
	}
	return ids, nil
}

// ListAllBlocked returns the distinct non-expired ids blocked in either
// direction (RelationshipService.ListAllBlockedAccountIds).
func (s *Service) ListAllBlocked(ctx context.Context, accountID uuid.UUID) ([]string, error) {
	key := CacheBlocked + "all:" + accountID.String()
	if s.Redis != nil && s.Redis.Cache != nil {
		var cached []string
		if found, err := s.Redis.Cache.Get(ctx, key, &cached); err == nil && found {
			return cached, nil
		}
	}
	ids, err := s.Store.ListAllBlockedAccountIDs(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if s.Redis != nil && s.Redis.Cache != nil {
		if err := s.Redis.Cache.Set(ctx, key, ids, CacheTTL); err != nil {
			s.Log.Warn("set blocked-all cache", "key", key, "error", err)
		}
	}
	return ids, nil
}

// cacheKey mirrors GetCachedRelationships' key shape: prefix + id + ":" +
// isRelated, so the C# reader and this service hit the same entry.
func cacheKey(status model.RelationshipStatus, accountID uuid.UUID, isRelated bool) string {
	var prefix string
	switch status {
	case model.RelationshipBlocked:
		prefix = CacheBlocked
	case model.RelationshipMuted:
		prefix = CacheMuted
	case model.RelationshipCloseFriend:
		prefix = CacheClose
	default:
		prefix = CacheFriends
	}
	suffix := "False"
	if isRelated {
		suffix = "True"
	}
	return prefix + accountID.String() + ":" + suffix
}

// PurgeCache mirrors PurgeRelationshipCache (a superset: the C# purges the
// keys without the :False/:True suffix for friends/blocked, this purges every
// variant so a sibling read can never go stale).
func (s *Service) PurgeCache(ctx context.Context, accountID, relatedID uuid.UUID, statuses ...model.RelationshipStatus) {
	if s == nil || s.Redis == nil || s.Redis.Cache == nil {
		return
	}
	has := func(want model.RelationshipStatus) bool {
		for _, status := range statuses {
			if status == want {
				return true
			}
		}
		return false
	}
	a, b := accountID.String(), relatedID.String()
	keys := map[string]bool{}
	for _, id := range []string{a, b} {
		if has(model.RelationshipFriends) || has(model.RelationshipPending) {
			keys[CacheFriends+id] = true
			keys[CacheFriends+id+":False"] = true
			keys[CacheFriends+id+":True"] = true
		}
		if has(model.RelationshipBlocked) {
			keys[CacheBlocked+id] = true
			keys[CacheBlocked+id+":False"] = true
			keys[CacheBlocked+id+":True"] = true
			keys[CacheBlocked+"all:"+id] = true
		}
		if has(model.RelationshipMuted) {
			keys[CacheMuted+id+":False"] = true
		}
		if has(model.RelationshipCloseFriend) {
			keys[CacheClose+id+":False"] = true
		}
	}
	if has(model.RelationshipBlocked) {
		smaller, larger := a, b
		if smaller > larger {
			smaller, larger = b, a
		}
		keys[CacheBlocked+"either:"+smaller+":"+larger] = true
	}
	for key := range keys {
		if err := s.Redis.Cache.Remove(ctx, key); err != nil {
			s.Log.Warn("purge relationship cache", "key", key, "error", err)
		}
	}
}

// Hydrate attaches Account/Related to each row. The C# uses
// AccountService.GetAccount, which also attaches the profile; a dangling
// target attaches nil rather than failing the read.
func (s *Service) Hydrate(ctx context.Context, relationships []model.Relationship) {
	for i := range relationships {
		if account, err := s.Store.GetAccountWithProfile(ctx, mustUUID(relationships[i].AccountId)); err == nil {
			relationships[i].Account = account
		}
		if related, err := s.Store.GetAccountWithProfile(ctx, mustUUID(relationships[i].RelatedId)); err == nil {
			relationships[i].Related = related
		}
	}
}

func mustUUID(value string) uuid.UUID {
	id, _ := uuid.Parse(value)
	return id
}

// sweepInterval and sweepBatchLimit mirror the C# RelationshipExpiryJob
// (a Quartz job firing every five minutes) and bound one pass so a long
// backlog drains over successive ticks instead of one unbounded transaction.
//
// sweepInterval is a var only so the ticker wiring can be exercised without
// waiting five minutes; nothing in the binary writes it.
const sweepBatchLimit = 500

var sweepInterval = 5 * time.Minute

// SweepExpired applies expired_at to the graph and purges the shared caches
// for every row it touched.
func (s *Service) SweepExpired(ctx context.Context) (store.RelationshipSweepCounts, error) {
	if s == nil || s.Store == nil {
		return store.RelationshipSweepCounts{}, nil
	}
	changed, counts, err := s.Store.SweepExpiredRelationships(ctx, sweepBatchLimit)
	if err != nil {
		return counts, err
	}
	for _, rel := range changed {
		// The row answers as its status before the sweep, and a degraded row
		// also becomes DegradeToStatus, so both key families are stale.
		statuses := []model.RelationshipStatus{rel.Status}
		if rel.DegradeToStatus != nil {
			statuses = append(statuses, *rel.DegradeToStatus)
		}
		s.PurgeCache(ctx, mustUUID(rel.AccountId), mustUUID(rel.RelatedId), statuses...)
	}
	return counts, nil
}

// PurgeAccount removes every relationship row naming the account on either
// side and purges the shared caches for each. It is what the
// accounts.removal.confirmed consumer calls, and it is a hard delete: the
// account is gone, so there is nothing left to reuse the row.
func (s *Service) PurgeAccount(ctx context.Context, accountID uuid.UUID) (int, error) {
	if s == nil || s.Store == nil {
		return 0, nil
	}
	rows, err := s.Store.PurgeAccountRelationships(ctx, accountID)
	if err != nil {
		return 0, err
	}
	for _, rel := range rows {
		statuses := []model.RelationshipStatus{rel.Status}
		if rel.DegradeToStatus != nil {
			statuses = append(statuses, *rel.DegradeToStatus)
		}
		s.PurgeCache(ctx, mustUUID(rel.AccountId), mustUUID(rel.RelatedId), statuses...)
	}
	return len(rows), nil
}

// StartSweeper runs SweepExpired every sweepInterval until ctx is cancelled.
//
// Passport's RelationshipExpiryJob was deleted along with the table it swept,
// so without this a timed block or mute never lapses (the reads keep
// answering "blocked" until the hour-long cache entry is refreshed from a row
// that is still status=Blocked) and an unaccepted friend request never
// resolves. The C# ran the same sweep every five minutes.
func (s *Service) StartSweeper(ctx context.Context) {
	if s == nil || s.Store == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(sweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				counts, err := s.SweepExpired(ctx)
				if err != nil {
					s.Log.Warn("relationship expiry sweep", "error", err)
					continue
				}
				if counts.Degraded > 0 || counts.Deleted > 0 {
					s.Log.Info("relationship expiry swept",
						"degraded", counts.Degraded, "deleted", counts.Deleted)
				}
			}
		}
	}()
}

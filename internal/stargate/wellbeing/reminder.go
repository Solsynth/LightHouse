package wellbeing

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// recentActivityWindow is Passport's "recently active" threshold.
const recentActivityWindow = 15 * time.Minute

// reminderTTL is how long a per-account/local-date/hour send is remembered;
// 26 hours covers the rest of the local day and any hour shift.
const reminderTTL = 26 * time.Hour

var errReminderAlreadySent = errors.New("wellbeing: night owl reminder already sent")

// localReminderSet is the process-local fallback used when Redis is not
// configured. Entries expire so a long-running process does not grow
// unboundedly.
type localReminderSet struct {
	mu      sync.Mutex
	entries map[string]time.Time
}

func (l *localReminderSet) claim(key string, now time.Time, ttl time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.entries == nil {
		l.entries = make(map[string]time.Time)
	}
	if expires, ok := l.entries[key]; ok && expires.After(now) {
		return false
	}
	l.entries[key] = now.Add(ttl)
	if len(l.entries) > 2048 {
		for candidate, expires := range l.entries {
			if !expires.After(now) {
				delete(l.entries, candidate)
			}
		}
	}
	return true
}

func (l *localReminderSet) release(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

// reminderAudienceRow is one recently active account plus the profile time
// zone needed to decide the local hour.
type reminderAudienceRow struct {
	AccountID uuid.UUID `gorm:"column:account_id"`
	Language  string    `gorm:"column:language"`
	TimeZone  *string   `gorm:"column:time_zone"`
}

// activeAudienceSQL selects accounts with a live presence lease or a profile
// seen in the last 15 minutes, mirroring NightOwlReminderJob.Execute.
const activeAudienceSQL = `
SELECT a.id AS account_id, a.language, p.time_zone
FROM accounts a
LEFT JOIN account_profiles p ON p.account_id = a.id AND p.deleted_at IS NULL
WHERE a.deleted_at IS NULL
	AND (
		EXISTS (
			SELECT 1 FROM presence_activities pa
			WHERE pa.account_id = a.id
				AND pa.deleted_at IS NULL
				AND pa.lease_expires_at > ?
		)
		OR p.last_seen_at >= ?
	)`

// executeNightOwl runs one sweep and never lets a panic escape the job.
func (s *Service) executeNightOwl(ctx context.Context) {
	if s == nil || !s.PushAvailable() {
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			s.logger().Error("night owl reminder job panicked", "panic", recovered)
		}
	}()
	sent, err := s.runNightOwl(ctx, time.Now().UTC())
	if err != nil {
		s.logger().Warn("night owl reminder sweep failed", "error", err)
		return
	}
	if sent > 0 {
		s.logger().Info("night owl reminders sent", "count", sent)
	}
}

func (s *Service) runNightOwl(ctx context.Context, now time.Time) (int, error) {
	var audience []reminderAudienceRow
	err := s.DB.WithContext(ctx).Raw(activeAudienceSQL, now, now.Add(-recentActivityWindow)).Scan(&audience).Error
	if err != nil {
		return 0, err
	}

	sent := 0
	for _, row := range audience {
		if row.TimeZone == nil {
			continue
		}
		zoneName := strings.TrimSpace(*row.TimeZone)
		if zoneName == "" {
			continue
		}
		location, err := time.LoadLocation(zoneName)
		if err != nil {
			s.logger().Debug("night owl: skipping invalid profile time zone",
				"account_id", row.AccountID, "time_zone", zoneName)
			continue
		}
		local := now.In(location)
		hour := local.Hour()
		if hour < 0 || hour > 2 {
			continue
		}
		key := reminderKey(row.AccountID, local, hour)
		claimed, err := s.claimReminder(ctx, key)
		if err != nil {
			s.logger().Warn("night owl: reminder dedupe failed",
				"account_id", row.AccountID, "error", err)
			continue
		}
		if !claimed {
			continue
		}
		variant := rand.IntN(3) + 1
		if err := s.SendNightOwl(ctx, row.AccountID.String(), row.Language, hour, variant); err != nil {
			s.logger().Warn("night owl: reminder push failed",
				"account_id", row.AccountID, "error", err)
			s.releaseReminder(ctx, key)
			continue
		}
		sent++
	}
	return sent, nil
}

func reminderKey(accountID uuid.UUID, local time.Time, hour int) string {
	return fmt.Sprintf("accounts:night-owl-reminder:%s:%s:%d",
		accountID, local.Format("2006-01-02"), hour)
}

// claimReminder reserves the send slot for a key. Redis uses a distributed
// lock plus the shared flag value (the same semantics as Passport's
// ExecuteWithLockAsync/HasFlagAsync); without Redis a process-local map is
// used.
func (s *Service) claimReminder(ctx context.Context, key string) (bool, error) {
	if s.Redis == nil || s.Redis.Cache == nil {
		return s.local.claim(key, time.Now(), reminderTTL), nil
	}
	cache := s.Redis.Cache
	locked, err := cache.ExecuteWithLock(ctx, key, time.Minute, 0, 100*time.Millisecond,
		func(lockCtx context.Context) error {
			exists, err := cache.HasFlag(lockCtx, key)
			if err != nil {
				return err
			}
			if exists {
				return errReminderAlreadySent
			}
			return cache.SetFlag(lockCtx, key, reminderTTL)
		})
	if errors.Is(err, errReminderAlreadySent) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return locked, nil
}

func (s *Service) releaseReminder(ctx context.Context, key string) {
	if s.Redis == nil || s.Redis.Cache == nil {
		s.local.release(key)
		return
	}
	if err := s.Redis.Cache.RemoveFlag(ctx, key); err != nil {
		s.logger().Warn("night owl: reminder release failed", "key", key, "error", err)
	}
}

// Package wellbeing ports Passport's account activity statistics and
// late-night ("night owl") reminder surface into Stargate.
//
// The package owns no tables: statistics are SQL aggregates over the core
// accounts/account_profiles tables, and reminder deduplication lives in Redis
// (with a process-local fallback when Redis is not configured).
package wellbeing

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"src.solsynth.dev/sosys/stargate/ports"
	"src.solsynth.dev/sosys/stargate/redis"
)

// ModuleID is the reverse-DNS module identifier.
const ModuleID = "app.solian.id.wellbeing"

// ReminderTopic is the push topic Passport used for sleep reminders.
const ReminderTopic = "accounts.wellbeing.sleep"

// ErrPushUnavailable reports that no push provider is enabled.
var ErrPushUnavailable = errors.New("wellbeing: push provider unavailable")

// Service implements account activity statistics and the night owl reminder
// sweep. DB and Cache may be nil in tests; Push is optional at runtime.
type Service struct {
	DB    *gorm.DB
	Push  ports.Push
	Redis *redis.Client
	Log   *slog.Logger

	local localReminderSet
}

// NewService builds the service. push may be nil when the push port is not
// available; the HTTP surface then answers 503 and the job stays idle.
func NewService(db *gorm.DB, push ports.Push, cache *redis.Client, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{DB: db, Push: push, Redis: cache, Log: log}
}

func (s *Service) logger() *slog.Logger {
	if s != nil && s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// PushAvailable reports whether a usable push provider is wired.
func (s *Service) PushAvailable() bool {
	return s != nil && s.Push != nil && s.Push.Available()
}

// SendNightOwl pushes one localized reminder variant to an account. The push
// port only carries title and body; Passport's topic, action URI and savable
// flag are provider defaults (see ReminderTopic).
func (s *Service) SendNightOwl(ctx context.Context, accountID, language string, hour, variant int) error {
	if !s.PushAvailable() {
		return ErrPushUnavailable
	}
	message := LookupNightOwl(language, hour, variant)
	return s.Push.SendToUser(ctx, accountID, message.Title, message.Body)
}

// JobInterval is the reminder sweep cadence, matching the C# Quartz job.
const JobInterval = 15 * time.Minute

// StartJobs launches the reminder sweep and returns a stop function that
// cancels it and waits for the goroutine. The first pass runs immediately.
func (s *Service) StartJobs(ctx context.Context) func() {
	if s == nil {
		return func() {}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.executeNightOwl(runCtx)
		ticker := time.NewTicker(JobInterval)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				s.executeNightOwl(runCtx)
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

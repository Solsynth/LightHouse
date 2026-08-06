// Package scheduler runs the Quartz-equivalent scheduled jobs on UTC
// schedules: AppDatabaseRecycling (daily 00:00), NotificationRetentionCleanup
// (daily 00:15), PushSubFlush (every 5 min), EmailSendingPlanAdvance (every
// 1 min).
package scheduler

import (
	"context"
	"log/slog"
	"time"

	"src.solsynth.dev/sosys/metoer/internal/email"
	"src.solsynth.dev/sosys/metoer/internal/push"
	"src.solsynth.dev/sosys/metoer/internal/store"
)

// Options carries the job dependencies.
type Options struct {
	Store *store.Store
	Push  *push.Service
	Plans *email.PlanService
	Log   *slog.Logger
}

// Run starts all four scheduler loops and blocks until ctx is cancelled.
func Run(ctx context.Context, opts Options) {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	go runDaily(ctx, log, "app_database_recycling", "0 0 0 * * ?", func(ctx context.Context) error {
		threshold := time.Now().UTC().Add(-7 * 24 * time.Hour)
		log.Info("deleting soft-deleted records...")
		return opts.Store.RecycleSoftDeleted(ctx, threshold)
	})
	go runDaily(ctx, log, "notification_retention_cleanup", "0 15 0 * * ?", func(ctx context.Context) error {
		count, err := opts.Store.DeleteExcessNotifications(ctx)
		if err != nil {
			return err
		}
		log.Info("deleted excess notifications, keeping max 100 per (account, app)", "count", count)
		return nil
	})
	go runInterval(ctx, log, "push_sub_flush", 5*time.Minute, func(ctx context.Context) error {
		return opts.Push.FlushRemovalBuffer(ctx)
	})
	go runInterval(ctx, log, "email_sending_plan_advance", time.Minute, func(ctx context.Context) error {
		return opts.Plans.AdvanceDuePlansAsync(ctx)
	})
}

// job is one scheduled task.
type job func(ctx context.Context) error

// runDaily runs fn once per UTC day at the given hour:minute (the Quartz
// cron "0 {minute} {hour} * * ?").
func runDaily(ctx context.Context, log *slog.Logger, name string, cronSpec string, fn job) {
	hour, minute := parseCronTime(cronSpec)
	for {
		now := time.Now().UTC()
		next := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, time.UTC)
		if !next.After(now) {
			next = next.Add(24 * time.Hour)
		}
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if err := fn(ctx); err != nil {
			log.Error("scheduled job failed", "job", name, "error", err)
		}
	}
}

// runInterval runs fn every interval, skipping the first tick only after the
// interval (matches Quartz's SimpleSchedule with repeat forever).
func runInterval(ctx context.Context, log *slog.Logger, name string, interval time.Duration, fn job) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := fn(ctx); err != nil {
				log.Error("scheduled job failed", "job", name, "error", err)
			}
		}
	}
}

// parseCronTime extracts the hour and minute from a Quartz cron
// "s m h * * ?" spec (the only form this scheduler uses).
func parseCronTime(spec string) (hour, minute int) {
	parts := stringsSplit(spec, " ")
	if len(parts) >= 3 {
		minute = atoiOrZero(parts[1])
		hour = atoiOrZero(parts[2])
	}
	return hour, minute
}

func atoiOrZero(s string) int {
	if s == "" {
		return 0
	}
	v := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		v = v*10 + int(r-'0')
	}
	return v
}

func stringsSplit(s, sep string) []string {
	var parts []string
	start := 0
	for i := 0; i+len(sep) <= len(s); i++ {
		if s[i:i+len(sep)] == sep {
			parts = append(parts, s[start:i])
			start = i + len(sep)
		}
	}
	parts = append(parts, s[start:])
	return parts
}

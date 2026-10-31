// Package jobs runs the scheduled background work the C# services delegated to
// Quartz. It supports fixed intervals and the subset of Quartz cron expressions
// the fleet actually uses (six fields: second, minute, hour, day-of-month,
// month, day-of-week, with `*`, `?`, `*/n` and numeric lists).
package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Scheduler owns the registered jobs and their goroutines.
type Scheduler struct {
	log   *slog.Logger
	ctx   context.Context
	mu    sync.Mutex
	wg    sync.WaitGroup
	start bool
}

// New creates a scheduler bound to ctx.
func New(ctx context.Context, log *slog.Logger) *Scheduler {
	if log == nil {
		log = slog.Default()
	}
	return &Scheduler{log: log, ctx: ctx}
}

// Every registers a job that runs at a fixed interval after an initial delay of
// the same interval.
func (s *Scheduler) Every(name string, interval time.Duration, fn func(context.Context) error) {
	if interval <= 0 || fn == nil {
		return
	}
	s.start = true
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-ticker.C:
				s.run(name, fn)
			}
		}
	}()
}

// Now registers a job that runs once immediately.
func (s *Scheduler) Now(name string, fn func(context.Context) error) {
	if fn == nil {
		return
	}
	s.start = true
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.run(name, fn)
	}()
}

// Cron registers a job that runs on a Quartz-style cron schedule.
func (s *Scheduler) Cron(name, expression string, fn func(context.Context) error) {
	schedule, err := ParseCron(expression)
	if err != nil {
		s.log.Error("invalid cron expression; job not scheduled", "job", name, "expression", expression, "error", err)
		return
	}
	if fn == nil {
		return
	}
	s.start = true
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		next := schedule.Next(time.Now())
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-time.After(time.Until(next)):
			}
			s.run(name, fn)
			next = schedule.Next(time.Now())
		}
	}()
}

func (s *Scheduler) run(name string, fn func(context.Context) error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			s.log.Error("scheduled job panicked", "job", name, "panic", recovered)
		}
	}()
	if err := fn(s.ctx); err != nil {
		s.log.Warn("scheduled job failed", "job", name, "error", err)
	}
}

// Wait blocks until every job goroutine has returned (after ctx is cancelled).
func (s *Scheduler) Wait() {
	if s == nil {
		return
	}
	s.wg.Wait()
}

// Cron is a parsed Quartz-style schedule.
type Cron struct {
	seconds  []int
	minutes  []int
	hours    []int
	days     []int
	months   []int
	weekdays []int
}

// ParseCron parses a six-field Quartz expression. Day-of-month, month and
// day-of-week are honored when they are restricted; `?` and `*` mean "any".
func ParseCron(expression string) (*Cron, error) {
	fields := strings.Fields(strings.TrimSpace(expression))
	if len(fields) != 6 {
		return nil, fmt.Errorf("expected 6 fields, got %d", len(fields))
	}
	parse := func(raw string, min, max int) ([]int, error) {
		if raw == "*" || raw == "?" {
			out := make([]int, 0, max-min+1)
			for v := min; v <= max; v++ {
				out = append(out, v)
			}
			return out, nil
		}
		var values []int
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if strings.HasPrefix(part, "*/") {
				step, err := strconv.Atoi(part[2:])
				if err != nil || step <= 0 {
					return nil, fmt.Errorf("invalid step %q", part)
				}
				for v := min; v <= max; v += step {
					values = append(values, v)
				}
				continue
			}
			v, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("invalid value %q", part)
			}
			if v < min || v > max {
				return nil, fmt.Errorf("value %d out of range %d..%d", v, min, max)
			}
			values = append(values, v)
		}
		if len(values) == 0 {
			return nil, fmt.Errorf("empty field %q", raw)
		}
		return values, nil
	}

	seconds, err := parse(fields[0], 0, 59)
	if err != nil {
		return nil, err
	}
	minutes, err := parse(fields[1], 0, 59)
	if err != nil {
		return nil, err
	}
	hours, err := parse(fields[2], 0, 23)
	if err != nil {
		return nil, err
	}
	days, err := parse(fields[3], 1, 31)
	if err != nil {
		return nil, err
	}
	months, err := parse(fields[4], 1, 12)
	if err != nil {
		return nil, err
	}
	weekdays, err := parse(fields[5], 0, 7)
	if err != nil {
		return nil, err
	}
	return &Cron{seconds: seconds, minutes: minutes, hours: hours, days: days, months: months, weekdays: weekdays}, nil
}

// Next returns the first matching instant strictly after t.
func (c *Cron) Next(after time.Time) time.Time {
	if c == nil {
		return after.Add(time.Hour)
	}
	t := after.Truncate(time.Second).Add(time.Second)
	// A yearly schedule can be almost a year away; bound the scan to 400 days.
	limit := t.Add(400 * 24 * time.Hour)
	for t.Before(limit) {
		if c.matches(t) {
			return t
		}
		t = t.Add(time.Second)
	}
	return limit
}

func (c *Cron) matches(t time.Time) bool {
	return contains(c.seconds, t.Second()) &&
		contains(c.minutes, t.Minute()) &&
		contains(c.hours, t.Hour()) &&
		contains(c.days, t.Day()) &&
		contains(c.months, int(t.Month())) &&
		contains(c.weekdays, int(t.Weekday()))
}

func contains(values []int, v int) bool {
	for _, candidate := range values {
		if candidate == v {
			return true
		}
	}
	return false
}

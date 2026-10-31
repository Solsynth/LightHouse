package accountevents

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/ports"
)

// AccountStore is the account/profile/social read surface the service needs.
// *store.Store implements it.
type AccountStore interface {
	GetAccountByID(ctx context.Context, id uuid.UUID) (*model.Account, error)
	GetAccountByName(ctx context.Context, name string) (*model.Account, error)
	GetOrCreateAccountProfile(ctx context.Context, accountID uuid.UUID) (*model.Profile, error)
	GetAccountsByIDs(ctx context.Context, ids []uuid.UUID) ([]model.Account, error)
	ListRelatedAccountIDs(ctx context.Context, accountID uuid.UUID, status model.RelationshipStatus, isRelated bool) ([]string, error)
	ListStatusesBetween(ctx context.Context, accountID uuid.UUID, from, to time.Time) ([]model.AccountStatus, error)
	ListStatusesForTimeline(ctx context.Context, accountID uuid.UUID) ([]model.AccountStatus, error)
	ListActivitiesForTimeline(ctx context.Context, accountID uuid.UUID) ([]model.PresenceActivity, error)
}

// ExperienceRecorder grants account experience. accountrecords.Store's
// AddExperienceWithMultiplier satisfies it through the module adapter.
type ExperienceRecorder interface {
	AddExperience(ctx context.Context, accountID uuid.UUID, reasonType, reason string, delta int64, multiplier float64) error
}

// CaptchaValidator verifies a captcha token (auth.AuthService).
type CaptchaValidator interface {
	ValidateCaptcha(ctx context.Context, token string) (bool, error)
}

// Deps is the service dependency set. Perks, Creditor and Files are optional
// ports: nil means the feature degrades (no captcha bypass, no payout, no file
// resolution) instead of panicking.
type Deps struct {
	Store    *Store
	Accounts AccountStore
	Rewards  ExperienceRecorder
	Perks    ports.Perks
	Creditor ports.Creditor
	Files    ports.Files
	Captcha  CaptchaValidator
	Notable  *NotableService
	Log      *slog.Logger
	Now      func() time.Time
}

// Service is the account-events domain service.
type Service struct {
	deps Deps

	locks   sync.Map // account id -> *sync.Mutex
	captcha sync.Map // account id -> captchaDecision
}

// NewService constructs the service.
func NewService(deps Deps) *Service {
	if deps.Log == nil {
		deps.Log = slog.Default()
	}
	if deps.Now == nil {
		deps.Now = func() time.Time { return time.Now().UTC() }
	}
	return &Service{deps: deps}
}

func (s *Service) now() time.Time { return s.deps.Now().UTC() }

func (s *Service) log() *slog.Logger { return s.deps.Log }

// accountLock serializes check-ins per account. Passing the same keyed mutex
// through the insert keeps concurrent requests from double-writing a day.
func (s *Service) accountLock(accountID uuid.UUID) *sync.Mutex {
	value, _ := s.locks.LoadOrStore(accountID, &sync.Mutex{})
	return value.(*sync.Mutex)
}

// ValidateCaptcha verifies a captcha token through the auth service.
func (s *Service) ValidateCaptcha(ctx context.Context, token string) (bool, error) {
	if s.deps.Captcha == nil {
		return false, nil
	}
	return s.deps.Captcha.ValidateCaptcha(ctx, token)
}

// HasPerkSubscription reports whether the account has an active perk plan.
func (s *Service) HasPerkSubscription(ctx context.Context, accountID string) bool {
	return s.hasPerkSubscription(ctx, accountID)
}

// hasPerkSubscription mirrors the C# RemoteSubscriptionService check.
func (s *Service) hasPerkSubscription(ctx context.Context, accountID string) bool {
	if s.deps.Perks == nil || !s.deps.Perks.Available() {
		return false
	}
	subscription, err := s.deps.Perks.Subscription(ctx, accountID)
	if err != nil {
		s.log().Warn("resolve perk subscription", "account", accountID, "error", err)
		return false
	}
	return subscription != nil
}

// normalizeCalendarTags mirrors NormalizeCalendarTags: trim, lower-case,
// de-duplicate, sort.
func normalizeCalendarTags(tags []string) []string {
	seen := make(map[string]struct{}, len(tags))
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		trimmed := strings.ToLower(strings.TrimSpace(tag))
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	sort.Strings(out)
	return out
}

// escapeLike neutralizes LIKE wildcards in user input.
func escapeLike(value string) string {
	out := make([]byte, 0, len(value))
	for i := 0; i < len(value); i++ {
		switch value[i] {
		case '\\', '%', '_':
			out = append(out, '\\')
		}
		out = append(out, value[i])
	}
	return string(out)
}

func modelTime(value time.Time) model.Time { return model.Time(value.UTC()) }

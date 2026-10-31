package accountrecords

import (
	"context"
	"log/slog"
	"src.solsynth.dev/sosys/stargate/store"
	"time"

	"github.com/google/uuid"
)

// DefaultSocialCreditInterval matches Passport's hourly
// SocialCreditValidationJob.
const DefaultSocialCreditInterval = time.Hour

// RecalculateSocialCredits recomputes the denormalized
// account_profiles.social_credits column for every account that has credit
// records. The C# job existed because GetSocialCredit computed live while
// clients read the profile column; keeping the column fresh preserves the
// same contract without a read-path cache.
func (s *Store) RecalculateSocialCredits(ctx context.Context) (int, error) {
	if s == nil || s.DB == nil || s.Profiles == nil {
		return 0, nil
	}
	var accountIDs []uuid.UUID
	if err := s.DB.WithContext(ctx).
		Model(&SocialCreditRecordEntity{}).
		Distinct("account_id").
		Pluck("account_id", &accountIDs).Error; err != nil {
		return 0, err
	}
	updated := 0
	for _, accountID := range accountIDs {
		total, err := s.EffectiveSocialCredit(ctx, accountID)
		if err != nil {
			return updated, err
		}
		value := total
		if err := s.Profiles.ApplyProfileFieldPatch(ctx, accountID, &store.ProfileFieldPatch{SocialCredits: &value}); err != nil {
			return updated, err
		}
		updated++
	}
	return updated, nil
}

// StartSocialCreditJob runs RecalculateSocialCredits immediately and then on
// interval until the returned stop function is called. Failures are logged;
// the job never crashes the process.
func StartSocialCreditJob(ctx context.Context, store *Store, interval time.Duration, log *slog.Logger) func() {
	if interval <= 0 {
		interval = DefaultSocialCreditInterval
	}
	jobCtx, cancel := context.WithCancel(ctx)
	go func() {
		run := func() {
			count, err := store.RecalculateSocialCredits(jobCtx)
			if err != nil {
				if log != nil {
					log.Warn("social credit validation failed", "error", err)
				}
				return
			}
			if log != nil && count > 0 {
				log.Debug("social credit profiles refreshed", "accounts", count)
			}
		}
		run()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-jobCtx.Done():
				return
			case <-ticker.C:
				run()
			}
		}
	}()
	return cancel
}

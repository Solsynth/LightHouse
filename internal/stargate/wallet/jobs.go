package wallet

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Job cadences mirror the Quartz triggers in ScheduledJobsConfiguration.cs.
// The two cron jobs (recycling and merchant settlement, both "0 0 0 * * ?")
// become daily tickers.
const (
	jobIntervalOrderExpiration       = 15 * time.Minute
	jobIntervalTransactionExpiration = 15 * time.Minute
	jobIntervalFundExpiration        = time.Hour
	jobIntervalRaisingDeadline       = 15 * time.Minute
	jobIntervalSubscriptionRenewal   = 30 * time.Minute
	jobIntervalGiftCleanup           = time.Hour
	jobIntervalMerchantSettlement    = 24 * time.Hour
	jobIntervalRecycling             = 24 * time.Hour

	// jobBatchSize is the batch the C# subscription job used (100).
	jobBatchSize = 100
	// recyclingRetention is AppDatabaseRecyclingJob's seven-day window.
	recyclingRetention = 7 * 24 * time.Hour
)

// startJobs launches every wallet background job and returns a stop function
// that cancels the jobs and waits for their goroutines to exit. Jobs run once
// on start (the Quartz simple triggers fired immediately) except the daily
// tickers, which wait a full interval.
func (d *Deps) startJobs(ctx context.Context) func() {
	if d == nil || d.Service == nil {
		return func() {}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	log := d.Log
	if log == nil {
		log = d.Service.Log
	}
	if log == nil {
		log = slog.Default()
	}
	subs := NewSubscriptions(d.Service)
	subs.Configure(d.Settings, log)
	jobs := &walletJobs{d: d, svc: d.Service, subs: subs, log: log}

	runCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup

	start := func(name string, interval time.Duration, immediate bool, run func(context.Context) error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if immediate {
				jobs.execute(runCtx, name, run)
			}
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-runCtx.Done():
					return
				case <-ticker.C:
					jobs.execute(runCtx, name, run)
				}
			}
		}()
	}

	start("order-expiration", jobIntervalOrderExpiration, true, jobs.expireOrders)
	start("transaction-expiration", jobIntervalTransactionExpiration, true, jobs.expireTransactions)
	start("fund-expiration", jobIntervalFundExpiration, true, jobs.expireFunds)
	start("raising-fund-deadline", jobIntervalRaisingDeadline, true, jobs.expireRaisingFunds)
	// SubscriptionRenewalJob performed the expiration sweep, the pending
	// activation sweep and the renewal pass in one run; keep that order.
	start("subscription-renewal", jobIntervalSubscriptionRenewal, true, jobs.renewSubscriptions)
	start("gift-cleanup", jobIntervalGiftCleanup, true, jobs.cleanupGifts)
	start("merchant-settlement", jobIntervalMerchantSettlement, false, jobs.settleMerchants)
	start("soft-delete-recycling", jobIntervalRecycling, false, jobs.recycleSoftDeleted)

	// LegacyInAppSubscriptionAvailabilityValidationService was an
	// IHostedService: one run after a configurable startup delay.
	wg.Add(1)
	go func() {
		defer wg.Done()
		timer := time.NewTimer(subs.LegacyInAppAvailabilityValidationDelay())
		defer timer.Stop()
		select {
		case <-runCtx.Done():
		case <-timer.C:
			jobs.execute(runCtx, "legacy-in-app-validation", jobs.validateLegacyInAppSubscriptions)
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			wg.Wait()
		})
	}
}

// walletJobs carries the shared services one job run needs.
type walletJobs struct {
	d    *Deps
	svc  *Service
	subs *Subscriptions
	log  *slog.Logger
}

// execute runs one job, logging failures (and staying quiet on shutdown).
func (j *walletJobs) execute(ctx context.Context, name string, run func(context.Context) error) {
	if ctx.Err() != nil {
		return
	}
	j.log.Debug("wallet job started", "job", name)
	if err := run(ctx); err != nil && ctx.Err() == nil {
		j.log.Error("wallet job failed", "job", name, "error", err)
	}
}

// expireOrders ports the lazy PaymentService.ExpireOverdueOrdersAsync the C#
// order endpoints invoked; running it on a timer also expires orders nobody
// reads again.
func (j *walletJobs) expireOrders(ctx context.Context) error {
	return j.svc.ExpireOrders(ctx)
}

func (j *walletJobs) expireTransactions(ctx context.Context) error {
	return j.svc.ProcessExpiredTransactions(ctx)
}

func (j *walletJobs) expireFunds(ctx context.Context) error {
	return j.svc.ProcessExpiredFunds(ctx)
}

func (j *walletJobs) expireRaisingFunds(ctx context.Context) error {
	return j.svc.ProcessExpiredRaisingFunds(ctx)
}

// renewSubscriptions ports SubscriptionRenewalJob: expire, activate pending,
// then auto-renew the in-app wallet subscriptions.
func (j *walletJobs) renewSubscriptions(ctx context.Context) error {
	expired, err := j.subs.UpdateExpiredSubscriptions(ctx, jobBatchSize)
	if err != nil {
		return err
	}
	activated, err := j.subs.ActivatePendingSubscriptions(ctx, jobBatchSize)
	if err != nil {
		return err
	}
	renewed, failed, err := j.subs.RenewSubscriptions(ctx, jobBatchSize)
	if err != nil {
		return err
	}
	if expired > 0 || activated > 0 || renewed > 0 || failed > 0 {
		j.log.Info("wallet subscription job finished",
			"expired", expired, "activated", activated, "renewed", renewed, "failed", failed)
	}
	return nil
}

func (j *walletJobs) cleanupGifts(ctx context.Context) error {
	count, err := j.subs.CleanupGifts(ctx)
	if err != nil {
		return err
	}
	if count > 0 {
		j.log.Info("wallet gift cleanup finished", "removed", count)
	}
	return nil
}

// settleMerchants ports AppSettlementJob: settle every wallet with pending
// settlements, one transaction per currency group.
func (j *walletJobs) settleMerchants(ctx context.Context) error {
	grouped, err := j.svc.PendingSettlementsGrouped(ctx)
	if err != nil {
		return err
	}
	settled, failures := 0, 0
	for walletID := range grouped {
		if ctx.Err() != nil {
			break
		}
		if _, err := j.svc.SettleWallet(ctx, walletID, SettlementAutomatic); err != nil {
			failures++
			j.log.Error("wallet merchant settlement failed", "wallet", walletID, "error", err)
			continue
		}
		settled++
	}
	if settled > 0 || failures > 0 {
		j.log.Info("wallet merchant settlement finished", "settled", settled, "failed", failures)
	}
	return nil
}

// recycleSoftDeleted ports AppDatabaseRecyclingJob: hard-delete rows that were
// soft-deleted more than seven days ago.
func (j *walletJobs) recycleSoftDeleted(ctx context.Context) error {
	cutoff := time.Now().UTC().Add(-recyclingRetention)
	var total int64
	for _, entity := range entities() {
		if ctx.Err() != nil {
			break
		}
		result := j.svc.DB.WithContext(ctx).Unscoped().
			Where("deleted_at IS NOT NULL AND deleted_at < ?", cutoff).
			Delete(entity)
		if result.Error != nil {
			return result.Error
		}
		total += result.RowsAffected
	}
	if total > 0 {
		j.log.Info("wallet soft-deleted records recycled", "removed", total)
	}
	return nil
}

// validateLegacyInAppSubscriptions ports
// LegacyInAppSubscriptionAvailabilityValidationService.
func (j *walletJobs) validateLegacyInAppSubscriptions(ctx context.Context) error {
	cancelled, err := j.subs.CancelUnavailableInAppWalletSubscriptions(ctx)
	if err != nil {
		return err
	}
	j.log.Info("legacy in-app subscription validation finished", "cancelled", cancelled)
	return nil
}

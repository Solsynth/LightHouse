package wallet

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// AppPublisherResolver maps a Develop custom app to the publisher that owns it.
// The module wires it to the Develop gRPC client when one is configured.
type AppPublisherResolver interface {
	PublisherIDForApp(ctx context.Context, appID uuid.UUID) (uuid.UUID, error)
}

// SetAppPublisherResolver installs the Develop lookup used for app orders.
func (s *Service) SetAppPublisherResolver(resolver AppPublisherResolver) { s.AppResolver = resolver }

// resolveMerchantForApp returns (and creates when missing) the merchant for an
// app's publisher. A nil result means the app could not be resolved.
func (s *Service) resolveMerchantForApp(ctx context.Context, appID uuid.UUID, payeeWalletID uuid.UUID) *MerchantEntity {
	if s.AppResolver == nil {
		return nil
	}
	publisherID, err := s.AppResolver.PublisherIDForApp(ctx, appID)
	if err != nil || publisherID == uuid.Nil {
		if err != nil {
			s.Log.Warn("resolving app publisher failed", "app", appID, "error", err)
		}
		return nil
	}
	merchant, err := s.UpsertMerchant(ctx, publisherID, &payeeWalletID, nil)
	if err != nil {
		s.Log.Warn("upserting merchant failed", "publisher", publisherID, "error", err)
		return nil
	}
	return merchant
}

// UpsertMerchant creates or updates the merchant for a publisher.
func (s *Service) UpsertMerchant(ctx context.Context, publisherID uuid.UUID, paymentWalletID *uuid.UUID, name *string) (*MerchantEntity, error) {
	var merchant MerchantEntity
	err := s.DB.WithContext(ctx).Where("publisher_id = ?", publisherID).First(&merchant).Error
	switch {
	case err == nil:
		updates := map[string]any{"payment_wallet_id": paymentWalletID}
		if name != nil {
			updates["name"] = *name
		}
		if err := s.DB.WithContext(ctx).Model(&MerchantEntity{}).Where("id = ?", merchant.ID).
			Updates(updates).Error; err != nil {
			return nil, err
		}
		merchant.PaymentWalletID = paymentWalletID
		if name != nil {
			merchant.Name = name
		}
		return &merchant, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		merchant = MerchantEntity{ID: newID(), PublisherID: publisherID, PaymentWalletID: paymentWalletID, Name: name}
		if err := s.DB.WithContext(ctx).Create(&merchant).Error; err != nil {
			return nil, err
		}
		return &merchant, nil
	default:
		return nil, err
	}
}

// GetMerchantByPublisher loads a publisher's merchant.
func (s *Service) GetMerchantByPublisher(ctx context.Context, publisherID uuid.UUID) (*MerchantEntity, error) {
	var merchant MerchantEntity
	if err := s.DB.WithContext(ctx).Where("publisher_id = ?", publisherID).First(&merchant).Error; err != nil {
		return nil, err
	}
	return &merchant, nil
}

// GetMerchant loads a merchant by id.
func (s *Service) GetMerchant(ctx context.Context, merchantID uuid.UUID) (*MerchantEntity, error) {
	var merchant MerchantEntity
	if err := s.DB.WithContext(ctx).First(&merchant, "id = ?", merchantID).Error; err != nil {
		return nil, err
	}
	return &merchant, nil
}

// CreateSettlement records a held payment awaiting settlement.
func (s *Service) CreateSettlement(ctx context.Context, merchantID uuid.UUID, paymentTransactionID *uuid.UUID, paymentWalletID uuid.UUID, currency string, amount decimal.Decimal, orderID *uuid.UUID) (*MerchantSettlementEntity, error) {
	settlement := MerchantSettlementEntity{
		ID:                   newID(),
		MerchantID:           merchantID,
		OrderID:              orderID,
		PaymentTransactionID: paymentTransactionID,
		PaymentWalletID:      paymentWalletID,
		Currency:             currency,
		Amount:               truncate3(amount),
		Status:               SettlementPending,
	}
	if err := s.DB.WithContext(ctx).Create(&settlement).Error; err != nil {
		return nil, err
	}
	return &settlement, nil
}

// PendingSettlements lists pending settlements for a wallet.
func (s *Service) PendingSettlements(ctx context.Context, paymentWalletID uuid.UUID) ([]MerchantSettlementEntity, error) {
	var settlements []MerchantSettlementEntity
	err := s.DB.WithContext(ctx).
		Where("status = ? AND payment_wallet_id = ?", SettlementPending, paymentWalletID).
		Order("created_at").Find(&settlements).Error
	return settlements, err
}

// PendingSettlementsGrouped lists every pending settlement grouped by wallet.
func (s *Service) PendingSettlementsGrouped(ctx context.Context) (map[uuid.UUID][]MerchantSettlementEntity, error) {
	var settlements []MerchantSettlementEntity
	if err := s.DB.WithContext(ctx).Where("status = ?", SettlementPending).
		Order("created_at").Find(&settlements).Error; err != nil {
		return nil, err
	}
	out := map[uuid.UUID][]MerchantSettlementEntity{}
	for _, settlement := range settlements {
		out[settlement.PaymentWalletID] = append(out[settlement.PaymentWalletID], settlement)
	}
	return out, nil
}

// PendingTotals returns per-currency pending counts and totals for a wallet.
func (s *Service) PendingTotals(ctx context.Context, paymentWalletID uuid.UUID) (map[string]CurrencyStat, error) {
	var rows []struct {
		Currency string
		Count    int
		Total    decimal.Decimal
	}
	err := s.DB.WithContext(ctx).Model(&MerchantSettlementEntity{}).
		Select("currency, count(*) AS count, coalesce(sum(amount), 0) AS total").
		Where("status = ? AND payment_wallet_id = ?", SettlementPending, paymentWalletID).
		Group("currency").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[string]CurrencyStat, len(rows))
	for _, row := range rows {
		out[row.Currency] = CurrencyStat{Count: row.Count, Total: row.Total}
	}
	return out, nil
}

// SettleWallet settles every pending settlement of a wallet, one transaction
// per currency. Returns the created settlement transactions.
func (s *Service) SettleWallet(ctx context.Context, paymentWalletID uuid.UUID, trigger int) ([]WalletTransactionEntity, error) {
	pending, err := s.PendingSettlements(ctx, paymentWalletID)
	if err != nil {
		return nil, err
	}
	if len(pending) == 0 {
		return nil, nil
	}
	byCurrency := map[string][]MerchantSettlementEntity{}
	for _, settlement := range pending {
		byCurrency[settlement.Currency] = append(byCurrency[settlement.Currency], settlement)
	}
	now := time.Now().UTC()
	results := make([]WalletTransactionEntity, 0, len(byCurrency))
	for currency, group := range byCurrency {
		total := decimal.Zero
		for _, settlement := range group {
			total = total.Add(settlement.Amount)
		}
		remarks := fmt.Sprintf("Settlement: %d transactions", len(group))
		transaction, err := s.CreateTransaction(ctx, TransactionInput{
			PayeeWalletID: &paymentWalletID,
			Currency:      currency,
			Amount:        total,
			Remarks:       &remarks,
			Type:          TransactionSystem,
			Silent:        true,
		})
		if err != nil {
			return nil, err
		}
		ids := make([]uuid.UUID, 0, len(group))
		for _, settlement := range group {
			ids = append(ids, settlement.ID)
		}
		if err := s.DB.WithContext(ctx).Model(&MerchantSettlementEntity{}).Where("id IN ?", ids).
			Updates(map[string]any{
				"status":                    SettlementSettled,
				"settled_by":                trigger,
				"settled_at":                now,
				"settlement_transaction_id": transaction.ID,
			}).Error; err != nil {
			return nil, err
		}
		results = append(results, *transaction)
	}
	return results, nil
}

// CancelSettlement cancels one pending settlement.
func (s *Service) CancelSettlement(ctx context.Context, settlementID uuid.UUID) error {
	return s.DB.WithContext(ctx).Model(&MerchantSettlementEntity{}).
		Where("id = ? AND status = ?", settlementID, SettlementPending).
		Update("status", SettlementCancelled).Error
}

// CancelSettlementsByOrder cancels pending settlements created by an order.
func (s *Service) CancelSettlementsByOrder(ctx context.Context, orderID uuid.UUID) error {
	return s.DB.WithContext(ctx).Model(&MerchantSettlementEntity{}).
		Where("order_id = ? AND status = ?", orderID, SettlementPending).
		Update("status", SettlementCancelled).Error
}

// MerchantSettlements lists a merchant's settlements, newest first.
func (s *Service) MerchantSettlements(ctx context.Context, merchantID uuid.UUID, status *int) ([]MerchantSettlementEntity, error) {
	query := s.DB.WithContext(ctx).Where("merchant_id = ?", merchantID)
	if status != nil {
		query = query.Where("status = ?", *status)
	}
	var settlements []MerchantSettlementEntity
	err := query.Order("created_at DESC").Find(&settlements).Error
	return settlements, err
}

// CurrencyStat is a count plus a total for one currency.
type CurrencyStat struct {
	Count int             `json:"count"`
	Total decimal.Decimal `json:"total"`
}

// StatusStat is a count plus a total for one settlement status.
type StatusStat struct {
	Count int             `json:"count"`
	Total decimal.Decimal `json:"total"`
}

// MerchantOverviewStats mirrors the C# DTO.
type MerchantOverviewStats struct {
	TotalPending        int                     `json:"total_pending"`
	TotalSettled        int                     `json:"total_settled"`
	TotalAllTime        int                     `json:"total_all_time"`
	PendingByCurrency   map[string]CurrencyStat `json:"pending_by_currency"`
	SettledByCurrency   map[string]CurrencyStat `json:"settled_by_currency"`
	ThisMonthByCurrency map[string]CurrencyStat `json:"this_month_by_currency"`
}

// OverviewStats aggregates a merchant's settlement counters.
func (s *Service) OverviewStats(ctx context.Context, merchantID uuid.UUID) (*MerchantOverviewStats, error) {
	var settlements []MerchantSettlementEntity
	if err := s.DB.WithContext(ctx).Where("merchant_id = ?", merchantID).Find(&settlements).Error; err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	out := &MerchantOverviewStats{
		PendingByCurrency:   map[string]CurrencyStat{},
		SettledByCurrency:   map[string]CurrencyStat{},
		ThisMonthByCurrency: map[string]CurrencyStat{},
	}
	add := func(target map[string]CurrencyStat, settlement MerchantSettlementEntity) {
		stat := target[settlement.Currency]
		stat.Count++
		stat.Total = stat.Total.Add(settlement.Amount)
		target[settlement.Currency] = stat
	}
	for _, settlement := range settlements {
		out.TotalAllTime++
		if !settlement.CreatedAt.Before(monthStart) {
			add(out.ThisMonthByCurrency, settlement)
		}
		switch settlement.Status {
		case SettlementPending:
			out.TotalPending++
			add(out.PendingByCurrency, settlement)
		case SettlementSettled:
			out.TotalSettled++
			add(out.SettledByCurrency, settlement)
		}
	}
	return out, nil
}

// PeriodIncomingResult mirrors the C# DTO.
type PeriodIncomingResult struct {
	From        string                  `json:"from"`
	To          string                  `json:"to"`
	Currency    *string                 `json:"currency"`
	TotalCount  int                     `json:"total_count"`
	TotalAmount decimal.Decimal         `json:"total_amount"`
	ByStatus    map[int]StatusStat      `json:"by_status"`
	ByCurrency  map[string]CurrencyStat `json:"by_currency"`
	Settlements []Settlement            `json:"settlements"`
}

// PeriodIncoming aggregates settlements created in a window.
func (s *Service) PeriodIncoming(ctx context.Context, merchantID uuid.UUID, from, to time.Time, currency *string) (*PeriodIncomingResult, error) {
	query := s.DB.WithContext(ctx).Where("merchant_id = ? AND created_at >= ? AND created_at <= ?", merchantID, from, to)
	if currency != nil && *currency != "" {
		query = query.Where("currency = ?", *currency)
	}
	var settlements []MerchantSettlementEntity
	if err := query.Order("created_at DESC").Find(&settlements).Error; err != nil {
		return nil, err
	}
	out := &PeriodIncomingResult{
		From:        from.UTC().Format(time.RFC3339),
		To:          to.UTC().Format(time.RFC3339),
		Currency:    currency,
		TotalCount:  len(settlements),
		TotalAmount: decimal.Zero,
		ByStatus:    map[int]StatusStat{},
		ByCurrency:  map[string]CurrencyStat{},
		Settlements: make([]Settlement, 0, len(settlements)),
	}
	for _, settlement := range settlements {
		out.TotalAmount = out.TotalAmount.Add(settlement.Amount)
		status := out.ByStatus[settlement.Status]
		status.Count++
		status.Total = status.Total.Add(settlement.Amount)
		out.ByStatus[settlement.Status] = status
		stat := out.ByCurrency[settlement.Currency]
		stat.Count++
		stat.Total = stat.Total.Add(settlement.Amount)
		out.ByCurrency[settlement.Currency] = stat
		out.Settlements = append(out.Settlements, settlementModel(&settlement))
	}
	return out, nil
}

// DailyStat mirrors the C# DTO.
type DailyStat struct {
	Date       string                     `json:"date"`
	Count      int                        `json:"count"`
	Total      decimal.Decimal            `json:"total"`
	ByCurrency map[string]decimal.Decimal `json:"by_currency"`
}

// DailyIncoming aggregates settlements per UTC day.
func (s *Service) DailyIncoming(ctx context.Context, merchantID uuid.UUID, from, to time.Time, currency *string) ([]DailyStat, error) {
	query := s.DB.WithContext(ctx).Where("merchant_id = ? AND created_at >= ? AND created_at <= ?", merchantID, from, to)
	if currency != nil && *currency != "" {
		query = query.Where("currency = ?", *currency)
	}
	var settlements []MerchantSettlementEntity
	if err := query.Find(&settlements).Error; err != nil {
		return nil, err
	}
	byDay := map[string]*DailyStat{}
	for _, settlement := range settlements {
		day := settlement.CreatedAt.UTC().Format("2006-01-02")
		stat, ok := byDay[day]
		if !ok {
			stat = &DailyStat{Date: day, ByCurrency: map[string]decimal.Decimal{}}
			byDay[day] = stat
		}
		stat.Count++
		stat.Total = stat.Total.Add(settlement.Amount)
		stat.ByCurrency[settlement.Currency] = stat.ByCurrency[settlement.Currency].Add(settlement.Amount)
	}
	out := make([]DailyStat, 0, len(byDay))
	for _, stat := range byDay {
		out = append(out, *stat)
	}
	return out, nil
}

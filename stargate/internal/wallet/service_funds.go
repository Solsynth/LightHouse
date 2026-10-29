package wallet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func mustJSON(value any) []byte {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return raw
}

// subscriptionAvailable mirrors SnWalletSubscription.IsAvailableAt.
func subscriptionAvailable(e *WalletSubscriptionEntity, now time.Time) bool {
	if !e.IsActive {
		return false
	}
	if e.BegunAt.After(now) {
		return false
	}
	if e.EndedAt != nil && now.After(*e.EndedAt) {
		return false
	}
	if e.RenewalAt != nil && now.After(*e.RenewalAt) {
		return false
	}
	return e.Status == SubscriptionActive
}

// subscriptionPending mirrors SnWalletSubscription.IsPendingActivationAt.
func subscriptionPending(e *WalletSubscriptionEntity, now time.Time) bool {
	if !e.IsActive || e.Status != SubscriptionActive {
		return false
	}
	if !e.BegunAt.After(now) {
		return false
	}
	if e.EndedAt != nil && !e.EndedAt.After(now) {
		return false
	}
	return true
}

// ── funds ──────────────────────────────────────────────────────────────────

// FundInput describes a distributing fund.
type FundInput struct {
	CreatorAccountID    uuid.UUID
	RecipientAccountIDs []uuid.UUID
	Currency            string
	TotalAmount         decimal.Decimal
	AmountOfSplits      int
	SplitType           int
	Message             *string
	Expiration          *time.Duration
}

// CreateFund escrows the creator's funds for later distribution.
func (s *Service) CreateFund(ctx context.Context, in FundInput) (*WalletFundEntity, error) {
	if in.TotalAmount.LessThanOrEqual(decimal.Zero) {
		return nil, errors.New("total amount must be positive")
	}
	creatorWallet, _, err := s.GetAccountWallet(ctx, in.CreatorAccountID)
	if err != nil {
		return nil, errors.New("creator wallet not found")
	}
	for _, accountID := range in.RecipientAccountIDs {
		if _, _, err := s.GetAccountWallet(ctx, accountID); err != nil {
			return nil, fmt.Errorf("wallet not found for recipient account %s", accountID)
		}
	}
	total := truncate3(in.TotalAmount)
	expiry := 24 * time.Hour
	if in.Expiration != nil {
		expiry = *in.Expiration
	}
	fund := WalletFundEntity{
		ID:               newID(),
		CreatorAccountID: in.CreatorAccountID,
		Currency:         in.Currency,
		TotalAmount:      total,
		RemainingAmount:  total,
		AmountOfSplits:   in.AmountOfSplits,
		SplitType:        in.SplitType,
		Message:          in.Message,
		ExpiredAt:        time.Now().UTC().Add(expiry),
		IsOpen:           len(in.RecipientAccountIDs) == 0,
	}
	err = s.DB.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		pocket, created, err := s.GetOrCreatePocket(ctx, db, creatorWallet.ID, in.Currency, nil)
		if err != nil {
			return err
		}
		if created || pocket.Amount.LessThan(total) {
			return ErrInsufficientFunds
		}
		if err := addToPocket(db, pocket.ID, total.Neg(), decimal.Zero); err != nil {
			return err
		}
		if err := db.Create(&fund).Error; err != nil {
			return err
		}
		for _, accountID := range in.RecipientAccountIDs {
			recipient := WalletFundRecipientEntity{
				ID:                 newID(),
				FundID:             fund.ID,
				RecipientAccountID: accountID,
				Amount:             decimal.Zero,
			}
			if err := db.Create(&recipient).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.notifyPocket(ctx, creatorWallet.ID, in.Currency)
	return &fund, nil
}

// RaisingFundInput describes a contribution-collecting fund.
type RaisingFundInput struct {
	CreatorAccountID   uuid.UUID
	Currency           string
	TargetAmount       decimal.Decimal
	MaxParticipants    int
	ContributionType   int
	ContributionAmount decimal.Decimal
	IsOpen             bool
	InvitedAccountIDs  []uuid.UUID
	Message            *string
	Expiration         *time.Duration
	DeadlineAt         *time.Time
}

// CreateRaisingFund creates a fund that collects contributions.
func (s *Service) CreateRaisingFund(ctx context.Context, in RaisingFundInput) (*WalletFundEntity, error) {
	if in.TargetAmount.LessThanOrEqual(decimal.Zero) {
		return nil, errors.New("target amount must be positive")
	}
	if in.ContributionType == ContributionFixed && in.ContributionAmount.LessThanOrEqual(decimal.Zero) {
		return nil, errors.New("fixed contribution amount must be positive")
	}
	expiry := 24 * time.Hour
	if in.Expiration != nil {
		expiry = *in.Expiration
	}
	fund := WalletFundEntity{
		ID:               newID(),
		CreatorAccountID: in.CreatorAccountID,
		Currency:         in.Currency,
		TotalAmount:      truncate3(in.TargetAmount),
		RemainingAmount:  truncate3(in.TargetAmount),
		AmountOfSplits:   in.MaxParticipants,
		SplitType:        FundSplitEven,
		Message:          in.Message,
		ExpiredAt:        time.Now().UTC().Add(expiry),
		IsOpen:           in.IsOpen,
		IsRaising:        true,
		TargetAmount:     truncate3(in.TargetAmount),
		ContributionType: in.ContributionType,
		DeadlineAt:       in.DeadlineAt,
	}
	if in.ContributionType == ContributionFixed {
		fund.ContributionAmount = truncate3(in.ContributionAmount)
	}
	err := s.DB.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		if err := db.Create(&fund).Error; err != nil {
			return err
		}
		for _, accountID := range in.InvitedAccountIDs {
			recipient := WalletFundRecipientEntity{
				ID:                 newID(),
				FundID:             fund.ID,
				RecipientAccountID: accountID,
				Amount:             decimal.Zero,
			}
			if err := db.Create(&recipient).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &fund, nil
}

// GetFund loads a fund with its recipients.
func (s *Service) GetFund(ctx context.Context, fundID uuid.UUID) (*WalletFundEntity, []WalletFundRecipientEntity, error) {
	var fund WalletFundEntity
	if err := s.DB.WithContext(ctx).First(&fund, "id = ?", fundID).Error; err != nil {
		return nil, nil, err
	}
	var recipients []WalletFundRecipientEntity
	if err := s.DB.WithContext(ctx).Where("fund_id = ?", fundID).Order("created_at").
		Find(&recipients).Error; err != nil {
		return nil, nil, err
	}
	return &fund, recipients, nil
}

// splitEvenly mirrors PaymentService.SplitEvenly (two-decimal increments).
func splitEvenly(total decimal.Decimal, count int) []decimal.Decimal {
	if count <= 0 {
		return nil
	}
	base := total.Div(decimal.NewFromInt(int64(count))).Truncate(2)
	remainder := total.Sub(base.Mul(decimal.NewFromInt(int64(count))))
	amounts := make([]decimal.Decimal, count)
	for i := range count {
		amount := base
		if decimal.NewFromInt(int64(i)).LessThan(remainder.Mul(decimal.NewFromInt(100))) {
			amount = amount.Add(decimal.NewFromFloat(0.01))
		}
		amounts[i] = amount
	}
	return amounts
}

// splitRandomly mirrors PaymentService.SplitRandomly.
func splitRandomly(total decimal.Decimal, count int) []decimal.Decimal {
	if count <= 0 {
		return nil
	}
	amounts := make([]decimal.Decimal, 0, count)
	remaining := total
	for i := range count - 1 {
		evenSplit := remaining.Div(decimal.NewFromInt(int64(count - i)))
		minAmount := evenSplit.Mul(decimal.NewFromFloat(0.5))
		maxAmount := remaining.Sub(minAmount.Mul(decimal.NewFromInt(int64(count - i - 1))))
		span := maxAmount.Sub(minAmount)
		amount := minAmount.Add(span.Mul(decimal.NewFromFloat(rand.Float64()))).Round(2)
		amounts = append(amounts, amount)
		remaining = remaining.Sub(amount)
	}
	amounts = append(amounts, remaining.Round(2))
	return amounts
}

// dynamicFundAmount mirrors PaymentService.CalculateDynamicAmount.
func dynamicFundAmount(fund *WalletFundEntity, recipients []WalletFundRecipientEntity) decimal.Decimal {
	if fund.RemainingAmount.LessThanOrEqual(decimal.Zero) {
		return decimal.Zero
	}
	if fund.IsOpen {
		received := 0
		for _, recipient := range recipients {
			if recipient.IsReceived {
				received++
			}
		}
		remainingSlots := fund.AmountOfSplits - received
		if remainingSlots <= 0 {
			return decimal.Zero
		}
		var amount decimal.Decimal
		if fund.SplitType == FundSplitRandom {
			amount = splitRandomly(fund.RemainingAmount, remainingSlots)[0]
		} else {
			amount = splitEvenly(fund.RemainingAmount, remainingSlots)[0]
		}
		if amount.LessThan(decimal.NewFromFloat(0.01)) {
			return decimal.NewFromFloat(0.01)
		}
		return amount
	}
	unclaimed := 0
	for _, recipient := range recipients {
		if !recipient.IsReceived {
			unclaimed++
		}
	}
	if unclaimed == 0 {
		return decimal.Zero
	}
	if fund.SplitType == FundSplitRandom {
		return splitRandomly(fund.RemainingAmount, unclaimed)[0]
	}
	return splitEvenly(fund.RemainingAmount, unclaimed)[0]
}

// ReceiveFund claims one recipient's share of a distributing fund.
func (s *Service) ReceiveFund(ctx context.Context, recipientAccountID, fundID uuid.UUID) (*WalletTransactionEntity, error) {
	var transaction *WalletTransactionEntity
	err := s.DB.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		var fund WalletFundEntity
		if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).First(&fund, "id = ?", fundID).Error; err != nil {
			return errors.New("fund not found")
		}
		if fund.Status == FundExpired || fund.Status == FundRefunded {
			return errors.New("fund is no longer available")
		}
		var recipients []WalletFundRecipientEntity
		if err := db.Where("fund_id = ?", fund.ID).Find(&recipients).Error; err != nil {
			return err
		}
		var recipient *WalletFundRecipientEntity
		for i := range recipients {
			if recipients[i].RecipientAccountID == recipientAccountID {
				recipient = &recipients[i]
				break
			}
		}
		switch {
		case recipient == nil && !fund.IsOpen:
			return errors.New("you are not a recipient of this fund")
		case recipient == nil && fund.IsOpen:
			amount := truncate3(dynamicFundAmount(&fund, recipients))
			if amount.LessThanOrEqual(decimal.Zero) {
				return errors.New("no funds remaining to claim")
			}
			row := WalletFundRecipientEntity{
				ID:                 newID(),
				FundID:             fund.ID,
				RecipientAccountID: recipientAccountID,
				Amount:             amount,
			}
			if err := db.Create(&row).Error; err != nil {
				return err
			}
			fund.RemainingAmount = fund.RemainingAmount.Sub(amount)
			recipient = &row
		}
		if !fund.IsOpen && recipient.Amount.IsZero() {
			amount := truncate3(dynamicFundAmount(&fund, recipients))
			if amount.LessThanOrEqual(decimal.Zero) {
				return errors.New("no funds remaining to claim")
			}
			recipient.Amount = amount
			fund.RemainingAmount = fund.RemainingAmount.Sub(amount)
		}
		if recipient.IsReceived {
			return errors.New("you have already received this fund")
		}
		recipientWallet, _, err := s.GetAccountWallet(ctx, recipientAccountID)
		if err != nil {
			return errors.New("recipient wallet not found")
		}
		remarks := fmt.Sprintf("Received fund portion from %s", fund.CreatorAccountID)
		transaction, err = s.CreateTransaction(ctx, TransactionInput{
			PayeeWalletID: &recipientWallet.ID,
			Currency:      fund.Currency,
			Amount:        recipient.Amount,
			Remarks:       &remarks,
			Type:          TransactionSystem,
			Silent:        true,
		})
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		recipient.IsReceived = true
		recipient.ReceivedAt = &now
		if err := db.Model(&WalletFundRecipientEntity{}).Where("id = ?", recipient.ID).
			Updates(map[string]any{"is_received": true, "received_at": now, "amount": recipient.Amount}).Error; err != nil {
			return err
		}
		if fund.IsOpen {
			if fund.RemainingAmount.LessThanOrEqual(decimal.Zero) {
				fund.Status = FundFullyReceived
			} else {
				fund.Status = FundPartiallyReceived
			}
		} else {
			all := true
			for _, row := range recipients {
				if row.ID == recipient.ID || row.IsReceived {
					continue
				}
				all = false
				break
			}
			if all {
				fund.Status = FundFullyReceived
			} else {
				fund.Status = FundPartiallyReceived
			}
		}
		return db.Model(&WalletFundEntity{}).Where("id = ?", fund.ID).
			Updates(map[string]any{"status": fund.Status, "remaining_amount": fund.RemainingAmount}).Error
	})
	if err != nil {
		return nil, err
	}
	return transaction, nil
}

// ContributeFund adds a contribution to a raising fund. The C# implementation
// debits the contributor and then attempted a wallet-less ledger entry that
// always failed; the port records the contribution without the second debit.
func (s *Service) ContributeFund(ctx context.Context, contributorAccountID, fundID uuid.UUID, amount decimal.Decimal) (*WalletFundEntity, error) {
	var fund WalletFundEntity
	err := s.DB.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).First(&fund, "id = ?", fundID).Error; err != nil {
			return errors.New("fund not found")
		}
		if !fund.IsRaising {
			return errors.New("this fund is not in raising mode")
		}
		if fund.Status == FundExpired || fund.Status == FundFullyReceived {
			return errors.New("fund is no longer accepting contributions")
		}
		now := time.Now().UTC()
		if fund.DeadlineAt != nil && fund.DeadlineAt.Before(now) {
			return errors.New("fund deadline has passed")
		}
		var recipients []WalletFundRecipientEntity
		if err := db.Where("fund_id = ?", fund.ID).Find(&recipients).Error; err != nil {
			return err
		}
		for _, row := range recipients {
			if row.RecipientAccountID == contributorAccountID && row.IsReceived {
				return errors.New("you have already contributed to this fund")
			}
		}
		contribution := truncate3(amount)
		if fund.ContributionType == ContributionFixed {
			contribution = fund.ContributionAmount
		} else if contribution.LessThanOrEqual(decimal.Zero) {
			return errors.New("contribution amount must be positive")
		}
		raised := decimal.Zero
		for _, row := range recipients {
			if row.IsReceived {
				raised = raised.Add(row.Amount)
			}
		}
		if fund.TargetAmount.GreaterThan(decimal.Zero) && raised.Add(contribution).GreaterThan(fund.TargetAmount) {
			return errors.New("contribution would exceed target amount")
		}
		if !fund.IsOpen && fund.AmountOfSplits > 0 {
			if len(recipients) >= fund.AmountOfSplits {
				return errors.New("fund has reached maximum participants")
			}
			invited := false
			for _, row := range recipients {
				if row.RecipientAccountID == contributorAccountID {
					invited = true
					break
				}
			}
			if !invited {
				return errors.New("you are not invited to contribute to this fund")
			}
		}
		wallet, _, err := s.GetAccountWallet(ctx, contributorAccountID)
		if err != nil {
			return errors.New("contributor wallet not found")
		}
		pocket, created, err := s.GetOrCreatePocket(ctx, db, wallet.ID, fund.Currency, nil)
		if err != nil {
			return err
		}
		if created || pocket.Amount.LessThan(contribution) {
			return ErrInsufficientFunds
		}
		if err := addToPocket(db, pocket.ID, contribution.Neg(), decimal.Zero); err != nil {
			return err
		}
		updated := false
		for i := range recipients {
			if recipients[i].RecipientAccountID == contributorAccountID {
				recipients[i].Amount = contribution
				recipients[i].IsReceived = true
				recipients[i].ReceivedAt = &now
				if err := db.Model(&WalletFundRecipientEntity{}).Where("id = ?", recipients[i].ID).
					Updates(map[string]any{"amount": contribution, "is_received": true, "received_at": now}).Error; err != nil {
					return err
				}
				updated = true
				break
			}
		}
		if !updated {
			row := WalletFundRecipientEntity{
				ID:                 newID(),
				FundID:             fund.ID,
				RecipientAccountID: contributorAccountID,
				Amount:             contribution,
				IsReceived:         true,
				ReceivedAt:         &now,
			}
			if err := db.Create(&row).Error; err != nil {
				return err
			}
		}
		fund.RemainingAmount = fund.RemainingAmount.Sub(contribution)
		if fund.TargetAmount.GreaterThan(decimal.Zero) && raised.Add(contribution).GreaterThanOrEqual(fund.TargetAmount) {
			fund.Status = FundFullyReceived
		} else {
			fund.Status = FundPartiallyReceived
		}
		if err := db.Model(&WalletFundEntity{}).Where("id = ?", fund.ID).
			Updates(map[string]any{"status": fund.Status, "remaining_amount": fund.RemainingAmount}).Error; err != nil {
			return err
		}
		s.notifyPocket(ctx, wallet.ID, fund.Currency)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &fund, nil
}

// ProcessExpiredFunds refunds unclaimed portions of distributing funds.
func (s *Service) ProcessExpiredFunds(ctx context.Context) error {
	var funds []WalletFundEntity
	if err := s.DB.WithContext(ctx).
		Where("status IN ? AND is_raising = false AND expired_at < ?", []int{FundCreated, FundPartiallyReceived}, time.Now().UTC()).
		Find(&funds).Error; err != nil {
		return err
	}
	for i := range funds {
		fund := funds[i]
		var recipients []WalletFundRecipientEntity
		if err := s.DB.WithContext(ctx).Where("fund_id = ?", fund.ID).Find(&recipients).Error; err != nil {
			return err
		}
		unclaimed := decimal.Zero
		for _, recipient := range recipients {
			if !recipient.IsReceived {
				unclaimed = unclaimed.Add(recipient.Amount)
			}
		}
		if unclaimed.GreaterThan(decimal.Zero) {
			if wallet, _, err := s.GetAccountWallet(ctx, fund.CreatorAccountID); err == nil {
				remarks := fmt.Sprintf("Refund for expired fund %s", fund.ID)
				if _, err := s.CreateTransaction(ctx, TransactionInput{
					PayeeWalletID: &wallet.ID,
					Currency:      fund.Currency,
					Amount:        unclaimed,
					Remarks:       &remarks,
					Type:          TransactionSystem,
					Silent:        true,
				}); err != nil {
					s.Log.Error("fund refund failed", "fund", fund.ID, "error", err)
					continue
				}
			}
		}
		if err := s.DB.WithContext(ctx).Model(&WalletFundEntity{}).Where("id = ?", fund.ID).
			Update("status", FundExpired).Error; err != nil {
			return err
		}
	}
	return nil
}

// ProcessExpiredRaisingFunds settles raising funds whose deadline passed.
func (s *Service) ProcessExpiredRaisingFunds(ctx context.Context) error {
	var funds []WalletFundEntity
	now := time.Now().UTC()
	if err := s.DB.WithContext(ctx).
		Where("is_raising = true AND status IN ? AND deadline_at IS NOT NULL AND deadline_at < ?", []int{FundCreated, FundPartiallyReceived}, now).
		Find(&funds).Error; err != nil {
		return err
	}
	for i := range funds {
		fund := funds[i]
		var recipients []WalletFundRecipientEntity
		if err := s.DB.WithContext(ctx).Where("fund_id = ?", fund.ID).Find(&recipients).Error; err != nil {
			return err
		}
		raised := decimal.Zero
		for _, recipient := range recipients {
			if recipient.IsReceived {
				raised = raised.Add(recipient.Amount)
			}
		}
		status := FundExpired
		if fund.TargetAmount.GreaterThan(decimal.Zero) && raised.GreaterThanOrEqual(fund.TargetAmount) {
			status = FundFullyReceived
		} else {
			for _, contributor := range recipients {
				if !contributor.IsReceived {
					continue
				}
				wallet, _, err := s.GetAccountWallet(ctx, contributor.RecipientAccountID)
				if err != nil {
					continue
				}
				remarks := fmt.Sprintf("Refund for expired raising fund %s", fund.ID)
				if _, err := s.CreateTransaction(ctx, TransactionInput{
					PayeeWalletID: &wallet.ID,
					Currency:      fund.Currency,
					Amount:        contributor.Amount,
					Remarks:       &remarks,
					Type:          TransactionSystem,
					Silent:        true,
				}); err != nil {
					s.Log.Error("raising fund refund failed", "fund", fund.ID, "error", err)
				}
			}
		}
		if err := s.DB.WithContext(ctx).Model(&WalletFundEntity{}).Where("id = ?", fund.ID).
			Update("status", status).Error; err != nil {
			return err
		}
	}
	return nil
}

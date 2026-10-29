package wallet

// Wallet statistics and overview projections.
//
// C# sources:
//
//	../DysonNetwork/DysonNetwork.Wallet/Payment/WalletController.cs (WalletStats)
//	../DysonNetwork/DysonNetwork.Wallet/Payment/PaymentService.cs (WalletOverview)

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"src.solsynth.dev/sosys/stargate/internal/model"
)

// walletStats is the wire shape of WalletController.WalletStats. TotalIncome,
// TotalOutgoing and Sum are computed properties in C#; System.Text.Json
// serializes them, so they are materialized here as well.
type walletStats struct {
	PeriodBegin        *model.Time                `json:"period_begin"`
	PeriodEnd          *model.Time                `json:"period_end"`
	TotalTransactions  int                        `json:"total_transactions"`
	TotalOrders        int                        `json:"total_orders"`
	IncomeCategories   map[string]decimal.Decimal `json:"income_categories"`
	OutgoingCategories map[string]decimal.Decimal `json:"outgoing_categories"`
	TotalIncome        decimal.Decimal            `json:"total_income"`
	TotalOutgoing      decimal.Decimal            `json:"total_outgoing"`
	Sum                decimal.Decimal            `json:"sum"`
}

// transactionSummary mirrors PaymentService.TransactionSummary.
type transactionSummary struct {
	Type       string                     `json:"type"`
	Currencies map[string]currencySummary `json:"currencies"`
}

// currencySummary mirrors PaymentService.CurrencySummary.
type currencySummary struct {
	Currency string          `json:"currency"`
	Income   decimal.Decimal `json:"income"`
	Spending decimal.Decimal `json:"spending"`
	Net      decimal.Decimal `json:"net"`
}

// walletOverview mirrors PaymentService.WalletOverview.
type walletOverview struct {
	AccountID     string                        `json:"account_id"`
	StartDate     *string                       `json:"start_date"`
	EndDate       *string                       `json:"end_date"`
	Summary       map[string]transactionSummary `json:"summary"`
	TotalIncome   decimal.Decimal               `json:"total_income"`
	TotalSpending decimal.Decimal               `json:"total_spending"`
	NetTotal      decimal.Decimal               `json:"net_total"`
}

// walletTypeName renders a TransactionType the way C# ToString() does.
func walletTypeName(t int) string {
	switch t {
	case TransactionTransfer:
		return "Transfer"
	case TransactionOrder:
		return "Order"
	default:
		return "System"
	}
}

// walletTypeKey is the dictionary key the C# serializer emits: dictionary
// keys go through JsonNamingPolicy.SnakeCaseLower, which lowercases the enum
// names ("system", "transfer", "order").
func walletTypeKey(t int) string {
	return strings.ToLower(walletTypeName(t))
}

// resolveAccessibleWallets mirrors WalletController.ResolveAccessibleWalletsAsync:
// no ids resolves the caller's primary wallet, otherwise every requested
// wallet must belong to the caller (realm wallets need publisher membership,
// which Stargate has no port for — superusers keep the C# access rule).
func (h *walletHTTP) resolveAccessibleWallets(ctx context.Context, user *model.Account, accountID uuid.UUID, walletIDs []uuid.UUID) ([]WalletEntity, error) {
	if len(walletIDs) == 0 {
		wallet, _, err := h.d.Service.GetAccountWallet(ctx, accountID)
		if err != nil {
			return nil, err
		}
		return []WalletEntity{*wallet}, nil
	}
	seen := make(map[uuid.UUID]bool, len(walletIDs))
	wallets := make([]WalletEntity, 0, len(walletIDs))
	for _, walletID := range walletIDs {
		if seen[walletID] {
			continue
		}
		seen[walletID] = true
		wallet, _, err := h.d.Service.GetWallet(ctx, walletID)
		if err != nil {
			return nil, err
		}
		if wallet.AccountID != nil && *wallet.AccountID == accountID {
			wallets = append(wallets, *wallet)
			continue
		}
		if wallet.RealmID == nil || user == nil || !user.IsSuperuser {
			return nil, errWalletAccess
		}
		wallets = append(wallets, *wallet)
	}
	return wallets, nil
}

// getWalletStats ports GET /api/wallets/stats.
//
// The C# action caches the result in Redis for an hour. Stargate exposes no
// cache port to feature modules, so the projection is recomputed per request.
func (h *walletHTTP) getWalletStats(c *gin.Context) {
	user, accountID, ok := currentWalletAccount(c)
	if !ok {
		return
	}
	reqCtx := c.Request.Context()
	period := queryInt(c, "period", 30)
	walletIDs, idsOK := queryUUIDList(c, "wallets")
	if !idsOK {
		h.invalidBody(c)
		return
	}
	currencies := normalizeFilters(queryStringList(c, "currencies"), CurrencyPoints)

	wallets, err := h.resolveAccessibleWallets(reqCtx, user, accountID, walletIDs)
	if err != nil || len(wallets) == 0 {
		h.fail(c, "WALLET_NOT_FOUND", "Wallet was not found, please create one first.", http.StatusNotFound)
		return
	}
	walletIDs = make([]uuid.UUID, 0, len(wallets))
	for _, wallet := range wallets {
		walletIDs = append(walletIDs, wallet.ID)
	}

	periodEnd := time.Now().UTC()
	periodBegin := periodEnd.Add(-time.Duration(period) * 24 * time.Hour)

	var transactions []WalletTransactionEntity
	if err := h.d.Service.DB.WithContext(reqCtx).
		Where("(payer_wallet_id IN ? OR payee_wallet_id IN ?)", walletIDs, walletIDs).
		Where("currency IN ?", currencies).
		Where("created_at >= ? AND created_at <= ?", periodBegin, periodEnd).
		Find(&transactions).Error; err != nil {
		h.fail(c, "INTERNAL_ERROR", "Failed to load wallet statistics.", http.StatusInternalServerError)
		return
	}

	var totalOrders int64
	if err := h.d.Service.DB.WithContext(reqCtx).Model(&WalletOrderEntity{}).
		Where("payee_wallet_id IN ?", walletIDs).
		Where("currency IN ?", currencies).
		Where("created_at >= ? AND created_at <= ?", periodBegin, periodEnd).
		Count(&totalOrders).Error; err != nil {
		h.fail(c, "INTERNAL_ERROR", "Failed to load wallet statistics.", http.StatusInternalServerError)
		return
	}

	inWallet := func(id *uuid.UUID) bool {
		if id == nil {
			return false
		}
		for _, walletID := range walletIDs {
			if walletID == *id {
				return true
			}
		}
		return false
	}

	income := map[string]decimal.Decimal{}
	outgoing := map[string]decimal.Decimal{}
	for _, transaction := range transactions {
		if inWallet(transaction.PayeeWalletID) {
			key := walletTypeKey(transaction.Type)
			income[key] = income[key].Add(transaction.Amount)
		}
		if inWallet(transaction.PayerWalletID) {
			key := walletTypeKey(transaction.Type)
			outgoing[key] = outgoing[key].Add(transaction.Amount)
		}
	}

	stats := walletStats{
		PeriodBegin:        timeValue(periodBegin),
		PeriodEnd:          timeValue(periodEnd),
		TotalTransactions:  len(transactions),
		TotalOrders:        int(totalOrders),
		IncomeCategories:   income,
		OutgoingCategories: outgoing,
	}
	for _, amount := range income {
		stats.TotalIncome = stats.TotalIncome.Add(amount)
	}
	for _, amount := range outgoing {
		stats.TotalOutgoing = stats.TotalOutgoing.Add(amount)
	}
	stats.Sum = stats.TotalIncome.Sub(stats.TotalOutgoing)
	c.JSON(http.StatusOK, stats)
}

// getOverview ports GET /api/wallets/overview.
func (h *walletHTTP) getOverview(c *gin.Context) {
	_, accountID, ok := currentWalletAccount(c)
	if !ok {
		return
	}
	start, startOK := queryOptionalTime(c, "start_date")
	end, endOK := queryOptionalTime(c, "end_date")
	if !startOK || !endOK {
		h.fail(c, "WALLET_OVERVIEW_FAILED", "The date filter is invalid.", http.StatusBadRequest)
		return
	}
	overview, err := h.buildOverview(c.Request.Context(), accountID, start, end)
	if err != nil {
		h.fail(c, "WALLET_OVERVIEW_FAILED", err.Error(), http.StatusBadRequest)
		return
	}
	c.JSON(http.StatusOK, overview)
}

// buildOverview mirrors PaymentService.GetWalletOverviewAsync.
func (h *walletHTTP) buildOverview(ctx context.Context, accountID uuid.UUID, start, end *time.Time) (*walletOverview, error) {
	wallet, _, err := h.d.Service.GetAccountWallet(ctx, accountID)
	if err != nil {
		return nil, errWalletNotFound
	}
	query := h.d.Service.DB.WithContext(ctx).Model(&WalletTransactionEntity{}).
		Where("payer_wallet_id = ? OR payee_wallet_id = ?", wallet.ID, wallet.ID)
	if start != nil {
		query = query.Where("created_at >= ?", *start)
	}
	if end != nil {
		query = query.Where("created_at <= ?", *end)
	}
	var transactions []WalletTransactionEntity
	if err := query.Find(&transactions).Error; err != nil {
		return nil, err
	}

	overview := &walletOverview{
		AccountID: uuidString(accountID),
		Summary:   map[string]transactionSummary{},
	}
	if start != nil {
		formatted := start.UTC().Format(time.RFC3339)
		overview.StartDate = &formatted
	}
	if end != nil {
		formatted := end.UTC().Format(time.RFC3339)
		overview.EndDate = &formatted
	}

	type groupKey struct {
		Type     int
		Currency string
	}
	grouped := map[groupKey][]WalletTransactionEntity{}
	for _, transaction := range transactions {
		key := groupKey{Type: transaction.Type, Currency: transaction.Currency}
		grouped[key] = append(grouped[key], transaction)
	}

	for key, rows := range grouped {
		// The dictionary key is snake_cased by the C# serializer, while the
		// Type field keeps the enum name.
		summaryKey := walletTypeKey(key.Type)
		summary, exists := overview.Summary[summaryKey]
		if !exists {
			summary = transactionSummary{Type: walletTypeName(key.Type), Currencies: map[string]currencySummary{}}
		}
		currency := currencySummary{Currency: key.Currency}
		for _, transaction := range rows {
			if transaction.PayeeWalletID != nil && *transaction.PayeeWalletID == wallet.ID {
				currency.Income = currency.Income.Add(transaction.Amount)
			} else if transaction.PayerWalletID != nil && *transaction.PayerWalletID == wallet.ID {
				currency.Spending = currency.Spending.Add(transaction.Amount)
			}
		}
		currency.Net = currency.Income.Sub(currency.Spending)
		summary.Currencies[key.Currency] = currency
		overview.Summary[summaryKey] = summary
	}

	for _, summary := range overview.Summary {
		for _, currency := range summary.Currencies {
			overview.TotalIncome = overview.TotalIncome.Add(currency.Income)
			overview.TotalSpending = overview.TotalSpending.Add(currency.Spending)
		}
	}
	overview.NetTotal = overview.TotalIncome.Sub(overview.TotalSpending)
	return overview, nil
}

// normalizeFilters mirrors WalletController.NormalizeFilters: blank entries are
// dropped, case is folded and an empty set falls back to the default currency.
func normalizeFilters(values []string, defaultValue string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(trimmed)
		if seen[lower] {
			continue
		}
		seen[lower] = true
		out = append(out, lower)
	}
	if len(out) == 0 {
		out = append(out, defaultValue)
	}
	return out
}

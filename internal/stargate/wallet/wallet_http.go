package wallet

// Wallet HTTP surface: a port of DysonNetwork.Wallet's WalletController.
//
// C# source:
//
//	../DysonNetwork/DysonNetwork.Wallet/Payment/WalletController.cs
//
// Routes mirror the controller verbatim: same paths, DTO field names, error
// codes, status codes and the X-Total header on every list endpoint.
//
// Known gap: realm wallets are guarded in C# by publisher membership, and
// Stargate has no publisher/realm port. Realm access is therefore granted to
// superusers and denied with 403 to everybody else.

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/go/pkg/errs"
	"src.solsynth.dev/sosys/stargate/middleware"
	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/permission"
)

var (
	// errWalletAccess marks a wallet the caller may not touch. The C# action
	// answers 404 on the list endpoints and 403 everywhere else.
	errWalletAccess = errors.New("wallet access denied")
	// errWalletNotFound marks a missing (or soft-deleted) wallet.
	errWalletNotFound = errors.New("wallet not found")
)

// walletHTTP binds the wallet domain graph to gin.
type walletHTTP struct {
	d *Deps
}

// log returns the module logger, never nil.
func (h *walletHTTP) log() *slog.Logger {
	if h.d.Log != nil {
		return h.d.Log
	}
	return slog.Default()
}

func registerWalletHTTP(api *gin.RouterGroup, d *Deps) {
	h := &walletHTTP{d: d}
	auth := middleware.RequireAuth()
	ask := func(key string) gin.HandlerFunc { return middleware.AskPermission(d.Perm, key) }

	// Literal segments first: gin keeps the wildcard child last, so the
	// `:id` routes below never shadow `all`, `stats`, `transactions`, ...
	api.POST("/wallets", auth, h.createWallet)
	api.GET("/wallets", auth, h.getWallet)
	api.GET("/wallets/all", auth, h.listWallets)
	api.GET("/wallets/stats", auth, h.getWalletStats)
	api.GET("/wallets/overview", auth, h.getOverview)

	api.GET("/wallets/transactions", auth, h.listTransactions)
	api.GET("/wallets/transactions/pending", auth, h.listPendingTransactions)
	api.GET("/wallets/transactions/:id", auth, h.getTransaction)
	api.POST("/wallets/transactions/:id/confirm", auth, ask(permission.WalletsTransactionsManage), h.confirmTransaction)
	api.POST("/wallets/transactions/:id/reject", auth, ask(permission.WalletsTransactionsManage), h.rejectTransaction)

	api.GET("/wallets/orders", auth, h.listOrders)

	api.GET("/wallets/exchange", h.listExchangeOptions)
	api.POST("/wallets/exchange", auth, ask(permission.WalletsTransactionsManage), h.exchangeCurrency)

	api.POST("/wallets/balance", auth, ask(permission.WalletsBalanceModify), h.modifyBalance)
	api.POST("/wallets/transfer", auth, ask(permission.WalletsTransactionsManage), h.transfer)
	api.POST("/wallets/transfer/requests", auth, ask(permission.WalletsTransferRequestsManage), h.createTransferRequest)
	api.GET("/wallets/transfer/requests/:id", h.getTransferRequest)

	api.POST("/wallets/funds", auth, ask(permission.WalletsFundsManage), h.createFund)
	api.GET("/wallets/funds", auth, h.listFunds)
	api.GET("/wallets/funds/:id", h.getFund)
	api.POST("/wallets/funds/:id/receive", auth, ask(permission.WalletsFundsManage), h.receiveFund)
	api.POST("/wallets/funds/:id/contribute", auth, ask(permission.WalletsFundsManage), h.contributeFund)
	api.GET("/wallets/funds/:id/contributors", h.getFundContributors)

	// Wildcard routes last so the literal segments above always win.
	api.GET("/wallets/:id", auth, h.getWalletByID)

	api.POST("/wallets/:id/default", auth, ask(permission.WalletsManage), h.setDefaultWallet)
	api.POST("/wallets/:id/public-id/enable", auth, ask(permission.WalletsPublicIdManage), h.enablePublicID)
	api.POST("/wallets/:id/public-id/disable", auth, ask(permission.WalletsPublicIdManage), h.disablePublicID)
}

// ── request/response DTOs ──────────────────────────────────────────────────

// createWalletRequest mirrors WalletController.CreateWalletRequest.
type createWalletRequest struct {
	Name    *string `json:"name"`
	RealmID *string `json:"realm_id"`
}

// walletBalanceRequest mirrors WalletController.WalletBalanceRequest.
type walletBalanceRequest struct {
	Remark         *string         `json:"remark"`
	Amount         decimal.Decimal `json:"amount"`
	Currency       string          `json:"currency"`
	AccountID      *string         `json:"account_id"`
	WalletID       *string         `json:"wallet_id"`
	ForceOperation bool            `json:"force_operation"`
}

// walletExchangeRequest mirrors WalletController.WalletExchangeRequest.
type walletExchangeRequest struct {
	Amount   decimal.Decimal `json:"amount"`
	Currency string          `json:"currency"`
	WalletID *string         `json:"wallet_id"`
	Remark   *string         `json:"remark"`
}

// walletExchangeOption mirrors WalletController.WalletExchangeOption.
type walletExchangeOption struct {
	SourceCurrency string          `json:"source_currency"`
	SourceAmount   decimal.Decimal `json:"source_amount"`
	TargetCurrency string          `json:"target_currency"`
	TargetAmount   decimal.Decimal `json:"target_amount"`
}

// walletExchangeResponse mirrors WalletController.WalletExchangeResponse.
type walletExchangeResponse struct {
	WalletID          string          `json:"wallet_id"`
	SourceCurrency    string          `json:"source_currency"`
	SourceAmount      decimal.Decimal `json:"source_amount"`
	TargetCurrency    string          `json:"target_currency"`
	TargetAmount      decimal.Decimal `json:"target_amount"`
	DebitTransaction  Transaction     `json:"debit_transaction"`
	CreditTransaction Transaction     `json:"credit_transaction"`
}

// walletTransferRequest mirrors WalletController.WalletTransferRequest.
type walletTransferRequest struct {
	Remark              *string         `json:"remark"`
	Amount              decimal.Decimal `json:"amount"`
	Currency            string          `json:"currency"`
	PayerWalletID       *string         `json:"payer_wallet_id"`
	PayeeAccountID      *string         `json:"payee_account_id"`
	PayeeWalletID       *string         `json:"payee_wallet_id"`
	PayeePublicID       *string         `json:"payee_public_id"`
	PinCode             *string         `json:"pin_code"`
	Freeze              bool            `json:"freeze"`
	RequireConfirmation bool            `json:"require_confirmation"`
	TransferRequestID   *string         `json:"transfer_request_id"`
}

// createWalletTransferRequestRequest mirrors the C# DTO of the same name.
type createWalletTransferRequestRequest struct {
	Amount              decimal.Decimal `json:"amount"`
	Currency            string          `json:"currency"`
	Remark              *string         `json:"remark"`
	WalletID            *string         `json:"wallet_id"`
	ExpirationHours     int             `json:"expiration_hours"`
	Freeze              bool            `json:"freeze"`
	RequireConfirmation bool            `json:"require_confirmation"`
}

// walletTransferRequestResponse mirrors WalletController.WalletTransferRequestResponse.
type walletTransferRequestResponse struct {
	ID                  string          `json:"id"`
	Status              int             `json:"status"`
	Currency            string          `json:"currency"`
	Amount              decimal.Decimal `json:"amount"`
	Remark              *string         `json:"remark"`
	Freeze              bool            `json:"freeze"`
	RequireConfirmation bool            `json:"require_confirmation"`
	ExpiresAt           *model.Time     `json:"expires_at"`
	FulfilledAt         *model.Time     `json:"fulfilled_at"`
	PayeeWalletID       string          `json:"payee_wallet_id"`
	PayeePublicID       *string         `json:"payee_public_id"`
	TransactionID       *string         `json:"transaction_id"`
}

// createFundRequest mirrors WalletController.CreateFundRequest. IsOpen is a
// pointer because the C# DTO defaults it to true.
type createFundRequest struct {
	RecipientAccountIDs []string        `json:"recipient_account_ids"`
	Currency            string          `json:"currency"`
	TotalAmount         decimal.Decimal `json:"total_amount"`
	AmountOfSplits      int             `json:"amount_of_splits"`
	SplitType           int             `json:"split_type"`
	Message             *string         `json:"message"`
	ExpirationHours     *int            `json:"expiration_hours"`
	PinCode             *string         `json:"pin_code"`
	IsRaising           bool            `json:"is_raising"`
	TargetAmount        decimal.Decimal `json:"target_amount"`
	ContributionType    int             `json:"contribution_type"`
	ContributionAmount  decimal.Decimal `json:"contribution_amount"`
	IsOpen              *bool           `json:"is_open"`
	DeadlineAt          *model.Time     `json:"deadline_at"`
}

// contributeFundRequest mirrors WalletController.ContributeFundRequest.
type contributeFundRequest struct {
	Amount decimal.Decimal `json:"amount"`
}

// exchangeRate mirrors WalletController.CurrencyExchangeRate.
type exchangeRate struct {
	SourceCurrency string
	SourceAmount   decimal.Decimal
	TargetCurrency string
	TargetAmount   decimal.Decimal
}

// ── helpers ────────────────────────────────────────────────────────────────

// fail writes one ApiError.
func (h *walletHTTP) fail(c *gin.Context, code, message string, status int) {
	c.JSON(status, errs.New(code, message, status))
}

// currentWalletAccount resolves the authenticated account and its uuid.
func currentWalletAccount(c *gin.Context) (*model.Account, uuid.UUID, bool) {
	user := middleware.CurrentUser(c.Request.Context())
	if user == nil {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized("Authentication is required."))
		return nil, uuid.Nil, false
	}
	id, err := uuid.Parse(user.Id)
	if err != nil {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized("Authentication is required."))
		return nil, uuid.Nil, false
	}
	return user, id, true
}

// parseOptionalUUID parses an optional uuid string; ok is false when malformed.
func parseOptionalUUID(value *string) (*uuid.UUID, bool) {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil, true
	}
	id, err := uuid.Parse(strings.TrimSpace(*value))
	if err != nil {
		return nil, false
	}
	return &id, true
}

// pathID parses the :id path parameter.
func pathID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return uuid.Nil, false
	}
	return id, true
}

// queryInt reads an integer query parameter, falling back on absence or junk.
func queryInt(c *gin.Context, key string, fallback int) int {
	raw := strings.TrimSpace(c.Query(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return value
}

// queryStringList reads repeated and comma-separated query values.
func queryStringList(c *gin.Context, key string) []string {
	var out []string
	for _, raw := range c.QueryArray(key) {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

// queryUUIDList reads repeated and comma-separated uuid query values.
func queryUUIDList(c *gin.Context, key string) ([]uuid.UUID, bool) {
	out := make([]uuid.UUID, 0, 4)
	for _, raw := range c.QueryArray(key) {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			id, err := uuid.Parse(part)
			if err != nil {
				return nil, false
			}
			out = append(out, id)
		}
	}
	return out, true
}

// queryOptionalTime parses an optional ISO-8601 query value.
func queryOptionalTime(c *gin.Context, key string) (*time.Time, bool) {
	raw := strings.TrimSpace(c.Query(key))
	if raw == "" {
		return nil, true
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02"} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			utc := parsed.UTC()
			return &utc, true
		}
	}
	return nil, false
}

// setTotal writes the X-Total pagination header.
func setTotal(c *gin.Context, total int64) {
	c.Header("X-Total", strconv.FormatInt(total, 10))
}

// accessibleWallet mirrors ResolveAccessibleWalletAsync: a nil id resolves the
// caller's primary wallet, and a realm wallet is only reachable by a superuser
// (Stargate has no publisher membership port).
func (h *walletHTTP) accessibleWallet(ctx context.Context, user *model.Account, accountID uuid.UUID, walletID *uuid.UUID) (*WalletEntity, error) {
	if walletID == nil {
		wallet, _, err := h.d.Service.GetAccountWallet(ctx, accountID)
		if err != nil {
			return nil, errWalletNotFound
		}
		return wallet, nil
	}
	wallet, _, err := h.d.Service.GetWallet(ctx, *walletID)
	if err != nil {
		return nil, errWalletNotFound
	}
	if wallet.AccountID != nil && *wallet.AccountID == accountID {
		return wallet, nil
	}
	if wallet.RealmID != nil && user != nil && user.IsSuperuser {
		return wallet, nil
	}
	return nil, errWalletAccess
}

// renderWallet loads a wallet with its pockets.
func (h *walletHTTP) renderWallet(ctx context.Context, id uuid.UUID) (Wallet, error) {
	entity, pockets, err := h.d.Service.GetWallet(ctx, id)
	if err != nil {
		return Wallet{}, err
	}
	return walletModel(entity, pockets), nil
}

// walletModels renders wallets with their pockets in two queries.
func (h *walletHTTP) walletModels(ctx context.Context, wallets []WalletEntity) ([]Wallet, error) {
	out := make([]Wallet, 0, len(wallets))
	if len(wallets) == 0 {
		return out, nil
	}
	ids := make([]uuid.UUID, 0, len(wallets))
	for _, wallet := range wallets {
		ids = append(ids, wallet.ID)
	}
	var pockets []WalletPocketEntity
	if err := h.d.Service.DB.WithContext(ctx).Where("wallet_id IN ?", ids).
		Order("created_at").Find(&pockets).Error; err != nil {
		return nil, err
	}
	byWallet := make(map[uuid.UUID][]WalletPocketEntity, len(wallets))
	for _, pocket := range pockets {
		byWallet[pocket.WalletID] = append(byWallet[pocket.WalletID], pocket)
	}
	for i := range wallets {
		out = append(out, walletModel(&wallets[i], byWallet[wallets[i].ID]))
	}
	return out, nil
}

// walletRefs renders wallets without pockets, keyed by id, for the nested
// wallet references of transactions and orders.
func (h *walletHTTP) walletRefs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]Wallet, error) {
	out := make(map[uuid.UUID]Wallet, len(ids))
	unique := make([]uuid.UUID, 0, len(ids))
	seen := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		unique = append(unique, id)
	}
	if len(unique) == 0 {
		return out, nil
	}
	var wallets []WalletEntity
	if err := h.d.Service.DB.WithContext(ctx).Where("id IN ?", unique).Find(&wallets).Error; err != nil {
		return nil, err
	}
	for i := range wallets {
		out[wallets[i].ID] = walletModel(&wallets[i], nil)
	}
	return out, nil
}

// hydrateTransaction fills the nested payer/payee wallets of one transaction.
func hydrateTransaction(out *Transaction, refs map[uuid.UUID]Wallet, e *WalletTransactionEntity) {
	if e.PayerWalletID != nil {
		if wallet, ok := refs[*e.PayerWalletID]; ok {
			wallet := wallet
			out.PayerWallet = &wallet
		}
	}
	if e.PayeeWalletID != nil {
		if wallet, ok := refs[*e.PayeeWalletID]; ok {
			wallet := wallet
			out.PayeeWallet = &wallet
		}
	}
}

// createLedgerEntry writes one confirmed System ledger row and moves the
// matching pocket balances inside tx. It mirrors CreateTransactionAsync with
// type=System and no freeze/confirmation flags, which is what the exchange and
// fund-contribution paths need.
func (h *walletHTTP) createLedgerEntry(ctx context.Context, tx *gorm.DB, payerWalletID, payeeWalletID *uuid.UUID, currency string, amount decimal.Decimal, remarks *string) (*WalletTransactionEntity, error) {
	amount = truncate3(amount)
	now := time.Now().UTC()
	entry := WalletTransactionEntity{
		ID:            newID(),
		Currency:      currency,
		Amount:        amount,
		Remarks:       remarks,
		Type:          TransactionSystem,
		Status:        TransactionConfirmed,
		ConfirmedAt:   &now,
		PayerWalletID: payerWalletID,
		PayeeWalletID: payeeWalletID,
	}
	if payerWalletID != nil {
		pocket, created, err := h.d.Service.GetOrCreatePocket(ctx, tx, *payerWalletID, currency, nil)
		if err != nil {
			return nil, err
		}
		if created || pocket.Amount.LessThan(amount) {
			return nil, ErrInsufficientFunds
		}
		if err := addToPocket(tx, pocket.ID, amount.Neg(), decimal.Zero); err != nil {
			return nil, err
		}
	}
	if payeeWalletID != nil {
		pocket, created, err := h.d.Service.GetOrCreatePocket(ctx, tx, *payeeWalletID, currency, &amount)
		if err != nil {
			return nil, err
		}
		if !created {
			if err := addToPocket(tx, pocket.ID, amount, decimal.Zero); err != nil {
				return nil, err
			}
		}
	}
	if err := tx.Create(&entry).Error; err != nil {
		return nil, err
	}
	return &entry, nil
}

// ── wallets ────────────────────────────────────────────────────────────────

// POST /api/wallets
func (h *walletHTTP) createWallet(c *gin.Context) {
	user, accountID, ok := currentWalletAccount(c)
	if !ok {
		return
	}
	var request createWalletRequest
	if err := c.ShouldBindJSON(&request); err != nil && !errors.Is(err, io.EOF) {
		h.fail(c, "WALLET_CREATE_FAILED", err.Error(), http.StatusBadRequest)
		return
	}
	realmID, realmOK := parseOptionalUUID(request.RealmID)
	if !realmOK {
		h.fail(c, "WALLET_CREATE_FAILED", "Realm id is invalid.", http.StatusBadRequest)
		return
	}
	if realmID != nil {
		// C# resolves the realm publisher and requires editor membership.
		// Stargate has no publisher port, so only superusers may create one.
		if !user.IsSuperuser {
			c.JSON(http.StatusForbidden, errs.Forbidden("You must be an editor of the realm publisher to create a realm wallet."))
			return
		}
		wallet, err := h.d.Service.CreateWallet(c.Request.Context(), nil, realmID, request.Name)
		if err != nil {
			h.fail(c, "WALLET_CREATE_FAILED", err.Error(), http.StatusBadRequest)
			return
		}
		c.JSON(http.StatusOK, walletModel(wallet, nil))
		return
	}
	wallet, err := h.d.Service.CreateWallet(c.Request.Context(), &accountID, nil, request.Name)
	if err != nil {
		h.fail(c, "WALLET_CREATE_FAILED", err.Error(), http.StatusBadRequest)
		return
	}
	c.JSON(http.StatusOK, walletModel(wallet, nil))
}

// GET /api/wallets
func (h *walletHTTP) getWallet(c *gin.Context) {
	_, accountID, ok := currentWalletAccount(c)
	if !ok {
		return
	}
	wallet, pockets, err := h.d.Service.GetAccountWallet(c.Request.Context(), accountID)
	if err != nil {
		h.fail(c, "WALLET_NOT_FOUND", "Wallet was not found, please create one first.", http.StatusNotFound)
		return
	}
	c.JSON(http.StatusOK, walletModel(wallet, pockets))
}

// GET /api/wallets/all
func (h *walletHTTP) listWallets(c *gin.Context) {
	_, accountID, ok := currentWalletAccount(c)
	if !ok {
		return
	}
	// C# also appends the wallets of every realm the account belongs to;
	// Stargate has no publisher membership port, so those cannot be resolved.
	wallets, err := h.d.Service.GetAccountWallets(c.Request.Context(), accountID)
	if err != nil {
		h.fail(c, "INTERNAL_ERROR", "Failed to load wallets.", http.StatusInternalServerError)
		return
	}
	models, err := h.walletModels(c.Request.Context(), wallets)
	if err != nil {
		h.fail(c, "INTERNAL_ERROR", "Failed to load wallets.", http.StatusInternalServerError)
		return
	}
	c.JSON(http.StatusOK, models)
}

// GET /api/wallets/:id
func (h *walletHTTP) getWalletByID(c *gin.Context) {
	user, accountID, ok := currentWalletAccount(c)
	if !ok {
		return
	}
	id, ok := pathID(c)
	if !ok {
		h.fail(c, "WALLET_NOT_FOUND", "Wallet was not found.", http.StatusNotFound)
		return
	}
	wallet, pockets, err := h.d.Service.GetWallet(c.Request.Context(), id)
	if err != nil {
		h.fail(c, "WALLET_NOT_FOUND", "Wallet was not found.", http.StatusNotFound)
		return
	}
	if wallet.AccountID != nil && *wallet.AccountID == accountID {
		c.JSON(http.StatusOK, walletModel(wallet, pockets))
		return
	}
	if wallet.RealmID != nil && user.IsSuperuser {
		c.JSON(http.StatusOK, walletModel(wallet, pockets))
		return
	}
	c.JSON(http.StatusForbidden, errs.Forbidden("You do not have permission to perform this action."))
}

// POST /api/wallets/:id/default
func (h *walletHTTP) setDefaultWallet(c *gin.Context) {
	_, accountID, ok := currentWalletAccount(c)
	if !ok {
		return
	}
	id, ok := pathID(c)
	if !ok {
		h.fail(c, "WALLET_NOT_FOUND", "Wallet was not found.", http.StatusNotFound)
		return
	}
	wallet, _, err := h.d.Service.GetWallet(c.Request.Context(), id)
	if err != nil {
		h.fail(c, "WALLET_NOT_FOUND", "Wallet was not found.", http.StatusNotFound)
		return
	}
	if wallet.AccountID == nil || *wallet.AccountID != accountID {
		c.JSON(http.StatusForbidden, errs.Forbidden("You do not have permission to perform this action."))
		return
	}
	if _, err := h.d.Service.SetPrimaryWallet(c.Request.Context(), id); err != nil {
		h.fail(c, "INTERNAL_ERROR", err.Error(), http.StatusInternalServerError)
		return
	}
	rendered, err := h.renderWallet(c.Request.Context(), id)
	if err != nil {
		h.fail(c, "INTERNAL_ERROR", err.Error(), http.StatusInternalServerError)
		return
	}
	c.JSON(http.StatusOK, rendered)
}

// POST /api/wallets/:id/public-id/enable
func (h *walletHTTP) enablePublicID(c *gin.Context) {
	h.togglePublicID(c, true)
}

// POST /api/wallets/:id/public-id/disable
func (h *walletHTTP) disablePublicID(c *gin.Context) {
	h.togglePublicID(c, false)
}

// togglePublicID is the shared body of the enable/disable actions: the owner
// or a realm editor may toggle, and realm editorship is superuser-only here.
func (h *walletHTTP) togglePublicID(c *gin.Context, enable bool) {
	user, accountID, ok := currentWalletAccount(c)
	if !ok {
		return
	}
	id, ok := pathID(c)
	if !ok {
		h.fail(c, "WALLET_NOT_FOUND", "Wallet was not found.", http.StatusNotFound)
		return
	}
	wallet, _, err := h.d.Service.GetWallet(c.Request.Context(), id)
	if err != nil {
		h.fail(c, "WALLET_NOT_FOUND", "Wallet was not found.", http.StatusNotFound)
		return
	}
	allowed := wallet.AccountID != nil && *wallet.AccountID == accountID
	if !allowed && wallet.RealmID != nil && user.IsSuperuser {
		allowed = true
	}
	if !allowed {
		c.JSON(http.StatusForbidden, errs.Forbidden("You do not have permission to perform this action."))
		return
	}
	if enable {
		_, err = h.d.Service.EnablePublicID(c.Request.Context(), id)
	} else {
		_, err = h.d.Service.DisablePublicID(c.Request.Context(), id)
	}
	if err != nil {
		h.fail(c, "INTERNAL_ERROR", err.Error(), http.StatusInternalServerError)
		return
	}
	rendered, err := h.renderWallet(c.Request.Context(), id)
	if err != nil {
		h.fail(c, "INTERNAL_ERROR", err.Error(), http.StatusInternalServerError)
		return
	}
	c.JSON(http.StatusOK, rendered)
}

// ── transactions ───────────────────────────────────────────────────────────

// GET /api/wallets/transactions
func (h *walletHTTP) listTransactions(c *gin.Context) {
	user, accountID, ok := currentWalletAccount(c)
	if !ok {
		return
	}
	reqCtx := c.Request.Context()
	walletID, walletOK := parseOptionalUUID(strPtrIfNotEmpty(c.Query("wallet")))
	if !walletOK {
		h.invalidBody(c)
		return
	}
	target, err := h.accessibleWallet(reqCtx, user, accountID, walletID)
	if err != nil {
		h.fail(c, "WALLET_NOT_FOUND", "Wallet was not found.", http.StatusNotFound)
		return
	}

	query := h.d.Service.DB.WithContext(reqCtx).Model(&WalletTransactionEntity{}).
		Where("(payee_wallet_id = ? OR payer_wallet_id = ?)", target.ID, target.ID)
	direction := strings.ToLower(strings.TrimSpace(c.Query("direction")))
	switch direction {
	case "":
	case "income", "incoming", "in":
		query = query.Where("payee_wallet_id = ?", target.ID)
	case "outcome", "outgoing", "out":
		query = query.Where("payer_wallet_id = ?", target.ID)
	default:
		h.fail(c, "WALLET_INVALID_DIRECTION", "Invalid direction. Use income or outcome.", http.StatusBadRequest)
		return
	}
	if raw := strings.TrimSpace(c.Query("type")); raw != "" {
		txType, convErr := strconv.Atoi(raw)
		if convErr != nil {
			h.invalidBody(c)
			return
		}
		query = query.Where("type = ?", txType)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		h.fail(c, "INTERNAL_ERROR", "Failed to load transactions.", http.StatusInternalServerError)
		return
	}
	setTotal(c, total)

	offset := max(queryInt(c, "offset", 0), 0)
	take := max(queryInt(c, "take", 20), 0)
	var transactions []WalletTransactionEntity
	if err := query.Order("created_at DESC").Offset(offset).Limit(take).
		Find(&transactions).Error; err != nil {
		h.fail(c, "INTERNAL_ERROR", "Failed to load transactions.", http.StatusInternalServerError)
		return
	}

	refIDs := make([]uuid.UUID, 0, len(transactions)*2)
	for _, transaction := range transactions {
		if transaction.PayerWalletID != nil {
			refIDs = append(refIDs, *transaction.PayerWalletID)
		}
		if transaction.PayeeWalletID != nil {
			refIDs = append(refIDs, *transaction.PayeeWalletID)
		}
	}
	refs, err := h.walletRefs(reqCtx, refIDs)
	if err != nil {
		h.fail(c, "INTERNAL_ERROR", "Failed to load transactions.", http.StatusInternalServerError)
		return
	}
	response := make([]Transaction, 0, len(transactions))
	for i := range transactions {
		item := transactionModel(&transactions[i])
		hydrateTransaction(&item, refs, &transactions[i])
		response = append(response, item)
	}
	c.JSON(http.StatusOK, response)
}

// GET /api/wallets/transactions/:id
func (h *walletHTTP) getTransaction(c *gin.Context) {
	user, accountID, ok := currentWalletAccount(c)
	if !ok {
		return
	}
	reqCtx := c.Request.Context()
	id, ok := pathID(c)
	if !ok {
		h.fail(c, "WALLET_TRANSACTION_NOT_FOUND", "Transaction was not found.", http.StatusNotFound)
		return
	}
	var transaction WalletTransactionEntity
	if err := h.d.Service.DB.WithContext(reqCtx).First(&transaction, "id = ?", id).Error; err != nil {
		h.fail(c, "WALLET_TRANSACTION_NOT_FOUND", "Transaction was not found.", http.StatusNotFound)
		return
	}

	accessible := false
	refIDs := make([]uuid.UUID, 0, 2)
	for _, walletID := range []*uuid.UUID{transaction.PayerWalletID, transaction.PayeeWalletID} {
		if walletID == nil {
			continue
		}
		refIDs = append(refIDs, *walletID)
		if _, err := h.accessibleWallet(reqCtx, user, accountID, walletID); err == nil {
			accessible = true
		}
	}
	if !accessible {
		h.fail(c, "WALLET_TRANSACTION_NOT_FOUND", "Transaction was not found.", http.StatusNotFound)
		return
	}

	refs, err := h.walletRefs(reqCtx, refIDs)
	if err != nil {
		h.fail(c, "INTERNAL_ERROR", "Failed to load the transaction.", http.StatusInternalServerError)
		return
	}
	response := transactionModel(&transaction)
	hydrateTransaction(&response, refs, &transaction)
	c.JSON(http.StatusOK, response)
}

// GET /api/wallets/transactions/pending
func (h *walletHTTP) listPendingTransactions(c *gin.Context) {
	_, accountID, ok := currentWalletAccount(c)
	if !ok {
		return
	}
	reqCtx := c.Request.Context()
	wallet, _, err := h.d.Service.GetAccountWallet(reqCtx, accountID)
	if err != nil {
		h.fail(c, "WALLET_NOT_FOUND", "Wallet not found.", http.StatusNotFound)
		return
	}
	query := h.d.Service.DB.WithContext(reqCtx).Model(&WalletTransactionEntity{}).
		Where("payee_wallet_id = ?", wallet.ID).
		Where("status IN ?", []int{TransactionPending, TransactionFrozen}).
		Where("require_confirmation")
	var total int64
	if err := query.Count(&total).Error; err != nil {
		h.fail(c, "INTERNAL_ERROR", "Failed to load pending transactions.", http.StatusInternalServerError)
		return
	}
	setTotal(c, total)
	offset := max(queryInt(c, "offset", 0), 0)
	take := max(queryInt(c, "take", 20), 0)
	var transactions []WalletTransactionEntity
	if err := query.Order("created_at DESC").Offset(offset).Limit(take).
		Find(&transactions).Error; err != nil {
		h.fail(c, "INTERNAL_ERROR", "Failed to load pending transactions.", http.StatusInternalServerError)
		return
	}
	refIDs := make([]uuid.UUID, 0, len(transactions))
	for _, transaction := range transactions {
		if transaction.PayerWalletID != nil {
			refIDs = append(refIDs, *transaction.PayerWalletID)
		}
	}
	refs, err := h.walletRefs(reqCtx, refIDs)
	if err != nil {
		h.fail(c, "INTERNAL_ERROR", "Failed to load pending transactions.", http.StatusInternalServerError)
		return
	}
	response := make([]Transaction, 0, len(transactions))
	for i := range transactions {
		item := transactionModel(&transactions[i])
		hydrateTransaction(&item, refs, &transactions[i])
		response = append(response, item)
	}
	c.JSON(http.StatusOK, response)
}

// POST /api/wallets/transactions/:id/confirm
func (h *walletHTTP) confirmTransaction(c *gin.Context) {
	_, accountID, ok := currentWalletAccount(c)
	if !ok {
		return
	}
	id, ok := pathID(c)
	if !ok {
		h.fail(c, "WALLET_TRANSACTION_CONFIRM_FAILED", "Transaction was not found.", http.StatusBadRequest)
		return
	}
	transaction, err := h.d.Service.ConfirmTransaction(c.Request.Context(), id, accountID)
	if err != nil {
		h.fail(c, "WALLET_TRANSACTION_CONFIRM_FAILED", err.Error(), http.StatusBadRequest)
		return
	}
	c.JSON(http.StatusOK, transactionModel(transaction))
}

// POST /api/wallets/transactions/:id/reject
func (h *walletHTTP) rejectTransaction(c *gin.Context) {
	_, accountID, ok := currentWalletAccount(c)
	if !ok {
		return
	}
	id, ok := pathID(c)
	if !ok {
		h.fail(c, "WALLET_TRANSACTION_REJECT_FAILED", "Transaction was not found.", http.StatusBadRequest)
		return
	}
	transaction, err := h.d.Service.RejectTransaction(c.Request.Context(), id, accountID)
	if err != nil {
		h.fail(c, "WALLET_TRANSACTION_REJECT_FAILED", err.Error(), http.StatusBadRequest)
		return
	}
	c.JSON(http.StatusOK, transactionModel(transaction))
}

// ── orders ─────────────────────────────────────────────────────────────────

// GET /api/wallets/orders
func (h *walletHTTP) listOrders(c *gin.Context) {
	user, accountID, ok := currentWalletAccount(c)
	if !ok {
		return
	}
	reqCtx := c.Request.Context()
	walletID, walletOK := parseOptionalUUID(strPtrIfNotEmpty(c.Query("wallet")))
	if !walletOK {
		h.invalidBody(c)
		return
	}
	target, err := h.accessibleWallet(reqCtx, user, accountID, walletID)
	if err != nil {
		h.fail(c, "WALLET_NOT_FOUND", "Wallet was not found.", http.StatusNotFound)
		return
	}

	const join = "JOIN payment_transactions ON payment_transactions.id = payment_orders.transaction_id"
	base := func() *gorm.DB {
		return h.d.Service.DB.WithContext(reqCtx).Model(&WalletOrderEntity{}).
			Joins(join).
			Where("payment_transactions.deleted_at IS NULL").
			Where("(payment_transactions.payee_wallet_id = ? OR payment_transactions.payer_wallet_id = ?)", target.ID, target.ID)
	}
	query := base()
	direction := strings.ToLower(strings.TrimSpace(c.Query("direction")))
	switch direction {
	case "":
	case "income", "incoming", "in":
		query = query.Where("payment_transactions.payee_wallet_id = ?", target.ID)
	case "outcome", "outgoing", "out":
		query = query.Where("payment_transactions.payer_wallet_id = ?", target.ID)
	default:
		h.fail(c, "WALLET_INVALID_DIRECTION", "Invalid direction. Use income or outcome.", http.StatusBadRequest)
		return
	}
	if raw := strings.TrimSpace(c.Query("type")); raw != "" {
		txType, convErr := strconv.Atoi(raw)
		if convErr != nil {
			h.invalidBody(c)
			return
		}
		query = query.Where("payment_transactions.type = ?", txType)
	}
	if raw := strings.TrimSpace(c.Query("status")); raw != "" {
		status, convErr := strconv.Atoi(raw)
		if convErr != nil {
			h.invalidBody(c)
			return
		}
		query = query.Where("payment_orders.status = ?", status)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		h.fail(c, "INTERNAL_ERROR", "Failed to load orders.", http.StatusInternalServerError)
		return
	}
	setTotal(c, total)

	offset := max(queryInt(c, "offset", 0), 0)
	take := max(queryInt(c, "take", 20), 0)
	var orders []WalletOrderEntity
	if err := query.Select("payment_orders.*").Order("payment_orders.created_at DESC").
		Offset(offset).Limit(take).Find(&orders).Error; err != nil {
		h.fail(c, "INTERNAL_ERROR", "Failed to load orders.", http.StatusInternalServerError)
		return
	}
	items, err := h.orderItems(reqCtx, orders)
	if err != nil {
		h.fail(c, "INTERNAL_ERROR", "Failed to load orders.", http.StatusInternalServerError)
		return
	}
	transactions, err := h.orderTransactions(reqCtx, orders)
	if err != nil {
		h.fail(c, "INTERNAL_ERROR", "Failed to load orders.", http.StatusInternalServerError)
		return
	}
	response := make([]Order, 0, len(orders))
	for i := range orders {
		order := orders[i]
		item := orderModel(&order, items[order.ID])
		if transaction, ok := transactions[order.ID]; ok {
			transaction := transaction
			item.Transaction = &transaction
		}
		response = append(response, item)
	}
	c.JSON(http.StatusOK, response)
}

// orderItems loads the line items of a page of orders in one query.
func (h *walletHTTP) orderItems(ctx context.Context, orders []WalletOrderEntity) (map[uuid.UUID][]WalletOrderItemEntity, error) {
	out := make(map[uuid.UUID][]WalletOrderItemEntity, len(orders))
	if len(orders) == 0 {
		return out, nil
	}
	ids := make([]uuid.UUID, 0, len(orders))
	for _, order := range orders {
		ids = append(ids, order.ID)
	}
	var items []WalletOrderItemEntity
	if err := h.d.Service.DB.WithContext(ctx).Where("order_id IN ?", ids).Find(&items).Error; err != nil {
		return nil, err
	}
	for _, item := range items {
		out[item.OrderID] = append(out[item.OrderID], item)
	}
	return out, nil
}

// orderTransactions loads the ledger entry of a page of orders in one query.
func (h *walletHTTP) orderTransactions(ctx context.Context, orders []WalletOrderEntity) (map[uuid.UUID]Transaction, error) {
	out := make(map[uuid.UUID]Transaction, len(orders))
	ids := make([]uuid.UUID, 0, len(orders))
	for _, order := range orders {
		if order.TransactionID != nil {
			ids = append(ids, *order.TransactionID)
		}
	}
	if len(ids) == 0 {
		return out, nil
	}
	var transactions []WalletTransactionEntity
	if err := h.d.Service.DB.WithContext(ctx).Where("id IN ?", ids).Find(&transactions).Error; err != nil {
		return nil, err
	}
	byID := make(map[uuid.UUID]Transaction, len(transactions))
	for i := range transactions {
		byID[transactions[i].ID] = transactionModel(&transactions[i])
	}
	for _, order := range orders {
		if order.TransactionID == nil {
			continue
		}
		if transaction, ok := byID[*order.TransactionID]; ok {
			out[order.ID] = transaction
		}
	}
	return out, nil
}

// strPtrIfNotEmpty keeps query parsing terse for optional uuid parameters.
func strPtrIfNotEmpty(value string) *string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

// ── exchange ───────────────────────────────────────────────────────────────

// GET /api/wallets/exchange (anonymous)
func (h *walletHTTP) listExchangeOptions(c *gin.Context) {
	rates, err := h.exchangeRates()
	if err != nil {
		h.log().Error("invalid wallet currency exchange configuration", "error", err)
		h.fail(c, "WALLET_EXCHANGE_CONFIGURATION_INVALID", "Currency exchange configuration is invalid.", http.StatusInternalServerError)
		return
	}
	options := make([]walletExchangeOption, 0, len(rates))
	for _, rate := range rates {
		options = append(options, walletExchangeOption{
			SourceCurrency: rate.SourceCurrency,
			SourceAmount:   rate.SourceAmount,
			TargetCurrency: rate.TargetCurrency,
			TargetAmount:   rate.TargetAmount,
		})
	}
	c.JSON(http.StatusOK, options)
}

// POST /api/wallets/exchange
func (h *walletHTTP) exchangeCurrency(c *gin.Context) {
	user, accountID, ok := currentWalletAccount(c)
	if !ok {
		return
	}
	reqCtx := c.Request.Context()
	var request walletExchangeRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.fail(c, "WALLET_EXCHANGE_INVALID_REQUEST", "Amount must be greater than zero and currency is required.", http.StatusBadRequest)
		return
	}
	if request.Amount.LessThanOrEqual(decimal.Zero) || strings.TrimSpace(request.Currency) == "" {
		h.fail(c, "WALLET_EXCHANGE_INVALID_REQUEST", "Amount must be greater than zero and currency is required.", http.StatusBadRequest)
		return
	}
	rates, err := h.exchangeRates()
	if err != nil {
		h.log().Error("invalid wallet currency exchange configuration", "error", err)
		h.fail(c, "WALLET_EXCHANGE_CONFIGURATION_INVALID", "Currency exchange configuration is invalid.", http.StatusInternalServerError)
		return
	}
	rate, found := findExchangeRate(rates, request.Currency)
	if !found {
		h.fail(c, "WALLET_EXCHANGE_NOT_SUPPORTED", "This currency exchange is not configured.", http.StatusBadRequest)
		return
	}

	walletID, walletOK := parseOptionalUUID(request.WalletID)
	if !walletOK {
		h.fail(c, "WALLET_NOT_FOUND", "Wallet was not found.", http.StatusNotFound)
		return
	}
	var wallet *WalletEntity
	if walletID != nil {
		entity, _, err := h.d.Service.GetWallet(reqCtx, *walletID)
		if err != nil {
			h.fail(c, "WALLET_NOT_FOUND", "Wallet was not found.", http.StatusNotFound)
			return
		}
		if entity.AccountID == nil || *entity.AccountID != accountID {
			switch {
			case entity.RealmID == nil:
				c.JSON(http.StatusForbidden, errs.Forbidden("You do not have permission to exchange from this wallet."))
				return
			case user == nil || !user.IsSuperuser:
				c.JSON(http.StatusForbidden, errs.Forbidden("You must be an editor of the realm publisher to exchange from this wallet."))
				return
			}
		}
		wallet = entity
	} else {
		entity, _, err := h.d.Service.GetAccountWallet(reqCtx, accountID)
		if err != nil {
			h.fail(c, "WALLET_NOT_FOUND", "Wallet was not found.", http.StatusNotFound)
			return
		}
		wallet = entity
	}

	sourceAmount := truncate3(request.Amount)
	if sourceAmount.LessThanOrEqual(decimal.Zero) {
		h.fail(c, "WALLET_EXCHANGE_INVALID_AMOUNT", "Amount must be at least 0.001.", http.StatusBadRequest)
		return
	}
	remarks := request.Remark
	if remarks == nil || strings.TrimSpace(*remarks) == "" {
		defaultRemark := "Currency exchange: " + rate.SourceCurrency + " to " + rate.TargetCurrency
		remarks = &defaultRemark
	}

	var debit, credit *WalletTransactionEntity
	err = h.d.Service.DB.WithContext(reqCtx).Transaction(func(tx *gorm.DB) error {
		var innerErr error
		debit, innerErr = h.createLedgerEntry(reqCtx, tx, &wallet.ID, nil, rate.SourceCurrency, sourceAmount, remarks)
		if innerErr != nil {
			return innerErr
		}
		targetAmount := truncate3(debit.Amount.Mul(rate.TargetAmount).Div(rate.SourceAmount))
		if targetAmount.LessThanOrEqual(decimal.Zero) {
			return errors.New("the configured exchange rate produces an amount below 0.001")
		}
		credit, innerErr = h.createLedgerEntry(reqCtx, tx, nil, &wallet.ID, rate.TargetCurrency, targetAmount, remarks)
		return innerErr
	})
	if err != nil {
		h.log().Error("failed to exchange wallet currency", "error", err)
		h.fail(c, "WALLET_EXCHANGE_FAILED", err.Error(), http.StatusBadRequest)
		return
	}
	c.JSON(http.StatusOK, walletExchangeResponse{
		WalletID:          uuidString(wallet.ID),
		SourceCurrency:    rate.SourceCurrency,
		SourceAmount:      debit.Amount,
		TargetCurrency:    rate.TargetCurrency,
		TargetAmount:      credit.Amount,
		DebitTransaction:  transactionModel(debit),
		CreditTransaction: transactionModel(credit),
	})
}

// exchangeRates reads the C# Payment:CurrencyExchange table. Stargate keeps
// it in the wallet module settings as { "1000points" = "1golds", ... }, either
// under `currencyExchange` or nested in a `payment` table.
func (h *walletHTTP) exchangeRates() ([]exchangeRate, error) {
	raw := map[string]any{}
	if h.d.Settings != nil {
		if nested, ok := h.d.Settings["payment"].(map[string]any); ok {
			if section, ok := nested["currencyExchange"].(map[string]any); ok {
				raw = section
			}
		}
		if len(raw) == 0 {
			if section, ok := h.d.Settings["currencyExchange"].(map[string]any); ok {
				raw = section
			}
		}
	}
	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	rates := make([]exchangeRate, 0, len(keys))
	for _, key := range keys {
		sourceAmount, sourceCurrency, ok := parseCurrencyAmount(key)
		if !ok {
			continue
		}
		value, isString := raw[key].(string)
		if !isString {
			return nil, errors.New("invalid currency exchange configuration for '" + key + "'")
		}
		targetAmount, targetCurrency, ok := parseCurrencyAmount(value)
		if !ok || targetAmount.LessThanOrEqual(decimal.Zero) || strings.EqualFold(sourceCurrency, targetCurrency) {
			return nil, errors.New("invalid currency exchange configuration for '" + key + "'")
		}
		rates = append(rates, exchangeRate{
			SourceCurrency: sourceCurrency,
			SourceAmount:   sourceAmount,
			TargetCurrency: targetCurrency,
			TargetAmount:   targetAmount,
		})
	}
	return rates, nil
}

// findExchangeRate picks the first rate whose source currency matches.
func findExchangeRate(rates []exchangeRate, currency string) (exchangeRate, bool) {
	requested := strings.TrimSpace(currency)
	for _, rate := range rates {
		if strings.EqualFold(rate.SourceCurrency, requested) {
			return rate, true
		}
	}
	return exchangeRate{}, false
}

// parseCurrencyAmount mirrors TryParseCurrencyAmount: a leading run of digits
// and dots is the amount, the trimmed remainder is the currency.
func parseCurrencyAmount(value string) (decimal.Decimal, string, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return decimal.Zero, "", false
	}
	separator := 0
	for separator < len(trimmed) {
		c := trimmed[separator]
		if (c >= '0' && c <= '9') || c == '.' {
			separator++
			continue
		}
		break
	}
	if separator == 0 || separator == len(trimmed) {
		return decimal.Zero, "", false
	}
	number := strings.TrimSuffix(trimmed[:separator], ".")
	amount, err := decimal.NewFromString(number)
	if err != nil || amount.LessThanOrEqual(decimal.Zero) {
		return decimal.Zero, "", false
	}
	currency := strings.TrimSpace(trimmed[separator:])
	if currency == "" {
		return decimal.Zero, "", false
	}
	return amount, currency, true
}

// ── balance ────────────────────────────────────────────────────────────────

// POST /api/wallets/balance — admin credit/debit.
func (h *walletHTTP) modifyBalance(c *gin.Context) {
	reqCtx := c.Request.Context()
	var request walletBalanceRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.invalidBody(c)
		return
	}
	if request.WalletID == nil && request.AccountID == nil {
		h.fail(c, "WALLET_MISSING_TARGET", "You must specify either WalletId or AccountId.", http.StatusBadRequest)
		return
	}
	walletID, walletOK := parseOptionalUUID(request.WalletID)
	accountID, accountOK := parseOptionalUUID(request.AccountID)
	if !walletOK || !accountOK {
		h.invalidBody(c)
		return
	}
	var wallet *WalletEntity
	if accountID != nil {
		entity, _, err := h.d.Service.GetAccountWallet(reqCtx, *accountID)
		if err != nil {
			h.fail(c, "WALLET_NOT_FOUND", "Wallet was not found.", http.StatusNotFound)
			return
		}
		wallet = entity
	} else {
		entity, _, err := h.d.Service.GetWallet(reqCtx, *walletID)
		if err != nil {
			h.fail(c, "WALLET_NOT_FOUND", "Wallet was not found.", http.StatusNotFound)
			return
		}
		wallet = entity
	}

	input := TransactionInput{
		Currency: request.Currency,
		Remarks:  request.Remark,
		Type:     TransactionSystem,
		Force:    request.ForceOperation,
	}
	if request.Amount.IsNegative() {
		amount := request.Amount.Neg()
		input.PayerWalletID = &wallet.ID
		input.Amount = amount
	} else {
		input.PayeeWalletID = &wallet.ID
		input.Amount = request.Amount
	}
	transaction, err := h.d.Service.CreateTransaction(reqCtx, input)
	if err != nil {
		h.fail(c, "WALLET_BALANCE_MODIFY_FAILED", err.Error(), http.StatusBadRequest)
		return
	}
	c.JSON(http.StatusOK, transactionModel(transaction))
}

// invalidBody answers a malformed [FromBody] payload.
func (h *walletHTTP) invalidBody(c *gin.Context) {
	c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{"body": {"Invalid request body."}}))
}

// ── transfers ──────────────────────────────────────────────────────────────

// POST /api/wallets/transfer
func (h *walletHTTP) transfer(c *gin.Context) {
	user, accountID, ok := currentWalletAccount(c)
	if !ok {
		return
	}
	reqCtx := c.Request.Context()
	var request walletTransferRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.invalidBody(c)
		return
	}
	failTransfer := func(message string) {
		h.fail(c, "WALLET_TRANSFER_FAILED", message, http.StatusBadRequest)
	}

	payerWalletID, payerOK := parseOptionalUUID(request.PayerWalletID)
	payeeWalletID, payeeWalletOK := parseOptionalUUID(request.PayeeWalletID)
	payeeAccountID, payeeAccountOK := parseOptionalUUID(request.PayeeAccountID)
	transferRequestID, transferRequestOK := parseOptionalUUID(request.TransferRequestID)
	if !payerOK || !payeeWalletOK || !payeeAccountOK || !transferRequestOK {
		h.invalidBody(c)
		return
	}

	var payerWallet *WalletEntity
	if payerWalletID != nil {
		entity, _, err := h.d.Service.GetWallet(reqCtx, *payerWalletID)
		if err != nil {
			failTransfer("Payer wallet not found.")
			return
		}
		if entity.AccountID == nil || *entity.AccountID != accountID {
			switch {
			case entity.RealmID == nil:
				c.JSON(http.StatusForbidden, errs.Forbidden("You do not have permission to perform this action."))
				return
			case user == nil || !user.IsSuperuser:
				c.JSON(http.StatusForbidden, errs.Forbidden("You must be an editor of the realm publisher to spend from this wallet."))
				return
			}
		}
		payerWallet = entity
	} else {
		entity, _, err := h.d.Service.GetAccountWallet(reqCtx, accountID)
		if err != nil {
			failTransfer("Default wallet not found.")
			return
		}
		payerWallet = entity
	}

	transferAmount := request.Amount
	transferCurrency := request.Currency
	transferRemark := request.Remark
	freezeTransfer := request.Freeze
	requireConfirmation := request.RequireConfirmation
	var transferRequest *WalletTransferRequestEntity
	var payeeWallet *WalletEntity

	switch {
	case transferRequestID != nil:
		entity, err := h.d.Service.GetTransferRequest(reqCtx, *transferRequestID)
		if err != nil {
			failTransfer("Transfer request not found.")
			return
		}
		if entity.Status != TransferActive {
			failTransfer("Transfer request is no longer active.")
			return
		}
		if entity.ExpiresAt.Before(time.Now().UTC()) {
			failTransfer("Transfer request has expired.")
			return
		}
		wallet, _, err := h.d.Service.GetWallet(reqCtx, entity.PayeeWalletID)
		if err != nil {
			failTransfer("Payee wallet not found.")
			return
		}
		transferRequest = entity
		payeeWallet = wallet
		transferAmount = entity.Amount
		transferCurrency = entity.Currency
		transferRemark = entity.Remark
		freezeTransfer = entity.Freeze
		requireConfirmation = entity.RequireConfirmation
	case payeeWalletID != nil:
		wallet, _, err := h.d.Service.GetWallet(reqCtx, *payeeWalletID)
		if err != nil {
			failTransfer("Payee wallet not found.")
			return
		}
		payeeWallet = wallet
	case strings.TrimSpace(deref(request.PayeePublicID)) != "":
		wallet, _, err := h.d.Service.GetWalletByPublicID(reqCtx, strings.TrimSpace(*request.PayeePublicID))
		if err != nil {
			failTransfer("Payee wallet not found.")
			return
		}
		payeeWallet = wallet
	case payeeAccountID != nil:
		if *payeeAccountID == accountID {
			h.fail(c, "WALLET_TRANSFER_SELF_FORBIDDEN", "Cannot transfer to yourself.", http.StatusBadRequest)
			return
		}
		wallet, _, err := h.d.Service.GetAccountWallet(reqCtx, *payeeAccountID)
		if err != nil {
			failTransfer("Payee wallet not found.")
			return
		}
		payeeWallet = wallet
	default:
		h.fail(c, "WALLET_TRANSFER_MISSING_PAYEE", "You must specify payee_account_id, payee_wallet_id, or payee_public_id.", http.StatusBadRequest)
		return
	}

	if payerWallet.ID == payeeWallet.ID {
		h.fail(c, "WALLET_TRANSFER_SAME_WALLET", "Cannot transfer to the same wallet.", http.StatusBadRequest)
		return
	}
	transaction, err := h.d.Service.TransferBetweenWallets(reqCtx, payerWallet.ID, payeeWallet.ID,
		transferCurrency, transferAmount, transferRemark, freezeTransfer, requireConfirmation)
	if err != nil {
		failTransfer(err.Error())
		return
	}
	if transferRequest != nil {
		if _, err := h.d.Service.FulfillTransferRequest(reqCtx, transferRequest.ID, transaction.ID); err != nil {
			failTransfer(err.Error())
			return
		}
	}
	c.JSON(http.StatusOK, transactionModel(transaction))
}

// POST /api/wallets/transfer/requests
func (h *walletHTTP) createTransferRequest(c *gin.Context) {
	user, accountID, ok := currentWalletAccount(c)
	if !ok {
		return
	}
	reqCtx := c.Request.Context()
	var request createWalletTransferRequestRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.invalidBody(c)
		return
	}
	expirationHours := request.ExpirationHours
	if expirationHours == 0 {
		expirationHours = 24
	}
	if expirationHours <= 0 || expirationHours > 24*7 {
		h.fail(c, "WALLET_TRANSFER_REQUEST_INVALID_EXPIRATION", "ExpirationHours must be between 1 and 168.", http.StatusBadRequest)
		return
	}
	walletID, walletOK := parseOptionalUUID(request.WalletID)
	if !walletOK {
		h.invalidBody(c)
		return
	}
	wallet, err := h.accessibleWallet(reqCtx, user, accountID, walletID)
	if err != nil {
		h.fail(c, "WALLET_NOT_FOUND", "Wallet not found.", http.StatusNotFound)
		return
	}
	if wallet.AccountID == nil || *wallet.AccountID != accountID {
		if wallet.RealmID == nil {
			h.fail(c, "WALLET_NOT_FOUND", "Wallet not found.", http.StatusNotFound)
			return
		}
		if user == nil || !user.IsSuperuser {
			c.JSON(http.StatusForbidden, errs.Forbidden("You must be an editor of the realm publisher to create transfer requests for this wallet."))
			return
		}
	}
	if wallet.PublicID == nil || strings.TrimSpace(*wallet.PublicID) == "" {
		h.fail(c, "WALLET_PUBLIC_ID_NOT_ENABLED", "Wallet public ID is not enabled.", http.StatusBadRequest)
		return
	}
	expiration := time.Duration(expirationHours) * time.Hour
	entity, err := h.d.Service.CreateTransferRequest(reqCtx, accountID, wallet.ID, request.Currency,
		request.Amount, request.Remark, &expiration, request.Freeze, request.RequireConfirmation)
	if err != nil {
		h.fail(c, "WALLET_TRANSFER_REQUEST_CREATE_FAILED", err.Error(), http.StatusBadRequest)
		return
	}
	c.JSON(http.StatusOK, mapTransferRequest(entity, wallet.PublicID))
}

// GET /api/wallets/transfer/requests/:id (anonymous)
func (h *walletHTTP) getTransferRequest(c *gin.Context) {
	reqCtx := c.Request.Context()
	id, ok := pathID(c)
	if !ok {
		h.fail(c, "WALLET_TRANSFER_REQUEST_NOT_FOUND", "Transfer request was not found.", http.StatusNotFound)
		return
	}
	entity, err := h.d.Service.GetTransferRequest(reqCtx, id)
	if err != nil {
		h.fail(c, "WALLET_TRANSFER_REQUEST_NOT_FOUND", "Transfer request was not found.", http.StatusNotFound)
		return
	}
	if entity.Status != TransferActive {
		h.fail(c, "WALLET_TRANSFER_REQUEST_NOT_FOUND", "Transfer request is no longer active.", http.StatusNotFound)
		return
	}
	if entity.ExpiresAt.Before(time.Now().UTC()) {
		h.fail(c, "WALLET_TRANSFER_REQUEST_NOT_FOUND", "Transfer request has expired.", http.StatusNotFound)
		return
	}
	var publicID *string
	if wallet, _, err := h.d.Service.GetWallet(reqCtx, entity.PayeeWalletID); err == nil {
		publicID = wallet.PublicID
	}
	c.JSON(http.StatusOK, mapTransferRequest(entity, publicID))
}

// mapTransferRequest mirrors WalletController.MapTransferRequest.
func mapTransferRequest(entity *WalletTransferRequestEntity, payeePublicID *string) walletTransferRequestResponse {
	return walletTransferRequestResponse{
		ID:                  uuidString(entity.ID),
		Status:              entity.Status,
		Currency:            entity.Currency,
		Amount:              entity.Amount,
		Remark:              entity.Remark,
		Freeze:              entity.Freeze,
		RequireConfirmation: entity.RequireConfirmation,
		ExpiresAt:           timeValue(entity.ExpiresAt),
		FulfilledAt:         timePointer(entity.FulfilledAt),
		PayeeWalletID:       uuidString(entity.PayeeWalletID),
		PayeePublicID:       payeePublicID,
		TransactionID:       uuidPointer(entity.TransactionID),
	}
}

// ── funds ──────────────────────────────────────────────────────────────────

// POST /api/wallets/funds
func (h *walletHTTP) createFund(c *gin.Context) {
	_, accountID, ok := currentWalletAccount(c)
	if !ok {
		return
	}
	reqCtx := c.Request.Context()
	var request createFundRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.invalidBody(c)
		return
	}
	recipients := make([]uuid.UUID, 0, len(request.RecipientAccountIDs))
	for _, raw := range request.RecipientAccountIDs {
		id, err := uuid.Parse(strings.TrimSpace(raw))
		if err != nil {
			h.fail(c, "WALLET_FUND_CREATE_FAILED", "Recipient account id is invalid.", http.StatusBadRequest)
			return
		}
		recipients = append(recipients, id)
	}
	var expiration *time.Duration
	if request.ExpirationHours != nil {
		value := time.Duration(*request.ExpirationHours) * time.Hour
		expiration = &value
	}

	var fund *WalletFundEntity
	var err error
	if request.IsRaising {
		isOpen := true
		if request.IsOpen != nil {
			isOpen = *request.IsOpen
		}
		var deadline *time.Time
		if request.DeadlineAt != nil {
			value := request.DeadlineAt.Time()
			deadline = &value
		}
		fund, err = h.d.Service.CreateRaisingFund(reqCtx, RaisingFundInput{
			CreatorAccountID:   accountID,
			Currency:           request.Currency,
			TargetAmount:       request.TargetAmount,
			MaxParticipants:    request.AmountOfSplits,
			ContributionType:   request.ContributionType,
			ContributionAmount: request.ContributionAmount,
			IsOpen:             isOpen,
			InvitedAccountIDs:  recipients,
			Message:            request.Message,
			Expiration:         expiration,
			DeadlineAt:         deadline,
		})
	} else {
		fund, err = h.d.Service.CreateFund(reqCtx, FundInput{
			CreatorAccountID:    accountID,
			RecipientAccountIDs: recipients,
			Currency:            request.Currency,
			TotalAmount:         request.TotalAmount,
			AmountOfSplits:      request.AmountOfSplits,
			SplitType:           request.SplitType,
			Message:             request.Message,
			Expiration:          expiration,
		})
	}
	if err != nil {
		h.fail(c, "WALLET_FUND_CREATE_FAILED", err.Error(), http.StatusBadRequest)
		return
	}
	_, rows, err := h.d.Service.GetFund(reqCtx, fund.ID)
	if err != nil {
		h.fail(c, "WALLET_FUND_CREATE_FAILED", err.Error(), http.StatusBadRequest)
		return
	}
	c.JSON(http.StatusOK, fundModel(fund, rows))
}

// GET /api/wallets/funds
func (h *walletHTTP) listFunds(c *gin.Context) {
	_, accountID, ok := currentWalletAccount(c)
	if !ok {
		return
	}
	reqCtx := c.Request.Context()
	query := h.d.Service.DB.WithContext(reqCtx).Model(&WalletFundEntity{}).
		Where("creator_account_id = ?", accountID)
	if raw := strings.TrimSpace(c.Query("status")); raw != "" {
		status, err := strconv.Atoi(raw)
		if err != nil {
			h.invalidBody(c)
			return
		}
		query = query.Where("status = ?", status)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		h.fail(c, "INTERNAL_ERROR", "Failed to load funds.", http.StatusInternalServerError)
		return
	}
	setTotal(c, total)
	offset := max(queryInt(c, "offset", 0), 0)
	take := max(queryInt(c, "take", 20), 0)
	var funds []WalletFundEntity
	if err := query.Order("created_at DESC").Offset(offset).Limit(take).
		Find(&funds).Error; err != nil {
		h.fail(c, "INTERNAL_ERROR", "Failed to load funds.", http.StatusInternalServerError)
		return
	}
	recipients, err := h.fundRecipients(reqCtx, funds)
	if err != nil {
		h.fail(c, "INTERNAL_ERROR", "Failed to load funds.", http.StatusInternalServerError)
		return
	}
	response := make([]Fund, 0, len(funds))
	for i := range funds {
		response = append(response, fundModel(&funds[i], recipients[funds[i].ID]))
	}
	c.JSON(http.StatusOK, response)
}

// fundRecipients loads the recipient rows of a page of funds in one query.
func (h *walletHTTP) fundRecipients(ctx context.Context, funds []WalletFundEntity) (map[uuid.UUID][]WalletFundRecipientEntity, error) {
	out := make(map[uuid.UUID][]WalletFundRecipientEntity, len(funds))
	if len(funds) == 0 {
		return out, nil
	}
	ids := make([]uuid.UUID, 0, len(funds))
	for _, fund := range funds {
		ids = append(ids, fund.ID)
	}
	var rows []WalletFundRecipientEntity
	if err := h.d.Service.DB.WithContext(ctx).Where("fund_id IN ?", ids).
		Order("created_at").Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.FundID] = append(out[row.FundID], row)
	}
	return out, nil
}

// GET /api/wallets/funds/:id (anonymous)
func (h *walletHTTP) getFund(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		h.fail(c, "WALLET_FUND_NOT_FOUND", "Fund not found.", http.StatusNotFound)
		return
	}
	fund, recipients, err := h.d.Service.GetFund(c.Request.Context(), id)
	if err != nil {
		h.fail(c, "WALLET_FUND_NOT_FOUND", "Fund not found.", http.StatusNotFound)
		return
	}
	c.JSON(http.StatusOK, fundModel(fund, recipients))
}

// POST /api/wallets/funds/:id/receive
func (h *walletHTTP) receiveFund(c *gin.Context) {
	_, accountID, ok := currentWalletAccount(c)
	if !ok {
		return
	}
	id, ok := pathID(c)
	if !ok {
		h.fail(c, "WALLET_FUND_RECEIVE_FAILED", "Fund not found.", http.StatusBadRequest)
		return
	}
	transaction, err := h.d.Service.ReceiveFund(c.Request.Context(), accountID, id)
	if err != nil {
		h.log().Error("failed to receive fund", "fund_id", id, "error", err)
		h.fail(c, "WALLET_FUND_RECEIVE_FAILED", err.Error(), http.StatusBadRequest)
		return
	}
	c.JSON(http.StatusOK, transactionModel(transaction))
}

// POST /api/wallets/funds/:id/contribute
func (h *walletHTTP) contributeFund(c *gin.Context) {
	_, accountID, ok := currentWalletAccount(c)
	if !ok {
		return
	}
	reqCtx := c.Request.Context()
	id, ok := pathID(c)
	if !ok {
		h.fail(c, "WALLET_FUND_CONTRIBUTE_FAILED", "Fund not found.", http.StatusBadRequest)
		return
	}
	var request contributeFundRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.invalidBody(c)
		return
	}
	fund, err := h.d.Service.ContributeFund(reqCtx, accountID, id, request.Amount)
	if err != nil {
		h.log().Error("failed to contribute to fund", "fund_id", id, "error", err)
		h.fail(c, "WALLET_FUND_CONTRIBUTE_FAILED", err.Error(), http.StatusBadRequest)
		return
	}
	amount := truncate3(request.Amount)
	if fund.ContributionType == ContributionFixed {
		amount = fund.ContributionAmount
	}
	remark := "Contribution to fund " + uuidString(fund.ID)
	entry, err := h.createLedgerEntry(reqCtx, h.d.Service.DB.WithContext(reqCtx), nil, nil, fund.Currency, amount, &remark)
	if err != nil {
		h.log().Error("failed to record the fund contribution", "fund_id", id, "error", err)
		h.fail(c, "WALLET_FUND_CONTRIBUTE_FAILED", err.Error(), http.StatusBadRequest)
		return
	}
	c.JSON(http.StatusOK, transactionModel(entry))
}

// GET /api/wallets/funds/:id/contributors (anonymous)
func (h *walletHTTP) getFundContributors(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		h.fail(c, "WALLET_FUND_NOT_FOUND", "Fund not found.", http.StatusNotFound)
		return
	}
	fund, recipients, err := h.d.Service.GetFund(c.Request.Context(), id)
	if err != nil {
		h.fail(c, "WALLET_FUND_NOT_FOUND", "Fund not found.", http.StatusNotFound)
		return
	}
	if !fund.IsRaising {
		h.fail(c, "WALLET_FUND_NOT_RAISING", "This fund is not in raising mode.", http.StatusBadRequest)
		return
	}
	response := make([]FundRecipient, 0, len(recipients))
	for i := range recipients {
		if recipients[i].IsReceived {
			response = append(response, fundRecipientModel(&recipients[i]))
		}
	}
	c.JSON(http.StatusOK, response)
}

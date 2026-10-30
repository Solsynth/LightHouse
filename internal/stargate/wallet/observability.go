package wallet

import (
	"context"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/stargate/localization"
	"src.solsynth.dev/sosys/stargate/middleware"
	"src.solsynth.dev/sosys/stargate/model"
)

// ── localization ───────────────────────────────────────────────────────────

// Locale resolves the preferred locale for an account. It mirrors the C#
// wallet, which passed the recipient's DyAccount.Language to the localizer:
// the account row is the source of truth, and an unknown account falls back to
// the English default.
func (s *Service) Locale(ctx context.Context, accountID string) string {
	if s == nil || accountID == "" {
		return ""
	}
	store := walletAccountStore(&Deps{Service: s})
	if store == nil {
		return ""
	}
	id, err := uuid.Parse(accountID)
	if err != nil {
		return ""
	}
	account, err := store.GetAccountByID(ctx, id)
	if err != nil || account == nil {
		return ""
	}
	return account.Language
}

// Localize renders one wallet notification string in the account's language.
func (s *Service) Localize(ctx context.Context, accountID, key string, args map[string]string) string {
	return localization.Localize(s.Locale(ctx, accountID), key, args)
}

// CurrencyName mirrors PaymentService.GetTranslationForCurrency: the two wallet
// currencies carry localized names, everything else is "unknown".
func (s *Service) CurrencyName(ctx context.Context, accountID, currency string) string {
	switch currency {
	case CurrencyPoints:
		return s.Localize(ctx, accountID, "currencyPoints", nil)
	case CurrencyGolds:
		return s.Localize(ctx, accountID, "currencyGolds", nil)
	default:
		return "unknown"
	}
}

// ── action logs ────────────────────────────────────────────────────────────

// LogAction records one action log the way the C# controllers did: fire and
// forget, with the caller's user agent and IP. Failures are logged and
// swallowed — an audit failure must never fail the request that produced it.
func (s *Service) LogAction(ctx context.Context, accountID string, action model.ActionLogType, meta map[string]any, userAgent, ipAddress string) {
	if s == nil || s.Logs == nil || accountID == "" {
		return
	}
	if err := s.Logs.Create(ctx, accountID, action, meta, userAgent, ipAddress, nil, nil); err != nil && s.Log != nil {
		s.Log.Warn("wallet action log failed", "action", action, "account", accountID, "error", err)
	}
}

// LogRequestAction is LogAction bound to an HTTP request: it attaches the
// request's user agent and resolved client IP.
func (s *Service) LogRequestAction(c *gin.Context, accountID string, action model.ActionLogType, meta map[string]any) {
	if s == nil || s.Logs == nil || accountID == "" || c == nil || c.Request == nil {
		return
	}
	s.LogAction(c.Request.Context(), accountID, action, meta, c.Request.UserAgent(), middleware.ClientIP(c.Request))
}

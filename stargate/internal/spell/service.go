// Package spell ports Passport's MagicSpellService (and the registration
// paths of AffiliationSpellService) into Stargate: spell creation, email
// notification via Ring, spell application (contact verification), and
// registration-invite affiliation consumption.
package spell

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/stargate/internal/config"
	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/redis"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

const spellWordCharset = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// Service owns the magic-spell lifecycle.
type Service struct {
	store   *store.Store
	redis   *redis.Client
	ring    gen.DyRingServiceClient
	siteURL string
	log     *slog.Logger
	cfg     *config.Config
}

// NewService wires the spell service. ring may be nil (email degrades).
func NewService(st *store.Store, rc *redis.Client, ring gen.DyRingServiceClient, siteURL string, cfg *config.Config, log *slog.Logger) *Service {
	return &Service{store: st, redis: rc, ring: ring, siteURL: siteURL, log: log, cfg: cfg}
}

// CreateOptions mirrors the optional parameters of
// MagicSpellService.CreateMagicSpell.
type CreateOptions struct {
	ExpiresAt     *time.Time
	AffectedAt    *time.Time
	Code          string
	PreventRepeat bool
}

// CreateMagicSpell mirrors MagicSpellService.CreateMagicSpell: with
// preventRepeat, an existing live spell of the same type for the account is
// returned instead of creating a new one.
func (s *Service) CreateMagicSpell(ctx context.Context, accountID string, typ model.MagicSpellType, meta map[string]any, opts CreateOptions) (*model.MagicSpell, error) {
	if opts.PreventRepeat {
		if existing, err := s.store.FindLiveMagicSpell(ctx, accountID, typ); err == nil && existing != nil {
			return existing, nil
		}
	}
	word := opts.Code
	if word == "" {
		word = generateSpellWord(128)
	}
	now := time.Now().UTC()
	spell := &model.MagicSpell{
		Id:        uuid.NewString(),
		Spell:     word,
		Type:      typ,
		Meta:      meta,
		AccountId: accountID,
		CreatedAt: model.NewTime(now),
		UpdatedAt: model.NewTime(now),
	}
	if opts.ExpiresAt != nil {
		spell.ExpiresAt = model.NewTime(*opts.ExpiresAt)
	}
	if opts.AffectedAt != nil {
		spell.AffectedAt = model.NewTime(*opts.AffectedAt)
	}
	return spell, s.store.CreateMagicSpell(ctx, spell)
}

const spellNotifyCacheKeyPrefix = "spells:notify:"

// NotifyMagicSpell mirrors MagicSpellService.NotifyMagicSpell: resolves the
// recipient, renders the templated email and pushes it through Ring. Sends
// are deduped for 5 minutes via the shared cache.
func (s *Service) NotifyMagicSpell(ctx context.Context, spell *model.MagicSpell, bypassVerify bool) error {
	cacheKey := spellNotifyCacheKeyPrefix + spell.Id
	if s.redis != nil && s.redis.Cache != nil {
		if found, err := s.redis.Cache.Get(ctx, cacheKey, new(bool)); err == nil && found {
			s.log.Info("skip sending magic spell; already sent", "spell_id", spell.Id)
			return nil
		}
	}
	if spell.AccountId == "" {
		return errors.New("spell is missing account id")
	}

	var recipient string
	if spell.Type == model.MagicSpellTypeContactVerification {
		recipient = getMetaString(spell, "contact_method")
		if strings.TrimSpace(recipient) == "" {
			return errors.New("contact method is not found")
		}
	} else {
		contact, err := s.store.GetEmailContactForNotify(ctx, spell.AccountId, !bypassVerify)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return errors.New("account has no contact method that can use")
			}
			return err
		}
		recipient = contact.Content
	}

	accountID, err := uuid.Parse(spell.AccountId)
	if err != nil {
		return errors.New("spell contains an invalid account id")
	}
	account, err := s.store.GetAccountByID(ctx, accountID)
	if err != nil {
		return err
	}

	switch spell.Type {
	case model.MagicSpellTypeAccountActivation, model.MagicSpellTypeContactVerification:
		err = s.sendTemplatedEmail(ctx, account, recipient, "ContactVerification", "contractMethodVerificationTitle", map[string]string{
			"link": s.spellLink(spell),
		})
	case model.MagicSpellTypeAuthPasswordReset:
		err = s.sendTemplatedEmail(ctx, account, recipient, "PasswordReset", "passwordResetTitle", map[string]string{
			"link": s.spellLink(spell),
		})
	case model.MagicSpellTypeAccountRemoval:
		err = s.sendTemplatedEmail(ctx, account, recipient, "AccountDeletion", "emailAccountDeletionTitle", map[string]string{
			"link": s.spellLink(spell),
		})
	default:
		err = errors.New("unsupported magic spell type for notification")
	}
	if err != nil {
		// Mirror MagicSpellService.NotifyMagicSpell: delivery failures are
		// logged and swallowed (the caller's outcome never depends on email).
		s.log.Warn("send magic spell email failed", "spell_id", spell.Id, "error", err)
		return nil
	}
	if s.redis != nil && s.redis.Cache != nil {
		_ = s.redis.Cache.Set(ctx, cacheKey, true, 5*time.Minute)
	}
	return nil
}

// ResendMagicSpell clears the notify dedupe and re-sends (mirrors
// MagicSpellService.ResendMagicSpell, bypassVerify defaults to true).
func (s *Service) ResendMagicSpell(ctx context.Context, spell *model.MagicSpell, bypassVerify bool) error {
	if s.redis != nil && s.redis.Cache != nil {
		_ = s.redis.Cache.Remove(ctx, spellNotifyCacheKeyPrefix+spell.Id)
	}
	return s.NotifyMagicSpell(ctx, spell, bypassVerify)
}

// SendWelcomeEmail sends the account-created welcome email (the Passport
// AccountCreatedEvent consumer's second email).
func (s *Service) SendWelcomeEmail(ctx context.Context, account *model.Account, recipient string) error {
	recipientName := account.Nick
	if strings.TrimSpace(recipientName) == "" {
		recipientName = account.Name
	}
	return s.sendTemplatedEmail(ctx, account, recipient, "Welcome", "welcomeEmailTitle", map[string]string{
		"nick":           recipientName,
		"site_url":       s.siteURL,
		"activation_url": strings.TrimRight(s.siteURL, "/") + "/accounts/me/activation",
	})
}

// SendFactorCodeEmail renders the FactorCode email (with the one-time code)
// for the account's language and pushes it through Ring, mirroring
// EmailService.SendTemplatedEmailAsync("FactorCode", { nick, code }).
func (s *Service) SendFactorCodeEmail(ctx context.Context, account *model.Account, recipient, code string) error {
	recipientName := strings.TrimSpace(account.Nick)
	if recipientName == "" {
		recipientName = account.Name
	}
	return s.sendTemplatedEmail(ctx, account, recipient, "FactorCode", "codeEmailTitle", map[string]string{
		"nick": recipientName,
		"code": code,
	})
}

// ApplyMagicSpell mirrors MagicSpellService.ApplyMagicSpell for the types
// Stargate produces (contact verification and the legacy account-activation
// alias). Applied spells are consumed (deleted).
func (s *Service) ApplyMagicSpell(ctx context.Context, spell *model.MagicSpell) error {
	switch spell.Type {
	case model.MagicSpellTypeContactVerification, model.MagicSpellTypeAccountActivation:
		if spell.AccountId == "" {
			return errors.New("contact verification spell is missing account id")
		}
		contactID := getMetaString(spell, "contact_id")
		if contactID == "" {
			return errors.New("contact verification spell is missing contact id")
		}
		if _, err := uuid.Parse(contactID); err != nil {
			return errors.New("contact verification spell contains an invalid contact id")
		}
		if _, err := s.store.MarkContactVerified(ctx, spell.AccountId, contactID, time.Now().UTC()); err != nil {
			return err
		}
		// Mirror Passport's TestService.TryActivateAfterContactVerification
		// in the tests-disabled branch: entry tests (exam logic) stay in
		// Passport and none are required here, so a verified contact
		// activates the account immediately.
		if err := s.ActivateAccountAfterVerifiedContact(ctx, spell.AccountId); err != nil {
			return err
		}
		return s.store.DeleteMagicSpell(ctx, spell.Id)
	case model.MagicSpellTypeAuthPasswordReset:
		return errors.New("for password reset spell, please use the ApplyPasswordReset method instead")
	case model.MagicSpellTypeAccountRemoval:
		// Account removal is gated in Stargate by the Redis 24h flag
		// (profilectl); no removal spells are produced here.
		return errors.New("account removal spells are not supported by Stargate")
	default:
		return errors.New("unsupported magic spell type")
	}
}

// ActivateAccountAfterVerifiedContact mirrors Passport's
// TestService.TryActivateAfterContactVerification. It is the shared
// activation entry point for every flow whose email contact is verified:
// magic-spell apply and OIDC/social registration with a provider-verified
// email. Entry-test (exam) logic stays in Passport: when [accountActivation]
// requires tests, activation is deferred to Passport — it evaluates attempts
// and publishes accounts.activated, which Stargate consumes. Only the
// tests-disabled branch activates directly here. Idempotent: an already
// activated account is left untouched.
func (s *Service) ActivateAccountAfterVerifiedContact(ctx context.Context, accountID string) error {
	if s.cfg != nil && s.cfg.AccountActivation.TestsEnabled && len(s.cfg.AccountActivation.RequiredTestKeys) > 0 {
		return nil
	}
	id, err := uuid.Parse(accountID)
	if err != nil {
		return errors.New("spell contains an invalid account id")
	}
	activated, err := s.store.ActivateAccountAndGrantVerified(ctx, id, time.Now().UTC())
	if err != nil {
		return err
	}
	if !activated {
		return nil
	}
	s.clearActorPermissionCache(ctx, accountID)
	return nil
}

// clearActorPermissionCache removes the C#-side permission cache entries for
// an actor (the Go permission service is DB-backed, but the C# fleet still
// reads the perm:* keys), best-effort.
func (s *Service) clearActorPermissionCache(ctx context.Context, actor string) {
	s.redis.ClearActorPermissionCache(ctx, actor)
}

// ApplyPasswordReset mirrors MagicSpellService.ApplyPasswordReset: replaces
// the account's password factor and consumes the spell.
func (s *Service) ApplyPasswordReset(ctx context.Context, spell *model.MagicSpell, newPassword string) error {
	if spell.Type != model.MagicSpellTypeAuthPasswordReset {
		return errors.New("this spell is not a password reset spell")
	}
	if spell.AccountId == "" {
		return errors.New("password reset spell is missing account id")
	}
	if strings.TrimSpace(newPassword) == "" {
		return errors.New("new password cannot be empty")
	}
	hash, err := hashPassword(newPassword)
	if err != nil {
		return err
	}
	if err := s.store.ResetPasswordFactor(ctx, spell.AccountId, hash); err != nil {
		return err
	}
	return s.store.DeleteMagicSpell(ctx, spell.Id)
}

// ConsumeRegistrationInvite mirrors AffiliationSpellService.
// ConsumeRegistrationInvite: records the new account as a use of the
// registration-invite spell and reports whether the invite skips entry tests.
func (s *Service) ConsumeRegistrationInvite(ctx context.Context, spellWord, accountID string) (consumed, skipsTests bool, err error) {
	spell, err := s.store.GetAffiliationSpellByWord(ctx, spellWord, model.AffiliationSpellTypeRegistrationInvite)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return false, false, nil
		}
		return false, false, err
	}
	if spell.ExpiresAt != nil && !spell.ExpiresAt.Time().After(time.Now().UTC()) {
		return false, false, nil
	}
	usages, err := s.store.CountAffiliationResults(ctx, spell.Id)
	if err != nil {
		return false, false, err
	}
	maxUsages := getIntMeta(spell, "max_usages")
	if maxUsages != nil && usages >= *maxUsages {
		return false, false, nil
	}
	if maxUsages != nil && *maxUsages == 1 {
		if err := s.store.SetAffiliationSpellAffected(ctx, spell.Id, time.Now().UTC()); err != nil {
			return false, false, err
		}
	}
	if err := s.store.CreateAffiliationResult(ctx, spell.Id, "account:"+accountID); err != nil {
		return false, false, err
	}
	return true, getBoolMeta(spell, "skip_tests"), nil
}

func (s *Service) spellLink(spell *model.MagicSpell) string {
	return strings.TrimRight(s.siteURL, "/") + "/spells/" + escapePathSegment(spell.Spell)
}

func getMetaString(spell *model.MagicSpell, key string) string {
	if spell.Meta == nil {
		return ""
	}
	v, ok := spell.Meta[key]
	if !ok || v == nil {
		return ""
	}
	if text, ok := v.(string); ok {
		return text
	}
	return ""
}

func getIntMeta(spell *model.AffiliationSpell, key string) *int {
	if spell.Meta == nil {
		return nil
	}
	v, ok := spell.Meta[key]
	if !ok || v == nil {
		return nil
	}
	switch n := v.(type) {
	case float64:
		i := int(n)
		return &i
	case int:
		return &n
	}
	return nil
}

func getBoolMeta(spell *model.AffiliationSpell, key string) bool {
	if spell.Meta == nil {
		return false
	}
	v, ok := spell.Meta[key]
	if !ok || v == nil {
		return false
	}
	b, ok := v.(bool)
	return ok && b
}

// generateSpellWord mirrors the C# _GenerateRandomString (modulo-biased but
// byte-identical behavior is not required for a 128-char secret).
func generateSpellWord(length int) string {
	buf := make([]byte, length)
	random := make([]byte, length)
	_, _ = rand.Read(random)
	for i := 0; i < length; i++ {
		buf[i] = spellWordCharset[int(random[i])%len(spellWordCharset)]
	}
	return string(buf)
}

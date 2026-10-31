package purchases

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"src.solsynth.dev/sosys/stargate/ports"
)

// Sentinel errors mapped to the C# API error codes by the HTTP layer. The
// messages mirror the exceptions raised by the C# services.
var (
	ErrAffiliationPurchasesDisabled = errors.New("Affiliation invite purchases are disabled.")
	ErrNameChangePurchasesDisabled  = errors.New("Name change card purchases are disabled.")
	ErrPurchaseLimit                = errors.New("The purchase limit has been reached for this period.")

	ErrSpellNotFound    = errors.New("The spell was not found.")
	ErrSpellTaken       = errors.New("The spell has been taken.")
	ErrInviteRestricted = errors.New("Registration invitations can only be used during account creation.")

	ErrNoNameChangeCard  = errors.New("No available name change card. Purchase one first.")
	ErrBadAccountName    = errors.New("Name can only contain letters, numbers, underscores, and hyphens (2-256 characters).")
	ErrTargetIDRequired  = errors.New("target_id is required.")
	ErrSlugEmpty         = errors.New("Slug cannot be empty.")
	ErrUnsupportedTarget = errors.New("Unsupported target type.")
	ErrPublisherMissing  = errors.New("Publisher renaming is not configured in this deployment.")
)

// PublisherRenamer renames a Sphere publisher. The current host core has no
// publisher gRPC client (grpcclient.Clients exposes wallet, payment, develop,
// drive, pass, nfc, blade and ring only), so a deployment leaves this nil and
// the publisher target reports a clear failure instead of silently no-oping.
type PublisherRenamer interface {
	RenamePublisher(ctx context.Context, publisherID, accountID uuid.UUID, newName string) (oldName string, err error)
}

// Service owns the affiliation and name-change-card lifecycle.
type Service struct {
	store     *Store
	settings  Settings
	payments  ports.Payments
	accounts  AccountStore
	publisher PublisherRenamer
	log       *slog.Logger

	now func() time.Time
}

// NewService wires the domain. payments may be nil; purchases then fail with
// ports.ErrUnavailable, which the HTTP layer serves as 503.
func NewService(store *Store, settings Settings, payments ports.Payments, accounts AccountStore, publisher PublisherRenamer, log *slog.Logger) *Service {
	return &Service{
		store:     store,
		settings:  settings,
		payments:  payments,
		accounts:  accounts,
		publisher: publisher,
		log:       log,
		now:       func() time.Time { return time.Now().UTC() },
	}
}

// ── affiliation spells ────────────────────────────────────────────────────

// CreateAffiliationSpell mirrors AffiliationSpellService.CreateAffiliationSpell.
func (s *Service) CreateAffiliationSpell(ctx context.Context, accountID uuid.UUID, word string, maxUsages *int, skipTests bool) (*AffiliationSpell, error) {
	if maxUsages != nil && *maxUsages < 1 {
		return nil, fmt.Errorf("max_usages must be at least 1")
	}
	if word == "" {
		for attempt := 0; attempt < 5; attempt++ {
			candidate, err := randomSpellWord(8)
			if err != nil {
				return nil, err
			}
			taken, err := s.store.AffiliationSpellTaken(ctx, candidate)
			if err != nil {
				return nil, err
			}
			if !taken {
				word = candidate
				break
			}
		}
		if word == "" {
			return nil, fmt.Errorf("could not allocate a unique spell word")
		}
	}
	taken, err := s.store.AffiliationSpellTaken(ctx, word)
	if err != nil {
		return nil, err
	}
	if taken {
		return nil, ErrSpellTaken
	}
	meta := map[string]any{"skip_tests": skipTests}
	if maxUsages == nil {
		meta["max_usages"] = nil
	} else {
		meta["max_usages"] = *maxUsages
	}
	entity, err := s.store.CreateAffiliationSpell(ctx, accountID, word, AffiliationSpellTypeRegistrationInvite, meta, nil)
	if err != nil {
		return nil, err
	}
	return entity.toWire(), nil
}

// ConsumeRegistrationInvite mirrors AffiliationSpellService.ConsumeRegistrationInvite.
// It reports whether the invite was consumed and whether it skips entry tests.
func (s *Service) ConsumeRegistrationInvite(ctx context.Context, word, accountID string) (bool, bool, error) {
	word = strings.TrimSpace(word)
	if word == "" {
		return false, false, nil
	}
	typ := AffiliationSpellTypeRegistrationInvite
	spell, err := s.store.GetAffiliationSpellByWord(ctx, word, &typ)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return false, false, nil
		}
		return false, false, err
	}
	now := s.now()
	if spell.ExpiresAt != nil && !spell.ExpiresAt.After(now) {
		return false, false, nil
	}
	usages, err := s.store.CountAffiliationResults(ctx, spell.ID)
	if err != nil {
		return false, false, err
	}
	maxUsages, maxSet := intMeta(decodeMeta(spell.Meta), "max_usages")
	if maxSet && usages >= maxUsages {
		return false, false, nil
	}
	if maxSet && maxUsages == 1 {
		if err := s.store.SetAffiliationSpellAffected(ctx, spell.ID, now); err != nil {
			return false, false, err
		}
	}
	if _, err := s.store.CreateAffiliationResult(ctx, spell.ID, "account:"+accountID); err != nil {
		return false, false, err
	}
	skipTests, _ := boolMeta(decodeMeta(spell.Meta), "skip_tests")
	return true, skipTests, nil
}

// RecordAffiliationResult mirrors RecordAffiliationEvent. Registration invites
// are rejected here because the C# controller allows them only at signup.
func (s *Service) RecordAffiliationResult(ctx context.Context, word, resource string) (*AffiliationResult, error) {
	spell, err := s.store.GetAffiliationSpellByWord(ctx, word, nil)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrSpellNotFound
		}
		return nil, err
	}
	if spell.Type == AffiliationSpellTypeRegistrationInvite {
		return nil, ErrInviteRestricted
	}
	result, err := s.store.CreateAffiliationResult(ctx, spell.ID, resource)
	if err != nil {
		return nil, err
	}
	return result.toWire(), nil
}

// ListAffiliationSpells lists one account's spells.
func (s *Service) ListAffiliationSpells(ctx context.Context, accountID uuid.UUID, orderBy string, desc bool, take, offset int) ([]*AffiliationSpell, int64, error) {
	entities, total, err := s.store.ListAffiliationSpells(ctx, accountID, orderBy, desc, take, offset)
	if err != nil {
		return nil, 0, err
	}
	out := make([]*AffiliationSpell, 0, len(entities))
	for i := range entities {
		out = append(out, entities[i].toWire())
	}
	return out, total, nil
}

// GetAffiliationSpell resolves one owned spell.
func (s *Service) GetAffiliationSpell(ctx context.Context, accountID, spellID uuid.UUID) (*AffiliationSpell, error) {
	entity, err := s.store.GetAffiliationSpellForAccount(ctx, accountID, spellID)
	if err != nil {
		return nil, err
	}
	return entity.toWire(), nil
}

// ListAffiliationResults lists one owned spell's results.
func (s *Service) ListAffiliationResults(ctx context.Context, accountID, spellID uuid.UUID, desc bool, take, offset int) ([]*AffiliationResult, int64, error) {
	return s.store.ListAffiliationResults(ctx, accountID, spellID, desc, take, offset)
}

// DeleteAffiliationSpell removes one owned spell.
func (s *Service) DeleteAffiliationSpell(ctx context.Context, accountID, spellID uuid.UUID) (bool, error) {
	return s.store.DeleteAffiliationSpell(ctx, accountID, spellID)
}

// ── affiliation purchases ─────────────────────────────────────────────────

// PurchaseRegistrationInvite opens the wallet order and records the purchase.
func (s *Service) PurchaseRegistrationInvite(ctx context.Context, accountID string) (*AffiliationSpellPurchase, error) {
	options := s.settings.Affiliation
	if !options.Enabled {
		return nil, ErrAffiliationPurchasesDisabled
	}
	id, err := uuid.Parse(accountID)
	if err != nil {
		return nil, err
	}
	since := s.now().Add(-time.Duration(options.PurchasePeriodDays) * 24 * time.Hour)
	count, err := s.store.CountAffiliationPurchasesSince(ctx, id, since)
	if err != nil {
		return nil, err
	}
	if count >= int64(options.MaxPurchases) {
		return nil, ErrPurchaseLimit
	}
	purchaseID := uuid.New()
	order, err := s.createOrder(ctx, id, purchaseID, options.PricePoints, ProductAffiliationRegistrationInvite,
		"Purchase registration invitation spell")
	if err != nil {
		return nil, err
	}
	orderID, err := uuid.Parse(order)
	if err != nil {
		return nil, err
	}
	now := s.now()
	purchase := &AffiliationSpellPurchaseEntity{
		ID: purchaseID,
		EntityBase: EntityBase{
			CreatedAt: now,
			UpdatedAt: now,
		},
		AccountID: id,
		OrderID:   orderID,
		Amount:    Decimal{Decimal: options.PricePoints},
	}
	if err := s.store.CreateAffiliationPurchase(ctx, purchase); err != nil {
		return nil, err
	}
	return purchase.toWire(), nil
}

// ── name change card ──────────────────────────────────────────────────────

// PurchaseNameChangeCard opens the wallet order and records the card purchase.
func (s *Service) PurchaseNameChangeCard(ctx context.Context, accountID string) (*NameChangeCardPurchase, error) {
	options := s.settings.NameChangeCard
	if !options.Enabled {
		return nil, ErrNameChangePurchasesDisabled
	}
	id, err := uuid.Parse(accountID)
	if err != nil {
		return nil, err
	}
	since := s.now().Add(-time.Duration(options.PurchasePeriodDays) * 24 * time.Hour)
	count, err := s.store.CountNameChangeCardPurchasesSince(ctx, id, since)
	if err != nil {
		return nil, err
	}
	if count >= int64(options.MaxPurchases) {
		return nil, ErrPurchaseLimit
	}
	purchaseID := uuid.New()
	order, err := s.createOrder(ctx, id, purchaseID, options.PricePoints, ProductNameChangeCard,
		"Purchase name change card")
	if err != nil {
		return nil, err
	}
	orderID, err := uuid.Parse(order)
	if err != nil {
		return nil, err
	}
	now := s.now()
	purchase := &NameChangeCardPurchaseEntity{
		ID: purchaseID,
		EntityBase: EntityBase{
			CreatedAt: now,
			UpdatedAt: now,
		},
		AccountID: id,
		OrderID:   orderID,
		Amount:    Decimal{Decimal: options.PricePoints},
	}
	if err := s.store.CreateNameChangeCardPurchase(ctx, purchase); err != nil {
		return nil, err
	}
	return purchase.toWire(), nil
}

// ListNameChangeCardPurchases lists the account's purchases newest first.
func (s *Service) ListNameChangeCardPurchases(ctx context.Context, accountID uuid.UUID) ([]*NameChangeCardPurchase, error) {
	entities, err := s.store.ListNameChangeCardPurchases(ctx, accountID)
	if err != nil {
		return nil, err
	}
	out := make([]*NameChangeCardPurchase, 0, len(entities))
	for i := range entities {
		out = append(out, entities[i].toWire())
	}
	return out, nil
}

// accountNameRegex is NameChangeCardService.AccountNameRegex.
var accountNameRegex = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// UseNameChangeCard consumes the oldest fulfilled, unconsumed card. The row is
// locked and marked consumed in one transaction; an invalid target rolls back
// without consuming the card.
func (s *Service) UseNameChangeCard(ctx context.Context, accountID uuid.UUID, target string, targetID *uuid.UUID, newName string) (*NameChangeCardPurchase, error) {
	switch target {
	case TargetAccount:
		trimmed := strings.TrimSpace(newName)
		if len([]rune(trimmed)) < 2 || len([]rune(trimmed)) > 256 || !accountNameRegex.MatchString(trimmed) {
			return nil, ErrBadAccountName
		}
		newName = trimmed
	case TargetRealm:
		if targetID == nil {
			return nil, ErrTargetIDRequired
		}
		if strings.TrimSpace(newName) == "" {
			return nil, ErrSlugEmpty
		}
		newName = strings.TrimSpace(newName)
	case TargetPublisher:
		if targetID == nil {
			return nil, ErrTargetIDRequired
		}
		if s.publisher == nil {
			return nil, ErrPublisherMissing
		}
	default:
		return nil, ErrUnsupportedTarget
	}

	tx := s.store.DB.WithContext(ctx).Begin()
	if tx.Error != nil {
		return nil, tx.Error
	}
	defer func() { _ = tx.Rollback() }()

	purchase, err := s.store.LockNameChangeCardPurchase(ctx, tx, accountID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNoNameChangeCard
		}
		return nil, err
	}

	var oldName string
	switch target {
	case TargetAccount:
		if s.accounts == nil {
			return nil, fmt.Errorf("account renaming is not available")
		}
		current, err := s.accounts.GetAccountByID(ctx, accountID)
		if err != nil {
			return nil, err
		}
		oldName = current.Name
		if _, err := s.accounts.RenameAccount(ctx, accountID, newName); err != nil {
			return nil, err
		}
	case TargetRealm:
		oldName, err = s.store.RenameRealmSlugTx(ctx, tx, accountID, *targetID, newName)
		if err != nil {
			return nil, err
		}
	case TargetPublisher:
		oldName, err = s.publisher.RenamePublisher(ctx, *targetID, accountID, newName)
		if err != nil {
			return nil, err
		}
	}

	if err := s.store.ConsumeNameChangeCardPurchase(ctx, tx, purchase, target, targetID, oldName, newName); err != nil {
		return nil, err
	}
	if err := tx.Commit().Error; err != nil {
		return nil, err
	}
	return purchase.toWire(), nil
}

// ── order fulfillment ─────────────────────────────────────────────────────

// HandleOrderPaid is the ports.OrderEvents subscriber. It is exported so a
// deployment without the wallet module (and tests) can drive fulfillment
// directly. Both branches are idempotent: repeated deliveries only observe the
// already-fulfilled row.
func (s *Service) HandleOrderPaid(ctx context.Context, event ports.OrderEvent) error {
	switch event.ProductIdentifier {
	case ProductAffiliationRegistrationInvite:
		purchaseID, err := metaUUID(event.Meta, "purchase_id")
		if err != nil {
			return fmt.Errorf("affiliation purchase order %s: %w", event.OrderID, err)
		}
		orderID, err := uuid.Parse(event.OrderID)
		if err != nil {
			return fmt.Errorf("affiliation purchase order id %q: %w", event.OrderID, err)
		}
		accountID, err := optionalMetaUUID(event.Meta, "account_id")
		if err != nil {
			return fmt.Errorf("affiliation purchase order %s: %w", event.OrderID, err)
		}
		word, err := randomSpellWord(8)
		if err != nil {
			return err
		}
		purchase, spell, applied, err := s.store.FulfillAffiliationPurchase(ctx, purchaseID, orderID, accountID, word)
		if err != nil {
			return err
		}
		if purchase == nil {
			return nil
		}
		if applied && s.log != nil {
			s.log.Info("fulfilled affiliation registration invite purchase",
				"purchase_id", purchase.ID, "order_id", orderID, "spell_id", spell.ID)
		}
		return nil
	case ProductNameChangeCard:
		purchaseID, err := metaUUID(event.Meta, "purchase_id")
		if err != nil {
			return fmt.Errorf("name change card order %s: %w", event.OrderID, err)
		}
		orderID, err := uuid.Parse(event.OrderID)
		if err != nil {
			return fmt.Errorf("name change card order id %q: %w", event.OrderID, err)
		}
		accountID, err := optionalMetaUUID(event.Meta, "account_id")
		if err != nil {
			return fmt.Errorf("name change card order %s: %w", event.OrderID, err)
		}
		if _, err := s.store.FulfillNameChangeCardPurchase(ctx, purchaseID, orderID, accountID); err != nil {
			if errors.Is(err, ErrNotFound) {
				return nil
			}
			return err
		}
		return nil
	default:
		return nil
	}
}

// createOrder opens the points order with the metadata the paid-order consumer
// reads back.
func (s *Service) createOrder(ctx context.Context, accountID, purchaseID uuid.UUID, amount decimal.Decimal, product, remarks string) (string, error) {
	if s.payments == nil || !s.payments.Available() {
		return "", ports.ErrUnavailable
	}
	app := AppIdentifierInternal
	orderRemarks := remarks
	productIdentifier := product
	created, err := s.payments.CreateOrder(ctx, ports.OrderRequest{
		AccountID:         accountID.String(),
		Currency:          CurrencyPoints,
		Amount:            amount,
		ProductIdentifier: &productIdentifier,
		AppIdentifier:     &app,
		Remarks:           &orderRemarks,
		Meta: map[string]any{
			"account_id":  accountID.String(),
			"purchase_id": purchaseID.String(),
		},
	})
	if err != nil {
		return "", err
	}
	return created.ID, nil
}

// ── meta helpers ──────────────────────────────────────────────────────────

func metaUUID(meta map[string]any, key string) (uuid.UUID, error) {
	value := metaString(meta, key)
	if value == "" {
		return uuid.Nil, fmt.Errorf("metadata %s is missing", key)
	}
	return uuid.Parse(value)
}

func optionalMetaUUID(meta map[string]any, key string) (*uuid.UUID, error) {
	value := metaString(meta, key)
	if value == "" {
		return nil, nil
	}
	parsed, err := uuid.Parse(value)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func metaString(meta map[string]any, key string) string {
	if meta == nil {
		return ""
	}
	switch value := meta[key].(type) {
	case string:
		return strings.TrimSpace(value)
	case json.Number:
		return value.String()
	case uuid.UUID:
		return value.String()
	case fmt.Stringer:
		return value.String()
	default:
		return ""
	}
}

func intMeta(meta map[string]any, key string) (int, bool) {
	if meta == nil {
		return 0, false
	}
	value, ok := meta[key]
	if !ok || value == nil {
		return 0, false
	}
	switch typed := value.(type) {
	case int:
		return typed, true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		return int(parsed), err == nil
	default:
		return 0, false
	}
}

func boolMeta(meta map[string]any, key string) (bool, bool) {
	if meta == nil {
		return false, false
	}
	value, ok := meta[key]
	if !ok || value == nil {
		return false, false
	}
	parsed, ok := value.(bool)
	return parsed, ok
}

// randomSpellWord mirrors AffiliationSpellService._GenerateRandomString (the
// same uppercase/digit charset and modulo selection).
func randomSpellWord(length int) (string, error) {
	const charset = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	raw := make([]byte, length)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	out := make([]byte, length)
	for i, value := range raw {
		out[i] = charset[int(value)%len(charset)]
	}
	return string(out), nil
}

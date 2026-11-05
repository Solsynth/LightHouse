package nfc

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/ports"
	"src.solsynth.dev/sosys/stargate/relationship"
	"src.solsynth.dev/sosys/stargate/store"
)

// Deps is the service dependency set.
type Deps struct {
	Store         *store.Store
	Relationships *relationship.Service
	Perks         ports.Perks
	Badges        ports.Badges
	Log           *slog.Logger
}

// Service is the ported NfcService.
type Service struct {
	store         *store.Store
	relationships *relationship.Service
	perks         ports.Perks
	badges        ports.Badges
	log           *slog.Logger
}

// NewService builds the NFC domain service. Optional collaborators (the
// relationships module, wallet perks, Passport badges) degrade to no-ops.
func NewService(deps Deps) *Service {
	log := deps.Log
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		store:         deps.Store,
		relationships: deps.Relationships,
		perks:         deps.Perks,
		badges:        deps.Badges,
		log:           log,
	}
}

// Available reports whether the service can serve reads.
func (s *Service) Available() bool { return s != nil && s.store != nil && s.store.DB != nil }

func (s *Service) now() time.Time { return time.Now().UTC() }

// Resolve resolves a UID read directly from an unencrypted tag and records
// the scan.
func (s *Service) Resolve(ctx context.Context, uid string, observer *uuid.UUID) (*ResolveResult, error) {
	var tag TagEntity
	err := s.store.DB.WithContext(ctx).
		Where("uid = ? AND is_active = ?", strings.ToUpper(uid), true).
		First(&tag).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	now := s.now()
	tag.LastSeenAt = &now
	if err := s.store.DB.WithContext(ctx).
		Model(&TagEntity{}).Where("id = ?", tag.ID).
		Update("last_seen_at", now).Error; err != nil {
		return nil, err
	}
	return s.buildResult(ctx, &tag, observer)
}

// LookupByUID resolves a tag without recording a scan.
func (s *Service) LookupByUID(ctx context.Context, uid string, observer *uuid.UUID) (*ResolveResult, error) {
	var tag TagEntity
	err := s.store.DB.WithContext(ctx).
		Where("uid = ? AND is_active = ?", strings.ToUpper(uid), true).
		First(&tag).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.buildResult(ctx, &tag, observer)
}

// LookupByID resolves a plain tag by its database entry id.
func (s *Service) LookupByID(ctx context.Context, tagID uuid.UUID, observer *uuid.UUID) (*ResolveResult, error) {
	var tag TagEntity
	err := s.store.DB.WithContext(ctx).
		Where("id = ? AND is_active = ?", tagID, true).
		First(&tag).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.buildResult(ctx, &tag, observer)
}

// ValidateSun validates NTAG 424 DNA SDM scan data: it decrypts the PICCData
// with the tag's SUN key, matches the decrypted UID, verifies the CMAC when
// present, enforces the replay counter and applies Passport's claim rules.
func (s *Service) ValidateSun(ctx context.Context, piccDataHex, encDataHex, cmacHex string, observer *uuid.UUID) (*ValidationResult, error) {
	encryptedPICCData, err := hex.DecodeString(piccDataHex)
	if err != nil {
		s.log.Warn("SUN scan: invalid picc_data hex format")
		return nil, nil
	}
	if len(encryptedPICCData) != 16 {
		s.log.Warn("SUN scan: picc_data must be exactly 16 bytes", "length", len(encryptedPICCData))
		return nil, nil
	}

	var encData []byte
	if encDataHex != "" {
		encData, err = hex.DecodeString(encDataHex)
		if err != nil {
			s.log.Warn("SUN scan: invalid e hex format")
			return nil, nil
		}
		if len(encData)%16 != 0 {
			s.log.Warn("SUN scan: enc data length must be a multiple of 16")
			return nil, nil
		}
	}

	var cmac []byte
	if cmacHex != "" {
		cmac, err = hex.DecodeString(cmacHex)
		if err != nil {
			s.log.Warn("SUN scan: invalid cmac hex format")
			return nil, nil
		}
		if len(cmac) != 8 {
			s.log.Warn("SUN scan: cmac must be exactly 8 bytes", "length", len(cmac))
			return nil, nil
		}
		// Passport builds the MAC input from the uppercase encrypted payload;
		// without it there is nothing to verify against.
		if encDataHex == "" {
			s.log.Warn("SUN scan: cmac provided without e")
			return nil, nil
		}
	}

	var tags []TagEntity
	if err := s.store.DB.WithContext(ctx).
		Where("is_active = ? AND is_encrypted = ? AND sun_key IS NOT NULL", true, true).
		Find(&tags).Error; err != nil {
		return nil, err
	}
	s.log.Debug("SUN scan: trying encrypted tags", "count", len(tags))

	for i := range tags {
		tag := &tags[i]
		// Passport uses SUN key as both the meta read key and the file read key.
		metaKey := tag.SunKey
		fileKey := tag.SunKey

		piccPlain, ok := DecryptPICCDataWithMetaKey(metaKey, encryptedPICCData)
		if !ok {
			continue
		}
		piccData, ok := ParsePICCData(piccPlain)
		if !ok {
			continue
		}
		if piccData.UID == nil || piccData.ReadCtrLSB == nil {
			continue
		}

		decryptedUID := FormatUID(piccData.UID)
		if !strings.EqualFold(decryptedUID, tag.UID) {
			continue
		}

		ksesEnc, ksesMac, ok := DeriveSessionKeys(fileKey, piccData.UID, piccData.ReadCtrLSB)
		if !ok {
			continue
		}

		if cmac != nil {
			macInput := []byte(strings.ToUpper(encDataHex) + "&cmac=")
			if !VerifyCMAC(ksesMac, macInput, cmac) {
				s.log.Warn("SUN scan: CMAC verification failed", "tag_id", tag.ID)
				continue
			}
		}

		if encData != nil {
			if ive, ok := BuildIVe(ksesEnc, piccData.ReadCtrLSB); ok {
				if _, ok := DecryptFileData(ksesEnc, encData, ive); !ok {
					s.log.Debug("SUN scan: file data decryption failed", "tag_id", tag.ID)
				}
			}
		}

		// Counter replay check: strictly reject counter less than or equal to last seen counter.
		if tag.Counter != nil && piccData.ReadCtr <= *tag.Counter {
			return nil, fmt.Errorf("%w: counter %d is not greater than last seen counter %d.",
				ErrReplay, piccData.ReadCtr, *tag.Counter)
		}

		now := s.now()
		counter := piccData.ReadCtr
		if err := s.store.DB.WithContext(ctx).Model(&TagEntity{}).
			Where("id = ?", tag.ID).
			Updates(map[string]any{"counter": counter, "last_seen_at": now}).Error; err != nil {
			return nil, err
		}
		tag.Counter = &counter
		tag.LastSeenAt = &now

		actions := make([]string, 0, 2)
		switch {
		case tag.AccountID == nil:
			// Unclaimed tags are never auto-claimed: the user claims them
			// explicitly through POST /api/nfc/tags/claim.
			if observer != nil {
				return &ValidationResult{
					Tag: tag, IsClaimed: false, ClaimStatus: ClaimUnclaimed,
					Actions: []string{"claim_tag"},
				}, nil
			}
			return &ValidationResult{
				Tag: tag, IsClaimed: false, ClaimStatus: ClaimNeedsAuth,
				Actions: []string{},
			}, nil
		case observer != nil && *observer != *tag.AccountID:
			// Scanning as a different account than the tag owner.
			return s.validationResultFor(ctx, tag, observer, ClaimPreAssignedMismatch, actions)
		}

		return s.validationResultFor(ctx, tag, observer, ClaimHasOwner, actions)
	}

	s.log.Warn("SUN scan: no matching tag found")
	return nil, nil
}

// validationResultFor loads the owner account and applies the observer
// relationship rules.
func (s *Service) validationResultFor(ctx context.Context, tag *TagEntity, observer *uuid.UUID, status ClaimStatus, actions []string) (*ValidationResult, error) {
	if tag.AccountID == nil {
		return &ValidationResult{Tag: tag, ClaimStatus: status, Actions: actions}, nil
	}
	account, err := s.loadAccount(ctx, *tag.AccountID)
	if err != nil {
		return nil, err
	}
	if account == nil {
		return &ValidationResult{Tag: tag, ClaimStatus: status, Actions: actions}, nil
	}
	result := &ValidationResult{
		Tag:         tag,
		Account:     account,
		Profile:     account.Profile,
		IsClaimed:   false,
		ClaimStatus: status,
		Actions:     append(actions, "view_profile"),
	}

	if observer != nil && *observer != *tag.AccountID {
		blocked, err := s.blockedEitherDirection(ctx, *observer, *tag.AccountID)
		if err != nil {
			return nil, err
		}
		if blocked {
			// Blocked: keep the tag/account for the login path but expose no
			// profile actions.
			result.Actions = []string{}
			return result, nil
		}
		isFriend, err := s.areFriends(ctx, *observer, *tag.AccountID)
		if err != nil {
			return nil, err
		}
		result.IsFriend = isFriend
		result.Actions = append(result.Actions, "add_friend")
	}
	return result, nil
}

func (s *Service) buildResult(ctx context.Context, tag *TagEntity, observer *uuid.UUID) (*ResolveResult, error) {
	if tag.AccountID == nil {
		return nil, nil
	}
	account, err := s.loadAccount(ctx, *tag.AccountID)
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, nil
	}

	isFriend := false
	actions := []string{"view_profile"}
	if observer != nil && *observer != *tag.AccountID {
		blocked, err := s.blockedEitherDirection(ctx, *observer, *tag.AccountID)
		if err != nil {
			return nil, err
		}
		if blocked {
			return nil, nil
		}
		isFriend, err = s.areFriends(ctx, *observer, *tag.AccountID)
		if err != nil {
			return nil, err
		}
		actions = append(actions, "add_friend")
	}

	return &ResolveResult{
		ID:        tag.ID,
		Account:   account,
		IsFriend:  isFriend,
		IsClaimed: false,
		Actions:   actions,
	}, nil
}

// loadAccount hydrates an account with its profile, perk subscription and
// badges; a missing account returns (nil, nil), mirroring AccountService.
func (s *Service) loadAccount(ctx context.Context, accountID uuid.UUID) (*model.Account, error) {
	account, err := s.store.GetAccountByID(ctx, accountID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if account.Profile == nil {
		profile, err := s.store.GetOrCreateAccountProfile(ctx, accountID)
		if err != nil {
			return nil, err
		}
		account.Profile = profile
	}
	if s.perks != nil && s.perks.Available() {
		subscription, err := s.perks.Subscription(ctx, account.Id)
		if err != nil {
			s.log.Warn("nfc: hydrate perk subscription", "account", account.Id, "error", err)
			account.PerkSubscription = nil
			account.PerkLevel = 0
		} else if subscription != nil {
			account.PerkSubscription = subscription
			account.PerkLevel = subscription.PerkLevel
		} else {
			account.PerkSubscription = nil
			account.PerkLevel = 0
		}
	}
	if s.badges != nil && s.badges.Available() {
		if badges, err := s.badges.List(ctx, account.Id); err == nil {
			account.Badges = badges
		} else {
			s.log.Warn("nfc: hydrate badges", "account", account.Id, "error", err)
		}
	}
	account.Contacts = []model.Contact{}
	return account, nil
}

func (s *Service) areFriends(ctx context.Context, accountID, otherID uuid.UUID) (bool, error) {
	if s.relationships == nil || !s.relationships.Available() {
		return false, nil
	}
	return s.relationships.HasStatus(ctx, accountID, otherID, model.RelationshipFriends)
}

func (s *Service) blockedEitherDirection(ctx context.Context, accountID, otherID uuid.UUID) (bool, error) {
	if s.relationships == nil || !s.relationships.Available() {
		return false, nil
	}
	return s.relationships.IsBlockedEitherDirection(ctx, accountID, otherID)
}

// RegisterTag registers a plain (unencrypted) tag, written to the physical
// tag as the entry UUID.
func (s *Service) RegisterTag(ctx context.Context, userID uuid.UUID, uid string, label *string) (*TagEntity, error) {
	normalized := strings.ToUpper(uid)
	var existing TagEntity
	err := s.store.DB.WithContext(ctx).Where("uid = ?", normalized).First(&existing).Error
	if err == nil {
		return nil, ErrTagExists
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	tag := &TagEntity{
		ID:          uuid.New(),
		UID:         normalized,
		AccountID:   &userID,
		Label:       label,
		IsActive:    true,
		IsEncrypted: false,
	}
	if err := s.store.DB.WithContext(ctx).Create(tag).Error; err != nil {
		return nil, err
	}
	return tag, nil
}

// RegisterEncryptedTag registers a factory-provisioned encrypted tag with a
// pre-generated SUN key, optionally pre-assigned to an account.
func (s *Service) RegisterEncryptedTag(ctx context.Context, uid string, sunKey []byte, assignedUserID *uuid.UUID) (*TagEntity, error) {
	normalized := strings.ToUpper(uid)
	var existing TagEntity
	err := s.store.DB.WithContext(ctx).Where("uid = ?", normalized).First(&existing).Error
	if err == nil {
		return nil, ErrTagExists
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if len(sunKey) != 16 && len(sunKey) != 32 {
		return nil, ErrInvalidSunKey
	}

	counter := 0
	tag := &TagEntity{
		ID:          uuid.New(),
		UID:         normalized,
		AccountID:   assignedUserID,
		IsActive:    true,
		IsEncrypted: true,
		SunKey:      sunKey,
		Counter:     &counter,
	}
	if err := s.store.DB.WithContext(ctx).Create(tag).Error; err != nil {
		return nil, err
	}
	return tag, nil
}

// ClaimTagByUID claims an unclaimed encrypted tag by its printed UID.
func (s *Service) ClaimTagByUID(ctx context.Context, uid string, userID uuid.UUID) (*TagEntity, error) {
	var tag TagEntity
	err := s.store.DB.WithContext(ctx).
		Where("uid = ? AND is_encrypted = ? AND is_active = ?", strings.ToUpper(uid), true, true).
		First(&tag).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrEncryptedTagByUID
	}
	if err != nil {
		return nil, err
	}
	return s.claimTag(ctx, &tag, userID)
}

// ClaimTagByID claims an unclaimed encrypted tag by its database entry id.
func (s *Service) ClaimTagByID(ctx context.Context, tagID uuid.UUID, userID uuid.UUID) (*TagEntity, error) {
	var tag TagEntity
	err := s.store.DB.WithContext(ctx).
		Where("id = ? AND is_encrypted = ? AND is_active = ?", tagID, true, true).
		First(&tag).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrEncryptedTagByID
	}
	if err != nil {
		return nil, err
	}
	return s.claimTag(ctx, &tag, userID)
}

func (s *Service) claimTag(ctx context.Context, tag *TagEntity, userID uuid.UUID) (*TagEntity, error) {
	if tag.AccountID != nil && *tag.AccountID == userID {
		return tag, nil
	}
	if tag.AccountID != nil && *tag.AccountID != userID {
		return nil, ErrTagClaimedByAnother
	}

	now := s.now()
	if err := s.store.DB.WithContext(ctx).Model(&TagEntity{}).
		Where("id = ?", tag.ID).
		Updates(map[string]any{"account_id": userID, "last_seen_at": now}).Error; err != nil {
		return nil, err
	}
	tag.AccountID = &userID
	tag.LastSeenAt = &now
	return tag, nil
}

// ListTags lists the active tags owned by an account.
func (s *Service) ListTags(ctx context.Context, userID uuid.UUID) ([]TagEntity, error) {
	var tags []TagEntity
	err := s.store.DB.WithContext(ctx).
		Where("account_id = ? AND is_active = ?", userID, true).
		Order("created_at DESC").
		Find(&tags).Error
	return tags, err
}

// UpdateTag updates a tag's label / active flag. A tag owned by another
// account is reported as not found.
func (s *Service) UpdateTag(ctx context.Context, userID, tagID uuid.UUID, label *string, isActive *bool) (*TagEntity, error) {
	var tag TagEntity
	err := s.store.DB.WithContext(ctx).
		Where("id = ? AND account_id = ?", tagID, userID).
		First(&tag).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	updates := map[string]any{}
	if label != nil {
		tag.Label = label
		updates["label"] = *label
	}
	if isActive != nil {
		tag.IsActive = *isActive
		updates["is_active"] = *isActive
	}
	if len(updates) == 0 {
		return &tag, nil
	}
	if err := s.store.DB.WithContext(ctx).Model(&TagEntity{}).
		Where("id = ?", tag.ID).Updates(updates).Error; err != nil {
		return nil, err
	}
	return &tag, nil
}

// LockTag records the physical lock timestamp.
func (s *Service) LockTag(ctx context.Context, userID, tagID uuid.UUID) (*TagEntity, error) {
	var tag TagEntity
	err := s.store.DB.WithContext(ctx).
		Where("id = ? AND account_id = ?", tagID, userID).
		First(&tag).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	now := s.now()
	if err := s.store.DB.WithContext(ctx).Model(&TagEntity{}).
		Where("id = ?", tag.ID).Update("locked_at", now).Error; err != nil {
		return nil, err
	}
	tag.LockedAt = &now
	return &tag, nil
}

// UnregisterTag soft-deletes a tag owned by the account.
func (s *Service) UnregisterTag(ctx context.Context, userID, tagID uuid.UUID) (bool, error) {
	var tag TagEntity
	err := s.store.DB.WithContext(ctx).
		Where("id = ? AND account_id = ?", tagID, userID).
		First(&tag).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	now := s.now()
	if err := s.store.DB.WithContext(ctx).Model(&TagEntity{}).
		Where("id = ?", tag.ID).
		Updates(map[string]any{"is_active": false, "deleted_at": now}).Error; err != nil {
		return false, err
	}
	return true, nil
}

// ListAllEncryptedTags lists every active encrypted tag (factory/admin view).
func (s *Service) ListAllEncryptedTags(ctx context.Context) ([]TagEntity, error) {
	var tags []TagEntity
	err := s.store.DB.WithContext(ctx).
		Where("is_encrypted = ? AND is_active = ?", true, true).
		Order("created_at DESC").
		Find(&tags).Error
	return tags, err
}

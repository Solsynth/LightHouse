package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

// This file adds the auth-challenge / account-registration query helpers used
// by internal/httpserver/authctl. It never touches existing store files.

// challengeFromEntity maps a persisted challenge row to the API model. A NULL
// account_id — anonymous challenges (discoverable passkey login, QR login) —
// normalizes back to the all-zero sentinel the handlers compare against.
func challengeFromEntity(entity *ChallengeEntity) *model.AuthChallenge {
	if entity == nil {
		return nil
	}
	challenge := &model.AuthChallenge{
		Id:                  entity.ID.String(),
		AccountId:           accountIDOrSentinel(uuidPtrStr(entity.AccountID)),
		ExpiredAt:           timePtr(entity.ExpiredAt),
		StepRemain:          entity.StepRemain,
		StepTotal:           entity.StepTotal,
		FailedAttempts:      entity.FailedAttempts,
		IpAddress:           entity.IPAddress,
		UserAgent:           entity.UserAgent,
		DeviceId:            entity.DeviceID,
		DeviceName:          entity.DeviceName,
		Platform:            model.ClientPlatform(entity.Platform),
		Nonce:               entity.Nonce,
		ApprovedAt:          timePtr(entity.ApprovedAt),
		DeclinedAt:          timePtr(entity.DeclinedAt),
		ApprovedBySessionId: uuidPtrStr(entity.ApprovedBySessionID),
		CreatedAt:           timePtr(&entity.CreatedAt),
		UpdatedAt:           timePtr(&entity.UpdatedAt),
		DeletedAt:           deletedTime(entity.DeletedAt),
	}
	_ = decodeJSONValue(entity.Audiences, &challenge.Audiences)
	_ = decodeJSONValue(entity.Scopes, &challenge.Scopes)
	_ = decodeJSONValue(entity.BlacklistFactors, &challenge.BlacklistFactors)
	_ = decodeJSON(entity.Location, &challenge.Location)
	return challenge
}

// GetAuthChallenge loads a challenge by id. Soft-deleted rows stay readable
// (the previous query never filtered deleted_at).
func (s *Store) GetAuthChallenge(ctx context.Context, id uuid.UUID) (*model.AuthChallenge, error) {
	var entity ChallengeEntity
	if err := s.DB.WithContext(ctx).Unscoped().First(&entity, "id = ?", id).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return challengeFromEntity(&entity), nil
}

// nullableAccountID maps the all-zero-UUID sentinel to NULL so anonymous
// challenges (discoverable passkey login, QR login) can be stored without an
// accounts row (account_id is nullable; see 0003 migration).
func nullableAccountID(accountID string) any {
	if accountID == "" || accountID == uuid.Nil.String() {
		return nil
	}
	return accountID
}

// accountIDOrSentinel normalizes a NULL account_id back to the sentinel so
// handlers keep treating uuid.Nil.String() as the "no account yet" marker.
func accountIDOrSentinel(accountID *string) string {
	if accountID == nil {
		return uuid.Nil.String()
	}
	return *accountID
}

// accountUUID is the typed sibling of nullableAccountID for entity writes:
// anonymous challenges (sentinel/empty) persist a NULL account_id, everything
// else must be a valid uuid.
func accountUUID(accountID string) (*uuid.UUID, error) {
	if accountID == "" || accountID == uuid.Nil.String() {
		return nil, nil
	}
	id, err := uuid.Parse(accountID)
	if err != nil {
		return nil, fmt.Errorf("invalid account id %q: %w", accountID, err)
	}
	return &id, nil
}

// parseUUIDPtr parses an optional uuid string; nil and "" yield nil so the
// nullable column receives NULL.
func parseUUIDPtr(value *string) (*uuid.UUID, error) {
	if value == nil || *value == "" {
		return nil, nil
	}
	id, err := uuid.Parse(*value)
	if err != nil {
		return nil, fmt.Errorf("invalid uuid %q: %w", *value, err)
	}
	return &id, nil
}

// CreateAuthChallenge inserts a challenge (ids and timestamps must be set).
func (s *Store) CreateAuthChallenge(ctx context.Context, ch *model.AuthChallenge) error {
	id, err := uuid.Parse(ch.Id)
	if err != nil {
		return fmt.Errorf("invalid challenge id %q: %w", ch.Id, err)
	}
	accountID, err := accountUUID(ch.AccountId)
	if err != nil {
		return err
	}
	approvedBy, err := parseUUIDPtr(ch.ApprovedBySessionId)
	if err != nil {
		return err
	}
	// location is nullable jsonb: a nil GeoPoint stores SQL NULL (a boxed nil
	// pointer would marshal to the JSON literal "null").
	var location *datatypes.JSON
	if ch.Location != nil {
		encoded, err := encodeJSONPtr(ch.Location)
		if err != nil {
			return err
		}
		location = encoded
	}
	createdAt, updatedAt := timeValue(ch.CreatedAt), timeValue(ch.UpdatedAt)
	if createdAt == nil || updatedAt == nil {
		return fmt.Errorf("challenge %s must carry created_at and updated_at", ch.Id)
	}
	base := EntityBase{CreatedAt: *createdAt, UpdatedAt: *updatedAt}
	if deletedAt := timeValue(ch.DeletedAt); deletedAt != nil {
		base.DeletedAt = gorm.DeletedAt{Time: *deletedAt, Valid: true}
	}
	return s.DB.WithContext(ctx).Create(&ChallengeEntity{
		ID:                  id,
		EntityBase:          base,
		AccountID:           accountID,
		ApprovedAt:          timeValue(ch.ApprovedAt),
		ApprovedBySessionID: approvedBy,
		// audiences/scopes/blacklist_factors are jsonb NOT NULL: jsonbOrEmpty
		// keeps a nil slice as '[]' instead of SQL NULL.
		Audiences:        datatypes.JSON(jsonbOrEmpty(ch.Audiences)),
		BlacklistFactors: datatypes.JSON(jsonbOrEmpty(ch.BlacklistFactors)),
		DeclinedAt:       timeValue(ch.DeclinedAt),
		DeviceID:         ch.DeviceId,
		DeviceName:       ch.DeviceName,
		ExpiredAt:        timeValue(ch.ExpiredAt),
		FailedAttempts:   ch.FailedAttempts,
		IPAddress:        ch.IpAddress,
		Location:         location,
		Nonce:            ch.Nonce,
		Platform:         int(ch.Platform),
		Scopes:           datatypes.JSON(jsonbOrEmpty(ch.Scopes)),
		StepRemain:       ch.StepRemain,
		StepTotal:        ch.StepTotal,
		UserAgent:        ch.UserAgent,
	}).Error
}

// jsonbOrEmpty marshals a slice as JSON, using '[]' for nil so jsonb NOT NULL
// columns never receive NULL.
func jsonbOrEmpty[T any](v []T) []byte {
	if v == nil {
		return []byte("[]")
	}
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("[]")
	}
	return b
}

// UpdateAuthChallenge persists the mutable challenge fields. Unscoped mirrors
// the previous statement, which updated rows regardless of deleted_at.
func (s *Store) UpdateAuthChallenge(ctx context.Context, ch *model.AuthChallenge) error {
	id, err := uuid.Parse(ch.Id)
	if err != nil {
		return fmt.Errorf("invalid challenge id %q: %w", ch.Id, err)
	}
	approvedBy, err := parseUUIDPtr(ch.ApprovedBySessionId)
	if err != nil {
		return err
	}
	// updated_at comes from the caller (all call sites set it right before
	// invoking), mirroring the legacy `updated_at = $11` binding.
	return s.DB.WithContext(ctx).Unscoped().Model(&ChallengeEntity{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"account_id":             nullableAccountID(ch.AccountId),
			"approved_at":            timeValue(ch.ApprovedAt),
			"approved_by_session_id": approvedBy,
			"blacklist_factors":      datatypes.JSON(jsonbOrEmpty(ch.BlacklistFactors)),
			"declined_at":            timeValue(ch.DeclinedAt),
			"expired_at":             timeValue(ch.ExpiredAt),
			"failed_attempts":        ch.FailedAttempts,
			"step_remain":            ch.StepRemain,
			"step_total":             ch.StepTotal,
			"updated_at":             timeValue(ch.UpdatedAt),
		}).Error
}

// FindLiveChallenge returns the newest live challenge for the same
// (account, ip, user-agent, device) triple, mirroring the reuse semantics of
// AuthController.CreateChallenge.
func (s *Store) FindLiveChallenge(ctx context.Context, accountID, ipAddress, userAgent, deviceID string) (*model.AuthChallenge, error) {
	var entity ChallengeEntity
	result := s.DB.WithContext(ctx).
		Where("account_id = ? AND ip_address = ? AND user_agent = ? AND device_id = ?",
			accountID, ipAddress, userAgent, deviceID).
		Where("step_remain > 0 AND expired_at IS NOT NULL AND expired_at > now()").
		Order("created_at DESC").Limit(1).Find(&entity)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, ErrNotFound
	}
	return challengeFromEntity(&entity), nil
}

// ListPendingChallenges lists the account's pending (unapproved, undeclined,
// live) challenges newest first.
func (s *Store) ListPendingChallenges(ctx context.Context, accountID string) ([]model.AuthChallenge, error) {
	var entities []ChallengeEntity
	if err := s.DB.WithContext(ctx).
		Where("account_id = ?", accountID).
		Where("approved_at IS NULL AND declined_at IS NULL AND step_remain > 0").
		Where("expired_at IS NULL OR expired_at > now()").
		Order("created_at DESC").Find(&entities).Error; err != nil {
		return nil, err
	}
	var challenges []model.AuthChallenge
	for i := range entities {
		challenges = append(challenges, *challengeFromEntity(&entities[i]))
	}
	return challenges, nil
}

// GetAuthFactorByType loads the account's oldest non-deleted factor of the
// given type.
func (s *Store) GetAuthFactorByType(ctx context.Context, accountID string, ftype model.AuthFactorType) (*model.AuthFactor, error) {
	var entity AuthFactorEntity
	if err := s.DB.WithContext(ctx).
		Where("account_id = ? AND type = ?", accountID, int(ftype)).
		Order("created_at").First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	factor := factorFromEntity(&entity)
	return &factor, nil
}

// LookupAccount resolves an account by name (case-insensitive) then by
// email/phone contact, mirroring AccountService.LookupAccount.
func (s *Store) LookupAccount(ctx context.Context, probe string) (*model.Account, error) {
	var account AccountEntity
	err := s.DB.WithContext(ctx).Where("name ILIKE ?", probe).First(&account).Error
	if err == nil {
		return accountFromEntity(&account), nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	var contact ContactEntity
	err = s.DB.WithContext(ctx).
		Where("type IN ?", []int{int(model.ContactTypeEmail), int(model.ContactTypePhoneNumber)}).
		Where("content ILIKE ?", probe).First(&contact).Error
	if err != nil {
		return nil, mapNotFound(err)
	}
	return s.GetAccountByID(ctx, contact.AccountID)
}

// CheckAccountNameTaken reports whether the name is already used
// (case-insensitive), mirroring CheckAccountNameHasTaken. Soft-deleted
// accounts hold their name (the previous query had no deleted_at filter).
func (s *Store) CheckAccountNameTaken(ctx context.Context, name string) (bool, error) {
	var count int64
	if err := s.DB.WithContext(ctx).Unscoped().Model(&AccountEntity{}).
		Where("lower(name) = lower(?)", name).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// CheckEmailUsed reports whether an email contact already exists
// (case-insensitive), mirroring CheckEmailHasBeenUsed.
func (s *Store) CheckEmailUsed(ctx context.Context, email string) (bool, error) {
	var count int64
	if err := s.DB.WithContext(ctx).Model(&ContactEntity{}).
		Where("type = ? AND content ILIKE ?", int(model.ContactTypeEmail), email).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// CreateAccountWithRegistration atomically creates the account, its primary
// email contact, its password auth factor (bcrypt hash) and the `default`
// permission-group membership, mirroring AccountService.CreateAccount.
func (s *Store) CreateAccountWithRegistration(ctx context.Context, acc *model.Account, email string, passwordHash string) error {
	accountID, err := uuid.Parse(acc.Id)
	if err != nil {
		return fmt.Errorf("invalid account id %q: %w", acc.Id, err)
	}
	now := time.Now().UTC()
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&AccountEntity{
			ID:          accountID,
			EntityBase:  EntityBase{CreatedAt: now, UpdatedAt: now},
			IsSuperuser: false,
			Language:    acc.Language,
			Name:        acc.Name,
			Nick:        acc.Nick,
			Region:      acc.Region,
		}).Error; err != nil {
			return err
		}
		if err := tx.Create(&ContactEntity{
			ID:         uuid.New(),
			EntityBase: EntityBase{CreatedAt: now, UpdatedAt: now},
			AccountID:  accountID,
			Content:    email,
			IsPrimary:  true,
			IsPublic:   false,
			Type:       int(model.ContactTypeEmail),
		}).Error; err != nil {
			return err
		}
		if err := tx.Create(&AuthFactorEntity{
			ID:          uuid.New(),
			EntityBase:  EntityBase{CreatedAt: now, UpdatedAt: now},
			AccountID:   accountID,
			EnabledAt:   &now,
			Secret:      &passwordHash,
			Trustworthy: 1,
			Type:        int(model.AuthFactorTypePassword),
		}).Error; err != nil {
			return err
		}
		// The INSERT..SELECT this replaces matched the `default` group and
		// inserted nothing when it was absent.
		var group PermissionGroupEntity
		if err := tx.Where(&PermissionGroupEntity{Key: "default"}).First(&group).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&PermissionGroupMemberEntity{
			GroupID:    group.ID,
			Actor:      acc.Id,
			EntityBase: EntityBase{CreatedAt: now, UpdatedAt: now},
		}).Error
	})
}

// RecentSessionInfo carries the fields DetectChallengeRisk needs from the
// account's recent sessions.
type RecentSessionInfo struct {
	ID            uuid.UUID
	LastGrantedAt *model.Time
	ChallengeID   *uuid.UUID
	ClientID      *uuid.UUID
	CreatedAt     time.Time
}

// ListRecentSessions returns the account's most recent sessions (by
// last_granted_at desc), mirroring DetectChallengeRisk's query.
func (s *Store) ListRecentSessions(ctx context.Context, accountID string, limit int) ([]RecentSessionInfo, error) {
	var entities []AuthSessionEntity
	if err := s.DB.WithContext(ctx).
		Select("id", "last_granted_at", "challenge_id", "client_id", "created_at").
		Where("account_id = ? AND last_granted_at IS NOT NULL", accountID).
		Order("last_granted_at DESC").Limit(limit).Find(&entities).Error; err != nil {
		return nil, err
	}
	var sessions []RecentSessionInfo
	for i := range entities {
		sessions = append(sessions, RecentSessionInfo{
			ID:            entities[i].ID,
			LastGrantedAt: timePtr(entities[i].LastGrantedAt),
			ChallengeID:   entities[i].ChallengeID,
			ClientID:      entities[i].ClientID,
			CreatedAt:     entities[i].CreatedAt,
		})
	}
	return sessions, nil
}

// ChallengeProbe carries the ip/user-agent history used by
// DetectChallengeRisk.
type ChallengeProbe struct {
	ID        uuid.UUID
	IpAddress *string
	UserAgent *string
}

// ListChallengesByIDs loads the ip/user-agent of the given challenges.
// Unscoped mirrors the previous query, which did not filter deleted_at.
func (s *Store) ListChallengesByIDs(ctx context.Context, ids []uuid.UUID) ([]ChallengeProbe, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var entities []ChallengeEntity
	if err := s.DB.WithContext(ctx).Unscoped().
		Select("id", "ip_address", "user_agent").
		Where("id IN ?", ids).Find(&entities).Error; err != nil {
		return nil, err
	}
	var probes []ChallengeProbe
	for i := range entities {
		probes = append(probes, ChallengeProbe{
			ID:        entities[i].ID,
			IpAddress: entities[i].IPAddress,
			UserAgent: entities[i].UserAgent,
		})
	}
	return probes, nil
}

// SumRecentFailedChallengeAttempts sums failed_attempts across challenges
// created after since, mirroring DetectChallengeRisk's risk component.
// Unscoped mirrors the previous query, which did not filter deleted_at.
func (s *Store) SumRecentFailedChallengeAttempts(ctx context.Context, accountID string, since time.Time) (int, error) {
	var total int64
	if err := s.DB.WithContext(ctx).Unscoped().Model(&ChallengeEntity{}).
		Select("COALESCE(SUM(failed_attempts), 0)").
		Where("account_id = ? AND created_at > ? AND failed_attempts > 0", accountID, since).
		Scan(&total).Error; err != nil {
		return 0, err
	}
	return int(total), nil
}

// PunishmentOverview mirrors SnAccountPunishment minus the hydrated account.
type PunishmentOverview struct {
	Type   model.PunishmentType
	Reason string
}

// GetActivePunishmentOverview returns the most severe active punishment
// (DisableAccount > BlockLogin > PermissionModification > Strike), mirroring
// AccountService.GetActivePunishmentOverview.
func (s *Store) GetActivePunishmentOverview(ctx context.Context, accountID string) (*PunishmentOverview, error) {
	var entities []PunishmentEntity
	if err := s.DB.WithContext(ctx).
		Select("type", "reason").
		Where("account_id = ?", accountID).
		Where("expired_at IS NULL OR expired_at > now()").
		Order("CASE type WHEN 2 THEN 0 WHEN 1 THEN 1 WHEN 0 THEN 2 WHEN 3 THEN 3 ELSE 99 END").
		Limit(1).Find(&entities).Error; err != nil {
		return nil, err
	}
	if len(entities) == 0 {
		return nil, nil
	}
	return &PunishmentOverview{
		Type:   model.PunishmentType(entities[0].Type),
		Reason: entities[0].Reason,
	}, nil
}

// passkeyFromEntity maps a persisted passkey row to the API model.
func passkeyFromEntity(entity *PasskeyEntity) model.Passkey {
	return model.Passkey{
		Id:           entity.ID.String(),
		AccountId:    entity.AccountID.String(),
		Label:        entity.Label,
		CredentialId: entity.CredentialID,
		Credential:   string(entity.Credential),
		CreatedAt:    timePtr(&entity.CreatedAt),
		UpdatedAt:    timePtr(&entity.UpdatedAt),
		DeletedAt:    deletedTime(entity.DeletedAt),
	}
}

// ListPasskeysByAccount lists the account's registered passkeys.
func (s *Store) ListPasskeysByAccount(ctx context.Context, accountID string) ([]model.Passkey, error) {
	var entities []PasskeyEntity
	if err := s.DB.WithContext(ctx).Where("account_id = ?", accountID).Find(&entities).Error; err != nil {
		return nil, err
	}
	var passkeys []model.Passkey
	for i := range entities {
		passkeys = append(passkeys, passkeyFromEntity(&entities[i]))
	}
	return passkeys, nil
}

// GetPasskeyByCredentialID loads a passkey by its normalized credential id.
func (s *Store) GetPasskeyByCredentialID(ctx context.Context, credentialID string) (*model.Passkey, error) {
	var entity PasskeyEntity
	if err := s.DB.WithContext(ctx).Where("credential_id = ?", credentialID).First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	passkey := passkeyFromEntity(&entity)
	return &passkey, nil
}

// GetPasskeyByAccountAndCredentialID loads a passkey scoped to the account.
func (s *Store) GetPasskeyByAccountAndCredentialID(ctx context.Context, accountID, credentialID string) (*model.Passkey, error) {
	var entity PasskeyEntity
	if err := s.DB.WithContext(ctx).
		Where("account_id = ? AND credential_id = ?", accountID, credentialID).
		First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	passkey := passkeyFromEntity(&entity)
	return &passkey, nil
}

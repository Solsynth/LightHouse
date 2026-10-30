package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

// Connection helpers for the social-login surface (account_connections).

// connectionFromEntity maps a persisted connection row (including the
// registration marker) to the API model.
func connectionFromEntity(entity *ConnectionEntity) model.Connection {
	connection := model.Connection{
		Id:                 entity.ID.String(),
		Provider:           entity.Provider,
		ProvidedIdentifier: entity.ProvidedIdentifier,
		LastUsedAt:         timePtr(entity.LastUsedAt),
		IsPublic:           entity.IsPublic,
		AccountId:          entity.AccountID.String(),
		RegisteredAt:       timePtr(entity.RegisteredAt),
		CreatedAt:          timePtr(&entity.CreatedAt),
		UpdatedAt:          timePtr(&entity.UpdatedAt),
		DeletedAt:          deletedTime(entity.DeletedAt),
	}
	_ = decodeJSONValue(entity.Meta, &connection.Meta)
	if entity.AccessToken != nil {
		connection.AccessToken = *entity.AccessToken
	}
	if entity.RefreshToken != nil {
		connection.RefreshToken = *entity.RefreshToken
	}
	return connection
}

// GetConnectionWithAccount loads a connection joined with its account.
func (s *Store) GetConnectionWithAccount(ctx context.Context, provider, providedIdentifier string) (*model.Connection, *model.Account, error) {
	var entity ConnectionEntity
	if err := s.DB.WithContext(ctx).
		Where("LOWER(provider) = LOWER(?) AND provided_identifier = ?", provider, providedIdentifier).
		First(&entity).Error; err != nil {
		return nil, nil, mapNotFound(err)
	}
	// The join never filtered the account's soft-delete flag, so neither do we.
	var accountEntity AccountEntity
	if err := s.DB.WithContext(ctx).Unscoped().Where("id = ?", entity.AccountID).First(&accountEntity).Error; err != nil {
		return nil, nil, mapNotFound(err)
	}
	connection := connectionFromEntity(&entity)
	return &connection, accountFromEntity(&accountEntity), nil
}

// GetConnectionByProviderIdentifier loads a connection by provider+identifier.
func (s *Store) GetConnectionByProviderIdentifier(ctx context.Context, provider, providedIdentifier string) (*model.Connection, error) {
	var entity ConnectionEntity
	if err := s.DB.WithContext(ctx).
		Where("LOWER(provider) = LOWER(?) AND provided_identifier = ?", provider, providedIdentifier).
		First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	connection := connectionFromEntity(&entity)
	return &connection, nil
}

// GetConnectionByAccountAndProvider loads the account's newest connection for
// a provider (case-insensitive provider match).
func (s *Store) GetConnectionByAccountAndProvider(ctx context.Context, accountID, provider string) (*model.Connection, error) {
	var entity ConnectionEntity
	if err := s.DB.WithContext(ctx).
		Where("account_id = ? AND LOWER(provider) = LOWER(?)", accountID, provider).
		Order("created_at").
		First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	connection := connectionFromEntity(&entity)
	return &connection, nil
}

// InsertConnection creates a new connection row, unless one already exists for
// the same account+provider+identifier (idempotent: concurrent callbacks no-op
// instead of racing on the unique index). registeredAt is non-nil when this
// connection created the account (OIDC registration).
func (s *Store) InsertConnection(ctx context.Context, accountID, provider, providedIdentifier, accessToken, refreshToken string, meta map[string]any, registeredAt *time.Time, now time.Time) error {
	metaJSON, _ := json.Marshal(meta)
	// The conflict target is a partial unique index over the expression
	// (account_id, LOWER(provider), provided_identifier) WHERE deleted_at IS
	// NULL, which clause.OnConflict cannot express; keep the statement and
	// let the database arbitrate the race.
	return s.DB.WithContext(ctx).Exec(`INSERT INTO account_connections
		(id, provider, provided_identifier, meta, access_token, refresh_token, last_used_at, is_public, account_id, registered_at, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,false,?,?,?,?)
		ON CONFLICT (account_id, LOWER(provider), provided_identifier) WHERE deleted_at IS NULL DO NOTHING`,
		uuid.NewString(), provider, providedIdentifier, metaJSON, nullStr(accessToken), nullStr(refreshToken), now, accountID, registeredAt, now, now).Error
}

// UpsertConnection atomically updates an existing connection or inserts a new
// one — a single statement racing safely on the (account_id, LOWER(provider),
// provided_identifier) unique index, so concurrent OIDC callbacks can never
// create duplicate rows. registeredAt is only applied to a newly inserted row
// (an existing row was never the registration connection). Returns whether a
// row was created.
func (s *Store) UpsertConnection(ctx context.Context, accountID, provider, providedIdentifier, accessToken, refreshToken string, meta map[string]any, registeredAt *time.Time, now time.Time) (bool, error) {
	metaJSON, _ := json.Marshal(meta)
	var created bool
	// Partial-expression conflict target: see InsertConnection.
	err := s.DB.WithContext(ctx).Raw(`INSERT INTO account_connections
		(id, provider, provided_identifier, meta, access_token, refresh_token, last_used_at, is_public, account_id, registered_at, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,false,?,?,?,?)
		ON CONFLICT (account_id, LOWER(provider), provided_identifier) WHERE deleted_at IS NULL
		DO UPDATE SET last_used_at = EXCLUDED.last_used_at, meta = EXCLUDED.meta, updated_at = EXCLUDED.updated_at
		RETURNING (xmax = 0)`,
		uuid.NewString(), provider, providedIdentifier, metaJSON, nullStr(accessToken), nullStr(refreshToken), now, accountID, registeredAt, now, now).Scan(&created).Error
	if err != nil {
		return false, err
	}
	return created, nil
}

// TouchConnectionTokens atomically updates or inserts a connection with fresh
// tokens — a single statement racing safely on the unique index (see
// UpsertConnection). Returns whether a row was created.
func (s *Store) TouchConnectionTokens(ctx context.Context, accountID, provider, providedIdentifier, accessToken, refreshToken string, meta map[string]any, registeredAt *time.Time, now time.Time) (bool, error) {
	metaJSON, _ := json.Marshal(meta)
	var created bool
	// Partial-expression conflict target: see InsertConnection.
	err := s.DB.WithContext(ctx).Raw(`INSERT INTO account_connections
		(id, provider, provided_identifier, meta, access_token, refresh_token, last_used_at, is_public, account_id, registered_at, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,false,?,?,?,?)
		ON CONFLICT (account_id, LOWER(provider), provided_identifier) WHERE deleted_at IS NULL
		DO UPDATE SET access_token = COALESCE(EXCLUDED.access_token, account_connections.access_token),
			refresh_token = COALESCE(EXCLUDED.refresh_token, account_connections.refresh_token),
			last_used_at = EXCLUDED.last_used_at, meta = EXCLUDED.meta, updated_at = EXCLUDED.updated_at
		RETURNING (xmax = 0)`,
		uuid.NewString(), provider, providedIdentifier, metaJSON, nullStr(accessToken), nullStr(refreshToken), now, accountID, registeredAt, now, now).Scan(&created).Error
	if err != nil {
		return false, err
	}
	return created, nil
}

// CreateOidcSession inserts an Oidc-typed session (type=2).
func (s *Store) CreateOidcSession(ctx context.Context, accountID string, clientID, parentSessionID *uuid.UUID, expiredAt, now time.Time) (*model.AuthSession, error) {
	parsedAccountID, err := ParseUUID(accountID)
	if err != nil {
		return nil, err
	}
	// audiences/scopes are jsonb NOT NULL: an empty JSON array, never NULL.
	sessionID := uuid.New()
	if err := s.DB.WithContext(ctx).Create(&AuthSessionEntity{
		ID:              sessionID,
		CreatedAt:       now,
		UpdatedAt:       now,
		AccountID:       parsedAccountID,
		AppID:           clientID,
		ClientID:        clientID,
		ParentSessionID: parentSessionID,
		Audiences:       datatypes.JSON([]byte("[]")),
		Scopes:          datatypes.JSON([]byte("[]")),
		Epoch:           0,
		ExpiredAt:       &expiredAt,
		LastGrantedAt:   &now,
		Type:            int(model.SessionTypeOidc),
	}).Error; err != nil {
		return nil, err
	}
	return &model.AuthSession{
		Id:              sessionID.String(),
		Type:            model.SessionTypeOidc,
		AccountId:       accountID,
		CreatedAt:       model.NewTime(now),
		LastGrantedAt:   model.NewTime(now),
		ExpiredAt:       model.NewTime(expiredAt),
		Scopes:          []string{},
		Audiences:       []string{},
		ParentSessionId: uuidPtrStr(parentSessionID),
		AppId:           uuidPtrStr(clientID),
	}, nil
}

// CreateAccountFromSocial creates a session-less account with a primary email
// contact and default-group membership, mirroring
// AccountService.CreateAccount(name, nick, email, null, isEmailVerified).
func (s *Store) CreateAccountFromSocial(ctx context.Context, name, nick, email string, emailVerified bool, now time.Time) (*model.Account, error) {
	used, err := s.CheckEmailUsed(ctx, email)
	if err != nil {
		return nil, err
	}
	if used {
		return nil, errors.New("Account email has already been used.")
	}

	var stored AccountEntity
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		created := &AccountEntity{
			ID:         uuid.New(),
			EntityBase: EntityBase{CreatedAt: now, UpdatedAt: now},
			Language:   "en-US",
			Name:       name,
			Nick:       nick,
			Region:     "en",
		}
		if err := tx.Create(created).Error; err != nil {
			return err
		}
		// automated_id is owned by the database (the INSERT returned it).
		if err := tx.Where("id = ?", created.ID).First(&stored).Error; err != nil {
			return err
		}

		var verifiedAt *time.Time
		if emailVerified {
			verifiedAt = &now
		}
		if err := tx.Create(&ContactEntity{
			ID:         uuid.New(),
			EntityBase: EntityBase{CreatedAt: now, UpdatedAt: now},
			AccountID:  created.ID,
			Content:    email,
			IsPrimary:  true,
			IsPublic:   false,
			Type:       int(model.ContactTypeEmail),
			VerifiedAt: verifiedAt,
		}).Error; err != nil {
			return err
		}

		// Enroll in the `default` permission group. A deployment without that
		// group enrolls nobody, exactly like the INSERT ... SELECT it replaces.
		var group PermissionGroupEntity
		if err := tx.Where(`"key" = ?`, "default").First(&group).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&PermissionGroupMemberEntity{
			GroupID:    group.ID,
			Actor:      created.ID.String(),
			EntityBase: EntityBase{CreatedAt: now, UpdatedAt: now},
		}).Error
	})
	if err != nil {
		return nil, err
	}
	return accountFromEntity(&stored), nil
}

func nullStr(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}

// Connection controller helpers (ConnectionController.cs port).

// ListConnections lists an account's connections WITHOUT a soft-delete filter
// (C# GetConnections has none).
func (s *Store) ListConnections(ctx context.Context, accountID string) ([]model.Connection, error) {
	var entities []ConnectionEntity
	if err := s.DB.WithContext(ctx).Unscoped().Where("account_id = ?", accountID).Find(&entities).Error; err != nil {
		return nil, err
	}
	var connections []model.Connection
	for i := range entities {
		connections = append(connections, connectionFromEntity(&entities[i]))
	}
	return connections, nil
}

// GetConnectionByID loads a connection scoped to the account.
func (s *Store) GetConnectionByID(ctx context.Context, accountID string, id uuid.UUID) (*model.Connection, error) {
	var entity ConnectionEntity
	if err := s.DB.WithContext(ctx).Unscoped().Where("id = ? AND account_id = ?", id, accountID).First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	connection := connectionFromEntity(&entity)
	return &connection, nil
}

// UpdateConnection applies the visibility toggle (is_public).
func (s *Store) UpdateConnection(ctx context.Context, c *model.Connection) error {
	return s.DB.WithContext(ctx).Unscoped().Model(&ConnectionEntity{}).
		Where("id = ?", c.Id).
		Updates(map[string]any{"is_public": c.IsPublic, "updated_at": gorm.Expr("now()")}).Error
}

// DeleteConnectionRow hard-deletes a connection row.
func (s *Store) DeleteConnectionRow(ctx context.Context, id uuid.UUID) error {
	return s.DB.WithContext(ctx).Unscoped().Where("id = ?", id).Delete(&ConnectionEntity{}).Error
}

// SetConnectionVisibility toggles a connection's public flag.
func (s *Store) SetConnectionVisibility(ctx context.Context, id uuid.UUID, isPublic bool) error {
	return s.DB.WithContext(ctx).Unscoped().Model(&ConnectionEntity{}).
		Where("id = ?", id).
		Updates(map[string]any{"is_public": isPublic, "updated_at": gorm.Expr("now()")}).Error
}

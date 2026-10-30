package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/stargate/internal/model"
)

// OAuth app storage for the app.solian.id.oauthapps module. This is the
// OAuth/OIDC client registry Stargate owns; it replaces the Develop lookup
// for registrations made here while leaving the Develop service untouched.

// CreateOAuthApp inserts an app (id and timestamps must be set).
func (s *Store) CreateOAuthApp(ctx context.Context, app *model.OAuthApp) error {
	entity, err := oauthAppToEntity(app)
	if err != nil {
		return err
	}
	return s.DB.WithContext(ctx).Create(entity).Error
}

// UpdateOAuthApp replaces an app's mutable fields. The column map is
// explicit, so it must carry every writable column: picture/background are
// listed so a PATCH can both set and clear the app's art.
func (s *Store) UpdateOAuthApp(ctx context.Context, app *model.OAuthApp) error {
	entity, err := oauthAppToEntity(app)
	if err != nil {
		return err
	}
	return s.DB.WithContext(ctx).Model(&OAuthAppEntity{}).
		Where("id = ?", entity.ID).
		Updates(map[string]any{
			"slug":         entity.Slug,
			"name":         entity.Name,
			"description":  entity.Description,
			"status":       entity.Status,
			"oauth_config": entity.OauthConfig,
			"links":        entity.Links,
			"picture":      entity.Picture,
			"background":   entity.Background,
			"updated_at":   entity.UpdatedAt,
		}).Error
}

// DeleteOAuthApp soft-deletes an app.
func (s *Store) DeleteOAuthApp(ctx context.Context, id string) error {
	return s.DB.WithContext(ctx).Delete(&OAuthAppEntity{}, "id = ?", id).Error
}

// GetOAuthAppByID loads an app by its stable ID.
func (s *Store) GetOAuthAppByID(ctx context.Context, id string) (*model.OAuthApp, error) {
	var entity OAuthAppEntity
	err := s.DB.WithContext(ctx).Where("id = ?", id).Take(&entity).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return oauthAppFromEntity(&entity), nil
}

// GetOAuthAppByIdentifier loads an app by ID when the value is a UUID and
// otherwise by slug (mirrors findClientByIdentifier).
func (s *Store) GetOAuthAppByIdentifier(ctx context.Context, identifier string) (*model.OAuthApp, error) {
	if id, err := uuid.Parse(identifier); err == nil {
		return s.GetOAuthAppByID(ctx, id.String())
	}
	return s.GetOAuthAppBySlug(ctx, identifier)
}

// GetOAuthAppBySlug loads an app by its human-readable slug.
func (s *Store) GetOAuthAppBySlug(ctx context.Context, slug string) (*model.OAuthApp, error) {
	var entity OAuthAppEntity
	err := s.DB.WithContext(ctx).Where("slug = ?", slug).Take(&entity).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return oauthAppFromEntity(&entity), nil
}

// ListOAuthApps returns the apps the caller can manage: individually owned
// when accountID is set, project-linked when projectID is set. A nil filter
// on both sides returns every app (the caller has already been authorized).
func (s *Store) ListOAuthApps(ctx context.Context, accountID, projectID *string) ([]*model.OAuthApp, error) {
	query := s.DB.WithContext(ctx).Model(&OAuthAppEntity{})
	if accountID != nil {
		query = query.Where("account_id = ?", *accountID)
	}
	if projectID != nil {
		query = query.Where("project_id = ?", *projectID)
	}
	var entities []OAuthAppEntity
	if err := query.Order("created_at DESC").Find(&entities).Error; err != nil {
		return nil, err
	}
	apps := make([]*model.OAuthApp, 0, len(entities))
	for i := range entities {
		apps = append(apps, oauthAppFromEntity(&entities[i]))
	}
	return apps, nil
}

// CountOAuthAppsWithSlug reports whether any app (including soft-deleted
// ones) already uses the slug, so a slug can never be silently reused.
func (s *Store) CountOAuthAppsWithSlug(ctx context.Context, slug string) (int64, error) {
	var count int64
	err := s.DB.WithContext(ctx).Model(&OAuthAppEntity{}).
		Unscoped().
		Where("slug = ?", slug).
		Count(&count).Error
	return count, err
}

// ── Secrets ────────────────────────────────────────────────────────────────

// CreateOAuthAppSecret inserts a secret. The caller supplies the ID so the
// value returned to the client is the one that exists in the table.
func (s *Store) CreateOAuthAppSecret(ctx context.Context, secret *model.OAuthAppSecret) error {
	appID, err := ParseUUID(secret.AppId)
	if err != nil {
		return err
	}
	id, err := ParseUUID(secret.Id)
	if err != nil {
		return err
	}
	entity := OAuthAppSecretEntity{
		ID:          id,
		EntityBase:  EntityBase{CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()},
		Secret:      secret.Secret,
		Description: secret.Description,
		IsOidc:      secret.IsOidc,
		ExpiredAt:   timeValue(secret.ExpiredAt),
		AppID:       appID,
	}
	return s.DB.WithContext(ctx).Create(&entity).Error
}

// ListOAuthAppSecrets returns an app's secrets, newest first, with the
// plaintext stripped.
func (s *Store) ListOAuthAppSecrets(ctx context.Context, appID string) ([]model.OAuthAppSecret, error) {
	var entities []OAuthAppSecretEntity
	if err := s.DB.WithContext(ctx).
		Where("app_id = ?", appID).
		Order("created_at DESC").
		Find(&entities).Error; err != nil {
		return nil, err
	}
	secrets := make([]model.OAuthAppSecret, 0, len(entities))
	for i := range entities {
		secrets = append(secrets, *oauthAppSecretFromEntity(&entities[i]))
	}
	return secrets, nil
}

// GetOAuthAppSecret loads one secret of an app. The returned value carries
// the stored plaintext so a verifier can compare it.
func (s *Store) GetOAuthAppSecret(ctx context.Context, appID, secretID string) (*model.OAuthAppSecret, error) {
	var entity OAuthAppSecretEntity
	err := s.DB.WithContext(ctx).
		Where("app_id = ? AND id = ?", appID, secretID).
		Take(&entity).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	secret := oauthAppSecretFromEntity(&entity)
	secret.Secret = entity.Secret
	return secret, nil
}

// FindValidOAuthAppSecret returns the non-expired secret matching the
// presented value for an app, or nil when nothing matches. isOidc=false
// restricts the lookup to API keys.
func (s *Store) FindValidOAuthAppSecret(ctx context.Context, appID, value string, isOidc bool) (*model.OAuthAppSecret, error) {
	var entities []OAuthAppSecretEntity
	now := time.Now().UTC()
	err := s.DB.WithContext(ctx).
		Where("app_id = ? AND is_oidc = ?", appID, isOidc).
		Where("expired_at IS NULL OR expired_at > ?", now).
		Order("created_at DESC").
		Find(&entities).Error
	if err != nil {
		return nil, err
	}
	for i := range entities {
		if entities[i].Secret == value {
			secret := oauthAppSecretFromEntity(&entities[i])
			secret.Secret = entities[i].Secret
			return secret, nil
		}
	}
	return nil, nil
}

// RotateOAuthAppSecret replaces a secret's value and description, keeping
// its id, type and expiry.
func (s *Store) RotateOAuthAppSecret(ctx context.Context, appID, secretID, value, description string) (*model.OAuthAppSecret, error) {
	var entity OAuthAppSecretEntity
	err := s.DB.WithContext(ctx).
		Where("app_id = ? AND id = ?", appID, secretID).
		Take(&entity).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if err := s.DB.WithContext(ctx).Model(&entity).Updates(map[string]any{
		"secret": value, "description": description, "updated_at": now,
	}).Error; err != nil {
		return nil, err
	}
	entity.Secret = value
	entity.Description = description
	entity.UpdatedAt = now
	secret := oauthAppSecretFromEntity(&entity)
	secret.Secret = value
	return secret, nil
}

// DeleteOAuthAppSecret soft-deletes a secret of an app.
func (s *Store) DeleteOAuthAppSecret(ctx context.Context, appID, secretID string) error {
	return s.DB.WithContext(ctx).
		Where("app_id = ? AND id = ?", appID, secretID).
		Delete(&OAuthAppSecretEntity{}).Error
}

// decodeFileReference maps a nullable jsonb art column onto the model. A nil
// column is SQL NULL, and the JSON literal null decodes to a nil pointer;
// both mean the app has no art.
func decodeFileReference(raw *datatypes.JSON) *model.SnCloudFileReferenceObject {
	if raw == nil || len(*raw) == 0 {
		return nil
	}
	var ref *model.SnCloudFileReferenceObject
	if err := decodeJSON(raw, &ref); err != nil {
		return nil
	}
	return ref
}

// ── Mappers ────────────────────────────────────────────────────────────────

func oauthAppFromEntity(entity *OAuthAppEntity) *model.OAuthApp {
	return &model.OAuthApp{
		Id:          entity.ID.String(),
		Slug:        entity.Slug,
		Name:        entity.Name,
		Description: entity.Description,
		Status:      entity.Status,
		ProjectId:   uuidPtrStr(entity.ProjectID),
		AccountId:   uuidPtrStr(entity.AccountID),
		OauthConfig: model.OAuthAppOauthConfig{
			ClientUri:              entity.OauthConfig.ClientUri,
			RedirectUris:           entity.OauthConfig.RedirectUris,
			PostLogoutRedirectUris: entity.OauthConfig.PostLogoutRedirectUris,
			AllowedScopes:          entity.OauthConfig.AllowedScopes,
			AllowedGrantTypes:      entity.OauthConfig.AllowedGrantTypes,
			RequirePkce:            entity.OauthConfig.RequirePkce,
			AllowOfflineAccess:     entity.OauthConfig.AllowOfflineAccess,
			IsPublicClient:         entity.OauthConfig.IsPublicClient,
		},
		Links: model.OAuthAppLinks{
			HomePage:       entity.Links.HomePage,
			PrivacyPolicy:  entity.Links.PrivacyPolicy,
			TermsOfService: entity.Links.TermsOfService,
		},
		// The art columns are nullable jsonb: nil is SQL NULL, so a cleared
		// reference decodes to a nil Picture/Background.
		Picture:    decodeFileReference(entity.Picture),
		Background: decodeFileReference(entity.Background),
		CreatedAt:  timePtr(&entity.CreatedAt),
		UpdatedAt:  timePtr(&entity.UpdatedAt),
		DeletedAt:  deletedTime(entity.DeletedAt),
	}
}

func oauthAppToEntity(app *model.OAuthApp) (*OAuthAppEntity, error) {
	id, err := ParseUUID(app.Id)
	if err != nil {
		return nil, err
	}
	entity := &OAuthAppEntity{
		ID: id, Slug: strings.TrimSpace(app.Slug), Name: strings.TrimSpace(app.Name),
		Description: app.Description, Status: app.Status,
		OauthConfig: OAuthAppOauthConfigEntity{
			ClientUri:              app.OauthConfig.ClientUri,
			RedirectUris:           app.OauthConfig.RedirectUris,
			PostLogoutRedirectUris: app.OauthConfig.PostLogoutRedirectUris,
			AllowedScopes:          app.OauthConfig.AllowedScopes,
			AllowedGrantTypes:      app.OauthConfig.AllowedGrantTypes,
			RequirePkce:            app.OauthConfig.RequirePkce,
			AllowOfflineAccess:     app.OauthConfig.AllowOfflineAccess,
			IsPublicClient:         app.OauthConfig.IsPublicClient,
		},
		Links: OAuthAppLinksEntity{
			HomePage:       app.Links.HomePage,
			PrivacyPolicy:  app.Links.PrivacyPolicy,
			TermsOfService: app.Links.TermsOfService,
		},
	}
	// An absent reference must become SQL NULL, not the jsonb literal null,
	// so a request that clears the art reads back as a nil pointer. The check
	// is explicit because a typed nil pointer inside `any` is not == nil and
	// json.Marshal would emit the literal null instead.
	if app.Picture != nil {
		encoded, err := encodeJSON(app.Picture)
		if err != nil {
			return nil, err
		}
		entity.Picture = &encoded
	}
	if app.Background != nil {
		encoded, err := encodeJSON(app.Background)
		if err != nil {
			return nil, err
		}
		entity.Background = &encoded
	}
	now := time.Now().UTC()
	entity.EntityBase.CreatedAt = now
	entity.EntityBase.UpdatedAt = now
	if app.ProjectId != nil && *app.ProjectId != "" {
		projectID, err := ParseUUID(*app.ProjectId)
		if err != nil {
			return nil, err
		}
		entity.ProjectID = &projectID
	}
	if app.AccountId != nil && *app.AccountId != "" {
		accountID, err := ParseUUID(*app.AccountId)
		if err != nil {
			return nil, err
		}
		entity.AccountID = &accountID
	}
	return entity, nil
}

func oauthAppSecretFromEntity(entity *OAuthAppSecretEntity) *model.OAuthAppSecret {
	return &model.OAuthAppSecret{
		Id:          entity.ID.String(),
		AppId:       entity.AppID.String(),
		Description: entity.Description,
		ExpiredAt:   timePtr(entity.ExpiredAt),
		IsOidc:      entity.IsOidc,
		CreatedAt:   timePtr(&entity.CreatedAt),
		UpdatedAt:   timePtr(&entity.UpdatedAt),
	}
}

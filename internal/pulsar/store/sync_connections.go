package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
)

// GetSyncConnection returns one account's link to a provider.
func (s *Store) GetSyncConnection(ctx context.Context, accountID uuid.UUID, provider string) (*model.ExternalSyncConnection, error) {
	var conn model.ExternalSyncConnection
	err := s.DB.WithContext(ctx).
		Where("account_id = ? AND LOWER(provider) = LOWER(?)", accountID, provider).
		First(&conn).Error
	if err != nil {
		return nil, err
	}
	return &conn, nil
}

// UpsertSyncConnection creates or refreshes a link. A reconnect replaces the
// tokens and remote identity in place so the account does not accumulate dead
// rows; an empty incoming conflict policy keeps the account's own choice.
func (s *Store) UpsertSyncConnection(ctx context.Context, conn *model.ExternalSyncConnection) error {
	if conn == nil {
		return errors.New("nil sync connection")
	}
	if conn.ID == uuid.Nil {
		conn.ID = uuid.New()
	}
	now := time.Now().UTC()
	conn.CreatedAt = now
	conn.UpdatedAt = now

	return s.DB.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "account_id"}, {Name: "provider"}},
		DoUpdates: clause.Assignments(map[string]any{
			"remote_user_id":   conn.RemoteUserID,
			"remote_username":  conn.RemoteUsername,
			"access_token":     conn.AccessToken,
			"refresh_token":    conn.RefreshToken,
			"token_expires_at": conn.TokenExpiresAt,
			"sync_enabled":     conn.SyncEnabled,
			"conflict_policy":  gorm.Expr("COALESCE(NULLIF(EXCLUDED.conflict_policy, ''), sync_connections.conflict_policy)"),
			"meta":             conn.Meta,
			"updated_at":       now,
		}),
	}).Create(conn).Error
}

// UpdateSyncConnectionTokens persists refreshed credentials and the new
// expiry without touching the account's sync preferences.
func (s *Store) UpdateSyncConnectionTokens(ctx context.Context, conn *model.ExternalSyncConnection) error {
	now := time.Now().UTC()
	return s.DB.WithContext(ctx).Model(&model.ExternalSyncConnection{}).
		Where("id = ?", conn.ID).
		Updates(map[string]any{
			"access_token":     conn.AccessToken,
			"refresh_token":    conn.RefreshToken,
			"token_expires_at": conn.TokenExpiresAt,
			"updated_at":       now,
		}).Error
}

// UpdateSyncConnectionResult records the outcome of one reconciliation.
func (s *Store) UpdateSyncConnectionResult(ctx context.Context, id uuid.UUID, syncedAt *time.Time, scanErr error) error {
	updates := map[string]any{"updated_at": time.Now().UTC()}
	if syncedAt != nil {
		updates["last_synced_at"] = syncedAt
	}
	if scanErr != nil {
		message := strings.TrimSpace(scanErr.Error())
		if len(message) > 4000 {
			message = message[:4000]
		}
		updates["last_error"] = message
	} else {
		updates["last_error"] = nil
	}
	return s.DB.WithContext(ctx).Model(&model.ExternalSyncConnection{}).
		Where("id = ?", id).Updates(updates).Error
}

// UpdateSyncConnectionSettings changes the per-account sync switches.
func (s *Store) UpdateSyncConnectionSettings(ctx context.Context, accountID uuid.UUID, provider string, syncEnabled *bool, conflictPolicy *string) error {
	updates := map[string]any{"updated_at": time.Now().UTC()}
	if syncEnabled != nil {
		updates["sync_enabled"] = *syncEnabled
	}
	if conflictPolicy != nil {
		updates["conflict_policy"] = *conflictPolicy
	}
	res := s.DB.WithContext(ctx).Model(&model.ExternalSyncConnection{}).
		Where("account_id = ? AND LOWER(provider) = LOWER(?)", accountID, provider).
		Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// DeleteSyncConnection unlinks an account from a provider.
func (s *Store) DeleteSyncConnection(ctx context.Context, accountID uuid.UUID, provider string) error {
	res := s.DB.WithContext(ctx).
		Where("account_id = ? AND LOWER(provider) = LOWER(?)", accountID, provider).
		Delete(&model.ExternalSyncConnection{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// ListSyncConnections returns every link to a provider, newest first. The
// background reconciler calls it with enabledOnly to skip paused accounts.
func (s *Store) ListSyncConnections(ctx context.Context, provider string, enabledOnly bool) ([]model.ExternalSyncConnection, error) {
	query := s.DB.WithContext(ctx).
		Where("LOWER(provider) = LOWER(?)", provider)
	if enabledOnly {
		query = query.Where("sync_enabled = ?", true)
	}
	var conns []model.ExternalSyncConnection
	if err := query.Order("created_at ASC").Find(&conns).Error; err != nil {
		return nil, err
	}
	return conns, nil
}

// CreateOAuthState stores the single-use state a callback must echo.
func (s *Store) CreateOAuthState(ctx context.Context, state *model.ExternalOAuthState) error {
	if state == nil {
		return errors.New("nil oauth state")
	}
	state.CreatedAt = time.Now().UTC()
	return s.DB.WithContext(ctx).Create(state).Error
}

// ConsumeOAuthState deletes and returns a live state row atomically. A
// missing or expired row is the same answer: the callback is not allowed to
// proceed, and the row is gone so it cannot be replayed.
func (s *Store) ConsumeOAuthState(ctx context.Context, state, provider string) (*model.ExternalOAuthState, error) {
	var row model.ExternalOAuthState
	res := s.DB.WithContext(ctx).Raw(
		`DELETE FROM sync_oauth_states
		 WHERE state = ? AND LOWER(provider) = LOWER(?) AND expires_at > ?
		 RETURNING state, provider, account_id, created_at, expires_at`,
		state, provider, time.Now().UTC(),
	).Scan(&row)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return &row, nil
}

// PurgeOAuthStates removes expired callback states.
func (s *Store) PurgeOAuthStates(ctx context.Context, before time.Time) error {
	return s.DB.WithContext(ctx).
		Where("expires_at < ?", before).
		Delete(&model.ExternalOAuthState{}).Error
}

// EnsureSyncCollection returns the account's mirror of one provider list,
// creating it on first use. The unique key is (account, provider, scope), so a
// concurrent first sync cannot leave two mirrors behind: the loser re-reads
// the winner's row.
func (s *Store) EnsureSyncCollection(ctx context.Context, accountID uuid.UUID, domain, provider, scope, name string) (*model.Collection, error) {
	db := s.DB.WithContext(ctx)
	var existing model.Collection
	err := db.Where("account_id = ? AND sync_provider = ? AND sync_scope = ?", accountID, provider, scope).
		First(&existing).Error
	if err == nil {
		return &existing, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	coll := &model.Collection{
		AccountID:    accountID,
		Domain:       domain,
		Kind:         model.KindWatchlist,
		Name:         name,
		IsPublic:     false,
		SyncProvider: &provider,
		SyncScope:    &scope,
	}
	if err := s.CreateCollection(ctx, coll); err != nil {
		if reread := db.Where("account_id = ? AND sync_provider = ? AND sync_scope = ?", accountID, provider, scope).
			First(&existing).Error; reread == nil {
			return &existing, nil
		}
		return nil, err
	}
	return coll, nil
}

// ListSyncCollections returns every mirror an account owns for a provider.
func (s *Store) ListSyncCollections(ctx context.Context, accountID uuid.UUID, provider string) ([]model.Collection, error) {
	var collections []model.Collection
	err := s.DB.WithContext(ctx).
		Where("account_id = ? AND sync_provider = ?", accountID, provider).
		Order("domain ASC").
		Find(&collections).Error
	if err != nil {
		return nil, err
	}
	return collections, nil
}

// GetCollectionItemByRemoteID finds one mirrored item by its provider id.
func (s *Store) GetCollectionItemByRemoteID(ctx context.Context, collectionID uuid.UUID, remoteID string) (*model.CollectionItem, error) {
	var item model.CollectionItem
	err := s.DB.WithContext(ctx).
		Where("collection_id = ? AND sync_remote_id = ?", collectionID, remoteID).
		First(&item).Error
	if err != nil {
		return nil, err
	}
	return &item, nil
}

// SaveCollectionItemSync writes the fields a reconciliation moved: the remote
// id, the two agreement fingerprints, the sync clock and the mirrored values.
func (s *Store) SaveCollectionItemSync(ctx context.Context, item *model.CollectionItem) error {
	now := time.Now().UTC()
	item.UpdatedAt = now
	return s.DB.WithContext(ctx).Model(&model.CollectionItem{}).
		Where("id = ?", item.ID).
		Updates(map[string]any{
			"catalog_id":              item.CatalogID,
			"note":                    item.Note,
			"rating":                  item.Rating,
			"status":                  item.Status,
			"progress":                item.Progress,
			"volume_progress":         item.VolumeProgress,
			"sync_remote_id":          item.SyncRemoteID,
			"sync_local_fingerprint":  item.SyncLocalFingerprint,
			"sync_remote_fingerprint": item.SyncRemoteFingerprint,
			"synced_at":               item.SyncedAt,
			"updated_at":              now,
		}).Error
}

// DeleteCollectionItemByID removes one mirrored row.
func (s *Store) DeleteCollectionItemByID(ctx context.Context, collectionID, itemID uuid.UUID) error {
	res := s.DB.WithContext(ctx).
		Where("collection_id = ? AND id = ?", collectionID, itemID).
		Delete(&model.CollectionItem{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// FindCatalogItemForBangumi finds the cached catalog row for one Bangumi
// subject in a domain. Bangumi metadata is stored under a per-domain provider
// name (bangumi, bangumi-book, ...), while the canonical URN and slug are
// shared, so all three handles are probed before creating a duplicate.
func (s *Store) FindCatalogItemForBangumi(ctx context.Context, domain, subjectID string) (*model.CatalogItem, error) {
	var item model.CatalogItem
	urn := "pulsar:" + domain + ":bangumi:" + subjectID
	slug := "bangumi-" + subjectID
	err := s.DB.WithContext(ctx).
		Where("urn = ? OR (domain = ? AND slug = ?) OR (domain = ? AND provider_key = ? AND provider LIKE 'bangumi%')",
			urn, domain, slug, domain, subjectID).
		First(&item).Error
	if err != nil {
		return nil, err
	}
	return &item, nil
}

// CountCollectionItems reports how many rows a collection holds.
func (s *Store) CountCollectionItems(ctx context.Context, collectionID uuid.UUID) (int64, error) {
	var count int64
	err := s.DB.WithContext(ctx).Model(&model.CollectionItem{}).
		Where("collection_id = ?", collectionID).Count(&count).Error
	return count, err
}

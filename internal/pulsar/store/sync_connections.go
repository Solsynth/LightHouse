package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
)

// GetSyncConnection returns one account's local sync preferences for a
// provider. The credential itself lives in Stargate.
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

// EnsureSyncConnection creates the local row for a newly linked account, or
// refreshes the remote identity on an existing one. Local preferences —
// background sync and conflict policy — are never overwritten by a re-link.
func (s *Store) EnsureSyncConnection(ctx context.Context, conn *model.ExternalSyncConnection) (bool, error) {
	if conn == nil {
		return false, errors.New("nil sync connection")
	}
	db := s.DB.WithContext(ctx)
	var existing model.ExternalSyncConnection
	err := db.Where("account_id = ? AND LOWER(provider) = LOWER(?)", conn.AccountID, conn.Provider).
		First(&existing).Error
	if err == nil {
		updates := map[string]any{
			"remote_user_id":  conn.RemoteUserID,
			"remote_username": conn.RemoteUsername,
			"meta":            conn.Meta,
			"updated_at":      time.Now().UTC(),
		}
		if conn.RemoteUserID == "" {
			delete(updates, "remote_user_id")
		}
		return false, db.Model(&model.ExternalSyncConnection{}).
			Where("id = ?", existing.ID).Updates(updates).Error
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return false, err
	}

	now := time.Now().UTC()
	conn.ID = uuid.New()
	conn.CreatedAt = now
	conn.UpdatedAt = now
	conn.SyncEnabled = true
	if err := db.Create(conn).Error; err != nil {
		// A concurrent first read may have won the unique key; re-read it.
		if reread := db.Where("account_id = ? AND LOWER(provider) = LOWER(?)", conn.AccountID, conn.Provider).
			First(&existing).Error; reread == nil {
			return false, nil
		}
		return false, err
	}
	return true, nil
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

// ListSyncConnections returns every local sync preference row for a provider,
// oldest first. The background reconciler uses enabledOnly to skip paused
// accounts.
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

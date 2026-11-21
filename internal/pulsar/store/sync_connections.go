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

// WatchlistTarget names the list a provider's mirrored items belong in.
type WatchlistTarget struct {
	AccountID uuid.UUID
	Domain    string
	Provider  string
	Scope     string
	// Name is used when no watchlist exists yet and Create is set.
	Name string
	// LegacyName is the dedicated mirror name earlier Pulsar versions created.
	// A collection still carrying that name and a provider tag is renamed to
	// Name when it becomes the target.
	LegacyName string
	// Create says whether a missing watchlist should be made. A provider list
	// with no rows passes false so syncing does not leave empty lists behind.
	Create bool
}

// EnsureWatchlistCollection returns the account's watchlist for one domain and
// makes it the target of a provider list. It folds the separate mirror
// collections older Pulsar versions created into that watchlist, so a Bangumi
// status and a locally tracked work share one list instead of appearing twice.
//
// The (account, provider, scope) tag lets the connection view find the list
// again even after the account renames it, and keeps a later watchlist in the
// same domain from stealing the target. A concurrent first sync may race to
// create the list, but only one row can hold the tag.
func (s *Store) EnsureWatchlistCollection(ctx context.Context, target WatchlistTarget) (*model.Collection, error) {
	var result *model.Collection
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var watchlist model.Collection
		watchlistErr := tx.
			Where("account_id = ? AND domain = ? AND kind = ? AND sync_provider IS NULL",
				target.AccountID, target.Domain, model.KindWatchlist).
			Order(clause.OrderBy{Expression: clause.Expr{
				SQL:  "CASE WHEN name = ? THEN 0 ELSE 1 END ASC, created_at ASC",
				Vars: []any{target.Name},
			}}).
			First(&watchlist).Error
		if watchlistErr != nil && !errors.Is(watchlistErr, gorm.ErrRecordNotFound) {
			return watchlistErr
		}

		var tagged model.Collection
		taggedErr := tx.
			Where("account_id = ? AND sync_provider = ? AND sync_scope = ?", target.AccountID, target.Provider, target.Scope).
			First(&tagged).Error
		if taggedErr != nil && !errors.Is(taggedErr, gorm.ErrRecordNotFound) {
			return taggedErr
		}

		switch {
		case taggedErr == nil && tagged.Name != target.LegacyName:
			// This list has already adopted the mirror. Keep it even if the
			// account started another watchlist in the meantime.
			watchlist = tagged
		case watchlistErr == nil:
			// The account owns a watchlist for this domain. If an older sync
			// still has a mirror, move its rows across and retire it.
			if taggedErr == nil && tagged.ID != watchlist.ID {
				if err := mergeCollectionInto(tx, tagged.ID, watchlist.ID); err != nil {
					return err
				}
				if err := tx.Where("id = ?", tagged.ID).Delete(&model.Collection{}).Error; err != nil {
					return err
				}
			}
		case taggedErr == nil:
			// No default list yet: the old mirror becomes it.
			watchlist = tagged
			if err := renameLegacyWatchlist(tx, &watchlist, target); err != nil {
				return err
			}
		default:
			if !target.Create {
				return nil
			}
			now := time.Now().UTC()
			watchlist = model.Collection{
				ID:        uuid.New(),
				AccountID: target.AccountID,
				Domain:    target.Domain,
				Kind:      model.KindWatchlist,
				Name:      target.Name,
				IsPublic:  false,
				CreatedAt: now,
				UpdatedAt: now,
			}
			if err := tx.Create(&watchlist).Error; err != nil {
				return err
			}
		}

		provider, scope := target.Provider, target.Scope
		if watchlist.SyncProvider == nil || *watchlist.SyncProvider != provider ||
			watchlist.SyncScope == nil || *watchlist.SyncScope != scope {
			// A retired mirror keeps its row for history, and a soft-deleted
			// collection still occupies the unique key on SQLite. Release the
			// tag from every other row before claiming it for the watchlist.
			if err := tx.Unscoped().Model(&model.Collection{}).
				Where("account_id = ? AND sync_provider = ? AND sync_scope = ? AND id <> ?",
					target.AccountID, provider, scope, watchlist.ID).
				Updates(map[string]any{"sync_provider": nil, "sync_scope": nil}).Error; err != nil {
				return err
			}
			if err := tx.Model(&model.Collection{}).Where("id = ?", watchlist.ID).Updates(map[string]any{
				"sync_provider": provider,
				"sync_scope":    scope,
				"updated_at":    time.Now().UTC(),
			}).Error; err != nil {
				return err
			}
			watchlist.SyncProvider = &provider
			watchlist.SyncScope = &scope
		}
		result = &watchlist
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// renameLegacyWatchlist gives an adopted mirror the same name a client would
// have chosen for the domain's default watchlist. A name the account set itself
// is left alone.
func renameLegacyWatchlist(tx *gorm.DB, coll *model.Collection, target WatchlistTarget) error {
	if coll.SyncProvider == nil || strings.TrimSpace(target.LegacyName) == "" || coll.Name != target.LegacyName {
		return nil
	}
	if err := tx.Model(&model.Collection{}).Where("id = ?", coll.ID).Update("name", target.Name).Error; err != nil {
		return err
	}
	coll.Name = target.Name
	return nil
}

// mergeCollectionInto moves every row of a retired mirror into the account's
// watchlist. A row for the same catalog or the same remote subject is folded
// into the existing entry rather than duplicated: values the watchlist already
// has win, while the link and fingerprints carry over so later Bangumi edits
// still reconcile.
func mergeCollectionInto(tx *gorm.DB, sourceID, targetID uuid.UUID) error {
	var targetItems []model.CollectionItem
	if err := tx.Where("collection_id = ?", targetID).Find(&targetItems).Error; err != nil {
		return err
	}
	nextPosition := 1
	targetByCatalog := make(map[uuid.UUID]int, len(targetItems))
	targetByRemote := make(map[string]bool, len(targetItems))
	for index := range targetItems {
		item := &targetItems[index]
		if item.Position >= nextPosition {
			nextPosition = item.Position + 1
		}
		if item.CatalogID != nil {
			targetByCatalog[*item.CatalogID] = index
		}
		if item.SyncRemoteID != nil {
			if remoteID := strings.TrimSpace(*item.SyncRemoteID); remoteID != "" {
				targetByRemote[remoteID] = true
			}
		}
	}

	var sourceItems []model.CollectionItem
	if err := tx.Where("collection_id = ?", sourceID).Order("position ASC, created_at ASC").Find(&sourceItems).Error; err != nil {
		return err
	}

	for index := range sourceItems {
		source := &sourceItems[index]
		remoteID := ""
		if source.SyncRemoteID != nil {
			remoteID = strings.TrimSpace(*source.SyncRemoteID)
		}
		if remoteID != "" && targetByRemote[remoteID] {
			if err := tx.Where("id = ?", source.ID).Delete(&model.CollectionItem{}).Error; err != nil {
				return err
			}
			continue
		}
		if source.CatalogID != nil {
			if targetIndex, exists := targetByCatalog[*source.CatalogID]; exists {
				target := &targetItems[targetIndex]
				mergeMirroredItem(target, source)
				if err := saveMergedItem(tx, target); err != nil {
					return err
				}
				if err := tx.Where("id = ?", source.ID).Delete(&model.CollectionItem{}).Error; err != nil {
					return err
				}
				continue
			}
		}

		source.CollectionID = targetID
		source.Position = nextPosition
		nextPosition++
		if err := tx.Model(&model.CollectionItem{}).Where("id = ?", source.ID).Updates(map[string]any{
			"collection_id": targetID,
			"position":      source.Position,
			"updated_at":    time.Now().UTC(),
		}).Error; err != nil {
			return err
		}
		targetItems = append(targetItems, *source)
		if source.CatalogID != nil {
			targetByCatalog[*source.CatalogID] = len(targetItems) - 1
		}
		if remoteID != "" {
			targetByRemote[remoteID] = true
		}
	}
	return tx.Model(&model.Collection{}).Where("id = ?", targetID).Update("updated_at", time.Now().UTC()).Error
}

// mergeMirroredItem folds the mirror's missing values into a watchlist entry.
// Anything the watchlist already carries stays; the sync fingerprints come
// from the mirror so the next pass can tell whether Bangumi moved since.
func mergeMirroredItem(target, source *model.CollectionItem) {
	if target.Status == nil {
		target.Status = source.Status
	}
	if target.Rating == nil {
		target.Rating = source.Rating
	}
	if target.Note == nil {
		target.Note = source.Note
	}
	if target.Progress == nil {
		target.Progress = source.Progress
	}
	if target.VolumeProgress == nil {
		target.VolumeProgress = source.VolumeProgress
	}
	adoptRemote := target.SyncRemoteID == nil || strings.TrimSpace(*target.SyncRemoteID) == ""
	if adoptRemote {
		target.SyncRemoteID = source.SyncRemoteID
	}
	if (adoptRemote || sameRemoteID(target.SyncRemoteID, source.SyncRemoteID)) &&
		(target.SyncLocalFingerprint == nil || target.SyncRemoteFingerprint == nil) {
		target.SyncLocalFingerprint = source.SyncLocalFingerprint
		target.SyncRemoteFingerprint = source.SyncRemoteFingerprint
	}
	if target.SyncedAt == nil {
		target.SyncedAt = source.SyncedAt
	}
}

func sameRemoteID(left, right *string) bool {
	if left == nil || right == nil {
		return false
	}
	leftValue := strings.TrimSpace(*left)
	return leftValue != "" && leftValue == strings.TrimSpace(*right)
}

func saveMergedItem(tx *gorm.DB, item *model.CollectionItem) error {
	now := time.Now().UTC()
	item.UpdatedAt = now
	return tx.Model(&model.CollectionItem{}).Where("id = ?", item.ID).Updates(map[string]any{
		"status":                  item.Status,
		"rating":                  item.Rating,
		"note":                    item.Note,
		"progress":                item.Progress,
		"volume_progress":         item.VolumeProgress,
		"sync_remote_id":          item.SyncRemoteID,
		"sync_local_fingerprint":  item.SyncLocalFingerprint,
		"sync_remote_fingerprint": item.SyncRemoteFingerprint,
		"synced_at":               item.SyncedAt,
		"updated_at":              now,
	}).Error
}

// ListSyncCollections returns every watchlist an account has made the target
// of a provider's lists.
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

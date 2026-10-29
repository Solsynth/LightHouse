package fediverse

import (
	"context"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/lighthouse/internal/platform/httpx"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

// handleKeyAudit mirrors KeyMigrationService.AuditKeysAsync.
func (s *server) handleKeyAudit(c *gin.Context) {
	ctx := c.Request.Context()
	var totalActors, actorsWithKeys, totalKeys, keysWithPrivate int64
	_ = s.db.WithContext(ctx).Model(&store.PublisherEntity{}).
		Where("type <> ?", int(model.PublisherFediverse)).Count(&totalActors).Error
	_ = s.db.WithContext(ctx).Model(&store.FediverseKeyEntity{}).Count(&totalKeys).Error
	_ = s.db.WithContext(ctx).Model(&store.FediverseKeyEntity{}).
		Where("private_key_pem IS NOT NULL AND private_key_pem <> ''").Count(&keysWithPrivate).Error
	_ = s.db.WithContext(ctx).Model(&store.PublisherEntity{}).
		Where("type <> ? AND id IN (?)", int(model.PublisherFediverse),
			s.db.Model(&store.FediverseKeyEntity{}).Select("publisher_id").Where("publisher_id IS NOT NULL")).
		Count(&actorsWithKeys).Error
	var orphaned int64
	_ = s.db.WithContext(ctx).Model(&store.FediverseKeyEntity{}).
		Where("publisher_id IS NOT NULL").
		Where("publisher_id NOT IN (?)", s.db.Model(&store.PublisherEntity{}).Select("id")).
		Count(&orphaned).Error

	httpx.OK(c, gin.H{
		"total_actors":             totalActors,
		"total_keys":               totalKeys,
		"actors_with_keys":         actorsWithKeys,
		"actors_without_keys":      totalActors - actorsWithKeys,
		"keys_with_private_key":    keysWithPrivate,
		"keys_without_private_key": totalKeys - keysWithPrivate,
		"orphaned_keys":            orphaned,
	})
}

// handleKeyBackfill migrates stored public keys and creates missing pairs.
func (s *server) handleKeyBackfill(c *gin.Context) {
	ctx := c.Request.Context()
	migrated, err := s.migratePublicKeys(ctx)
	if err != nil {
		httpx.Internal(c)
		return
	}
	created, err := s.backfillActorKeys(ctx)
	if err != nil {
		httpx.Internal(c)
		return
	}
	httpx.OK(c, gin.H{
		"migrated_from_actor": migrated,
		"newly_created":       created,
		"total":               migrated + created,
	})
}

func (s *server) migratePublicKeys(ctx context.Context) (int64, error) {
	var actors []store.PublisherEntity
	if err := s.db.WithContext(ctx).
		Where("public_key IS NOT NULL AND public_key <> ''").
		Where("id NOT IN (?)", s.db.Model(&store.FediverseKeyEntity{}).Select("publisher_id").Where("publisher_id IS NOT NULL")).
		Find(&actors).Error; err != nil {
		return 0, err
	}
	var migrated int64
	for i := range actors {
		if actors[i].URI == nil || *actors[i].URI == "" {
			continue
		}
		keyID := *actors[i].URI + "#main-key"
		var existing store.FediverseKeyEntity
		if err := s.db.WithContext(ctx).Where("key_id = ?", keyID).First(&existing).Error; err == nil {
			continue
		}
		row := &store.FediverseKeyEntity{
			ID:          uuid.New(),
			KeyID:       keyID,
			KeyPEM:      *actors[i].PublicKey,
			Algorithm:   "rsa-sha256",
			PublisherID: &actors[i].ID,
		}
		if err := s.db.WithContext(ctx).Create(row).Error; err == nil {
			migrated++
		}
	}
	return migrated, nil
}

func (s *server) backfillActorKeys(ctx context.Context) (int64, error) {
	var actors []store.PublisherEntity
	if err := s.db.WithContext(ctx).
		Where("type <> ? AND uri IS NOT NULL", int(model.PublisherFediverse)).
		Where("id NOT IN (?)", s.db.Model(&store.FediverseKeyEntity{}).Select("publisher_id").Where("publisher_id IS NOT NULL")).
		Find(&actors).Error; err != nil {
		return 0, err
	}
	var created int64
	for i := range actors {
		if _, err := s.keys.getOrCreateActorKey(ctx, &actors[i]); err == nil {
			created++
		}
	}
	return created, nil
}

// handleKeyCleanup removes keys whose publisher row no longer exists.
func (s *server) handleKeyCleanup(c *gin.Context) {
	ctx := c.Request.Context()
	var keys []store.FediverseKeyEntity
	if err := s.db.WithContext(ctx).
		Where("publisher_id IS NOT NULL").
		Where("publisher_id NOT IN (?)", s.db.Model(&store.PublisherEntity{}).Select("id")).
		Find(&keys).Error; err != nil {
		httpx.Internal(c)
		return
	}
	var removed int64
	for i := range keys {
		if err := s.db.WithContext(ctx).Delete(&keys[i]).Error; err == nil {
			removed++
		}
	}
	httpx.OK(c, gin.H{"removed": removed})
}

func (s *server) handleKeyStats(c *gin.Context) {
	ctx := c.Request.Context()
	var total, withPrivate, withoutPrivate, localActors int64
	_ = s.db.WithContext(ctx).Model(&store.FediverseKeyEntity{}).Count(&total).Error
	_ = s.db.WithContext(ctx).Model(&store.FediverseKeyEntity{}).
		Where("private_key_pem IS NOT NULL AND private_key_pem <> ''").Count(&withPrivate).Error
	_ = s.db.WithContext(ctx).Model(&store.FediverseKeyEntity{}).
		Where("private_key_pem IS NULL OR private_key_pem = ''").Count(&withoutPrivate).Error
	_ = s.db.WithContext(ctx).Model(&store.PublisherEntity{}).
		Where("uri IS NOT NULL").Count(&localActors).Error
	httpx.OK(c, gin.H{
		"total_keys":               total,
		"keys_with_private_key":    withPrivate,
		"keys_without_private_key": withoutPrivate,
		"local_actors":             localActors,
	})
}

func (s *server) handleActorKeyInfo(c *gin.Context) {
	id, err := uuid.Parse(c.Param("actorId"))
	if err != nil {
		httpx.Fail(c, errsActorNotFound())
		return
	}
	ctx := c.Request.Context()
	var actor store.PublisherEntity
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&actor).Error; err != nil {
		httpx.Fail(c, errsActorNotFound())
		return
	}
	var key store.FediverseKeyEntity
	hasKey := s.db.WithContext(ctx).Where("publisher_id = ?", id).First(&key).Error == nil
	info := gin.H{
		"actor_id":        actor.ID.String(),
		"actor_uri":       deref(actor.URI),
		"username":        deref(actor.Username),
		"domain":          deref(actor.InstanceDomain),
		"has_key":         hasKey,
		"has_private_key": hasKey && key.PrivateKeyPEM != nil && *key.PrivateKeyPEM != "",
		"key_id":          nil,
		"key_created_at":  nil,
		"key_rotated_at":  nil,
	}
	if hasKey {
		info["key_id"] = key.KeyID
		if !key.CreatedAt.IsZero() {
			info["key_created_at"] = activityPubTime(key.CreatedAt)
		}
		if key.RotatedAt != nil {
			info["key_rotated_at"] = activityPubTime(*key.RotatedAt)
		}
	}
	httpx.OK(c, info)
}

func (s *server) handleActorKeyEnsure(c *gin.Context) {
	id, err := uuid.Parse(c.Param("actorId"))
	if err != nil {
		httpx.Fail(c, errsActorNotFound())
		return
	}
	ctx := c.Request.Context()
	var actor store.PublisherEntity
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&actor).Error; err != nil {
		httpx.Fail(c, errsActorNotFound())
		return
	}
	if _, err := s.keys.getOrCreateActorKey(ctx, &actor); err != nil {
		httpx.BadRequest(c, "FEDIVERSE_KEY_ENSURE_FAILED", "Failed to ensure key.")
		return
	}
	httpx.OK(c, gin.H{"message": "Key ensured successfully"})
}

func (s *server) handleActorKeyRegenerate(c *gin.Context) {
	id, err := uuid.Parse(c.Param("actorId"))
	if err != nil {
		httpx.Fail(c, errsActorNotFound())
		return
	}
	ctx := c.Request.Context()
	var actor store.PublisherEntity
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&actor).Error; err != nil {
		httpx.Fail(c, errsActorNotFound())
		return
	}
	if actor.URI == nil || *actor.URI == "" {
		httpx.BadRequest(c, "FEDIVERSE_KEY_REGENERATE_FAILED", "Failed to regenerate key.")
		return
	}
	if err := s.keys.rotateKey(ctx, id); err != nil {
		s.log.Error("failed to rotate actor key", "actor_id", id, "error", err)
		httpx.BadRequest(c, "FEDIVERSE_KEY_REGENERATE_FAILED", "Failed to regenerate key.")
		return
	}
	httpx.OK(c, gin.H{"message": "Key regenerated successfully"})
}

// keep errs referenced for the ApiError shorthands used above.

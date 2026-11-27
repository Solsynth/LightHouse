package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
)

var (
	// ErrSyncKeyNotFound indicates the key does not exist or is revoked.
	ErrSyncKeyNotFound = errors.New("sync api key not found")
	// ErrSyncKeyScopeDenied indicates the key is valid but lacks the required scope.
	ErrSyncKeyScopeDenied = errors.New("sync api key scope denied")
)

const syncKeyPrefix = "pls_"

// HashSyncAPIKey returns the lowercase SHA-256 hex digest of a raw API key.
func HashSyncAPIKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// CreateSyncAPIKey generates a new opaque API key for accountID, stores its
// SHA-256 hash, and returns both the persisted record and the one-time raw key.
func (s *Store) CreateSyncAPIKey(ctx context.Context, accountID uuid.UUID, name string, scopes []string) (*model.SyncAPIKey, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, "", errors.New("key name is required")
	}

	secret := make([]byte, 24)
	if _, err := rand.Read(secret); err != nil {
		return nil, "", err
	}
	rawKey := syncKeyPrefix + hex.EncodeToString(secret)
	prefix := rawKey[:12]

	var scopesJSON datatypes.JSON
	if len(scopes) > 0 {
		cleaned := make([]string, 0, len(scopes))
		for _, sc := range scopes {
			if trimmed := strings.TrimSpace(strings.ToLower(sc)); trimmed != "" {
				cleaned = append(cleaned, trimmed)
			}
		}
		if len(cleaned) > 0 {
			encoded, err := json.Marshal(cleaned)
			if err != nil {
				return nil, "", err
			}
			scopesJSON = encoded
		}
	}

	now := time.Now().UTC()
	row := &model.SyncAPIKey{
		ID:        uuid.New(),
		AccountID: accountID,
		Name:      name,
		KeyHash:   HashSyncAPIKey(rawKey),
		KeyPrefix: prefix,
		Scopes:    scopesJSON,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.DB.WithContext(ctx).Create(row).Error; err != nil {
		return nil, "", err
	}
	return row, rawKey, nil
}

// ListSyncAPIKeys returns all active (non-revoked) sync API keys for accountID.
func (s *Store) ListSyncAPIKeys(ctx context.Context, accountID uuid.UUID) ([]model.SyncAPIKey, error) {
	var rows []model.SyncAPIKey
	err := s.DB.WithContext(ctx).
		Where("account_id = ? AND revoked_at IS NULL", accountID).
		Order("created_at DESC").
		Find(&rows).Error
	return rows, err
}

// RevokeSyncAPIKey marks a key owned by accountID as revoked.
func (s *Store) RevokeSyncAPIKey(ctx context.Context, accountID, keyID uuid.UUID) error {
	now := time.Now().UTC()
	res := s.DB.WithContext(ctx).
		Model(&model.SyncAPIKey{}).
		Where("id = ? AND account_id = ? AND revoked_at IS NULL", keyID, accountID).
		Updates(map[string]any{
			"revoked_at": &now,
			"updated_at": now,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrSyncKeyNotFound
	}
	return nil
}

// AuthenticateSyncAPIKey validates rawKey, enforces requiredScope when the key
// has non-empty scopes, and stamps last_used_at.
func (s *Store) AuthenticateSyncAPIKey(ctx context.Context, rawKey, requiredScope string) (*model.SyncAPIKey, error) {
	rawKey = strings.TrimSpace(rawKey)
	if rawKey == "" {
		return nil, ErrSyncKeyNotFound
	}

	var row model.SyncAPIKey
	err := s.DB.WithContext(ctx).
		Where("key_hash = ? AND revoked_at IS NULL", HashSyncAPIKey(rawKey)).
		First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrSyncKeyNotFound
		}
		return nil, err
	}

	if requiredScope != "" && len(row.Scopes) > 0 {
		var allowed []string
		if err := json.Unmarshal(row.Scopes, &allowed); err == nil && len(allowed) > 0 {
			matched := false
			target := strings.ToLower(strings.TrimSpace(requiredScope))
			for _, sc := range allowed {
				if sc == "*" || strings.ToLower(strings.TrimSpace(sc)) == target {
					matched = true
					break
				}
			}
			if !matched {
				return nil, ErrSyncKeyScopeDenied
			}
		}
	}

	now := time.Now().UTC()
	_ = s.DB.WithContext(ctx).
		Model(&model.SyncAPIKey{}).
		Where("id = ?", row.ID).
		Update("last_used_at", &now).Error
	row.LastUsedAt = &now
	return &row, nil
}

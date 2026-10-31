package nearby

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/stargate/store"
)

// Store owns the two nearby tables. Nothing outside this package queries
// them directly.
type Store struct {
	db *gorm.DB
}

// NewStore builds the persistence layer.
func NewStore(db *gorm.DB) *Store { return &Store{db: db} }

// FindDevice loads the live device row for (user, device). A missing row
// returns store.ErrNotFound.
func (s *Store) FindDevice(ctx context.Context, userID uuid.UUID, deviceID string) (*NearbyDeviceEntity, error) {
	var entity NearbyDeviceEntity
	err := s.db.WithContext(ctx).
		Where("user_id = ? AND device_id = ?", userID, deviceID).
		First(&entity).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	return &entity, nil
}

// CreateDevice inserts a new device row.
func (s *Store) CreateDevice(ctx context.Context, entity *NearbyDeviceEntity) error {
	return s.db.WithContext(ctx).Create(entity).Error
}

// SaveDevice updates an existing device row.
func (s *Store) SaveDevice(ctx context.Context, entity *NearbyDeviceEntity) error {
	return s.db.WithContext(ctx).Save(entity).Error
}

// DeleteTokensFromSlot physically removes future tokens for a device, mirroring
// NearbyService's ExecuteDeleteAsync (settings change invalidates them).
func (s *Store) DeleteTokensFromSlot(ctx context.Context, deviceID uuid.UUID, slot int64) error {
	return s.db.WithContext(ctx).Unscoped().
		Where("device_id = ? AND slot >= ?", deviceID, slot).
		Delete(&NearbyPresenceTokenEntity{}).Error
}

// ListTokenSlots returns the slots already materialized for a device in
// [from, to).
func (s *Store) ListTokenSlots(ctx context.Context, deviceID uuid.UUID, from, to int64) ([]int64, error) {
	var slots []int64
	err := s.db.WithContext(ctx).Model(&NearbyPresenceTokenEntity{}).
		Where("device_id = ? AND slot >= ? AND slot < ?", deviceID, from, to).
		Pluck("slot", &slots).Error
	return slots, err
}

// CreateTokens inserts the newly generated slot rows in one statement.
func (s *Store) CreateTokens(ctx context.Context, entities []*NearbyPresenceTokenEntity) error {
	if len(entities) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Create(&entities).Error
}

// TokenMatch is one resolvable token row joined with its device owner.
type TokenMatch struct {
	TokenHash  string    `gorm:"column:token_hash"`
	Slot       int64     `gorm:"column:slot"`
	FriendOnly bool      `gorm:"column:friend_only"`
	UserID     uuid.UUID `gorm:"column:user_id"`
}

// FindMatches resolves token hashes/slots to active, discoverable devices
// within the validity window (C#: ValidFrom <= now + skew && ValidTo >= now -
// skew).
func (s *Store) FindMatches(ctx context.Context, hashes []string, slots []int64, now time.Time, skew time.Duration) ([]TokenMatch, error) {
	if len(hashes) == 0 || len(slots) == 0 {
		return nil, nil
	}
	var matches []TokenMatch
	err := s.db.WithContext(ctx).Table("nearby_presence_tokens AS t").
		Select("t.token_hash, t.slot, t.friend_only, d.user_id").
		Joins("JOIN nearby_devices AS d ON d.id = t.device_id").
		Where("t.deleted_at IS NULL AND d.deleted_at IS NULL").
		Where("t.token_hash IN ?", hashes).
		Where("t.slot IN ?", slots).
		Where("t.valid_from <= ? AND t.valid_to >= ?", now.Add(skew), now.Add(-skew)).
		Where("d.status = ? AND d.discoverable", int(DeviceActive)).
		Scan(&matches).Error
	if err != nil {
		return nil, err
	}
	return matches, nil
}

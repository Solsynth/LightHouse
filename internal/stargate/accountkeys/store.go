package accountkeys

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/stargate/model"
)

// MaxKeysPerAccount caps each key kind per account. Twenty is generous for
// laptops, CI deploy keys and YubiKeys while keeping the list readable.
const MaxKeysPerAccount = 20

var (
	// ErrNotFound reports a key that does not exist or belongs to another
	// account.
	ErrNotFound = errors.New("accountkeys: not found")
	// ErrDuplicate reports an account that already has this fingerprint.
	ErrDuplicate = errors.New("accountkeys: key already exists")
	// ErrLimit reports an account at the per-kind cap.
	ErrLimit = errors.New("accountkeys: key limit reached")
)

// Store persists account SSH and PGP keys in the module-owned tables.
type Store struct {
	DB *gorm.DB
}

// NewStore builds the persistence layer over the module database handle.
func NewStore(db *gorm.DB) *Store { return &Store{DB: db} }

// ── SSH ─────────────────────────────────────────────────────────────────────

// ListSSHKeys lists the account's SSH keys, oldest first.
func (s *Store) ListSSHKeys(ctx context.Context, accountID uuid.UUID) ([]model.SSHKey, error) {
	var entities []SSHKeyEntity
	if err := s.DB.WithContext(ctx).
		Where("account_id = ?", accountID).
		Order("created_at").Find(&entities).Error; err != nil {
		return nil, err
	}
	keys := make([]model.SSHKey, 0, len(entities))
	for i := range entities {
		keys = append(keys, SSHKeyFromEntity(&entities[i]))
	}
	return keys, nil
}

// FindSSHKeyByFingerprint resolves a fingerprint to one account's live key.
// accountID nil searches every account, which is what a git gateway needs
// before it knows the owner. It returns ErrNotFound when no live row matches.
func (s *Store) FindSSHKeyByFingerprint(ctx context.Context, fingerprint string, accountID *uuid.UUID) (*model.SSHKey, error) {
	query := s.DB.WithContext(ctx).Where("fingerprint = ?", fingerprint)
	if accountID != nil {
		query = query.Where("account_id = ?", *accountID)
	}
	var entity SSHKeyEntity
	if err := query.First(&entity).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	key := SSHKeyFromEntity(&entity)
	return &key, nil
}

// FindPGPKeyByFingerprint resolves a primary-key fingerprint to one account's
// live key. accountID nil searches every account.
func (s *Store) FindPGPKeyByFingerprint(ctx context.Context, fingerprint string, accountID *uuid.UUID) (*model.PGPKey, error) {
	query := s.DB.WithContext(ctx).Where("fingerprint = ?", fingerprint)
	if accountID != nil {
		query = query.Where("account_id = ?", *accountID)
	}
	var entity PGPKeyEntity
	if err := query.First(&entity).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	key := PGPKeyFromEntity(&entity)
	return &key, nil
}

// CreateSSHKey inserts a normalized SSH key. It reports ErrDuplicate when the
// account already holds this fingerprint and ErrLimit at the cap.
func (s *Store) CreateSSHKey(ctx context.Context, accountID uuid.UUID, label string, info *SSHKeyInfo) (*model.SSHKey, error) {
	count, err := s.count(ctx, &SSHKeyEntity{}, accountID)
	if err != nil {
		return nil, err
	}
	if count >= MaxKeysPerAccount {
		return nil, ErrLimit
	}
	now := time.Now().UTC()
	entity := &SSHKeyEntity{
		ID:          uuid.New(),
		EntityBase:  EntityBase{CreatedAt: now, UpdatedAt: now},
		AccountID:   accountID,
		Label:       label,
		Algorithm:   info.Algorithm,
		PublicKey:   info.PublicKey,
		Fingerprint: info.Fingerprint,
	}
	if err := s.DB.WithContext(ctx).Create(entity).Error; err != nil {
		if isDuplicate(err) {
			return nil, ErrDuplicate
		}
		return nil, err
	}
	key := SSHKeyFromEntity(entity)
	return &key, nil
}

// RenameSSHKey changes the label of one account-owned key.
func (s *Store) RenameSSHKey(ctx context.Context, accountID, id uuid.UUID, label string) (*model.SSHKey, error) {
	entity, err := s.getSSHKey(ctx, accountID, id)
	if err != nil {
		return nil, err
	}
	if err := s.DB.WithContext(ctx).Model(&SSHKeyEntity{}).
		Where("id = ? AND account_id = ?", id, accountID).
		Updates(map[string]any{"label": label, "updated_at": time.Now().UTC()}).Error; err != nil {
		return nil, err
	}
	entity.Label = label
	entity.UpdatedAt = time.Now().UTC()
	key := SSHKeyFromEntity(entity)
	return &key, nil
}

// DeleteSSHKey removes one account-owned key.
func (s *Store) DeleteSSHKey(ctx context.Context, accountID, id uuid.UUID) error {
	entity, err := s.getSSHKey(ctx, accountID, id)
	if err != nil {
		return err
	}
	return s.delete(ctx, entity, accountID, id)
}

func (s *Store) getSSHKey(ctx context.Context, accountID, id uuid.UUID) (*SSHKeyEntity, error) {
	var entity SSHKeyEntity
	if err := s.DB.WithContext(ctx).
		Where("id = ? AND account_id = ?", id, accountID).
		First(&entity).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &entity, nil
}

// ── PGP ─────────────────────────────────────────────────────────────────────

// ListPGPKeys lists the account's PGP keys, oldest first.
func (s *Store) ListPGPKeys(ctx context.Context, accountID uuid.UUID) ([]model.PGPKey, error) {
	var entities []PGPKeyEntity
	if err := s.DB.WithContext(ctx).
		Where("account_id = ?", accountID).
		Order("created_at").Find(&entities).Error; err != nil {
		return nil, err
	}
	keys := make([]model.PGPKey, 0, len(entities))
	for i := range entities {
		keys = append(keys, PGPKeyFromEntity(&entities[i]))
	}
	return keys, nil
}

// CreatePGPKey inserts an armored PGP key. It reports ErrDuplicate when the
// account already holds this fingerprint and ErrLimit at the cap.
func (s *Store) CreatePGPKey(ctx context.Context, accountID uuid.UUID, label string, info *PGPKeyInfo) (*model.PGPKey, error) {
	count, err := s.count(ctx, &PGPKeyEntity{}, accountID)
	if err != nil {
		return nil, err
	}
	if count >= MaxKeysPerAccount {
		return nil, ErrLimit
	}
	now := time.Now().UTC()
	entity := &PGPKeyEntity{
		ID:          uuid.New(),
		EntityBase:  EntityBase{CreatedAt: now, UpdatedAt: now},
		AccountID:   accountID,
		Label:       label,
		Algorithm:   info.Algorithm,
		PublicKey:   info.PublicKey,
		Fingerprint: info.Fingerprint,
		KeyID:       info.KeyID,
		UserIDs:     joinUserIDs(info.UserIDs),
	}
	if err := s.DB.WithContext(ctx).Create(entity).Error; err != nil {
		if isDuplicate(err) {
			return nil, ErrDuplicate
		}
		return nil, err
	}
	key := PGPKeyFromEntity(entity)
	return &key, nil
}

// RenamePGPKey changes the label of one account-owned key.
func (s *Store) RenamePGPKey(ctx context.Context, accountID, id uuid.UUID, label string) (*model.PGPKey, error) {
	entity, err := s.getPGPKey(ctx, accountID, id)
	if err != nil {
		return nil, err
	}
	if err := s.DB.WithContext(ctx).Model(&PGPKeyEntity{}).
		Where("id = ? AND account_id = ?", id, accountID).
		Updates(map[string]any{"label": label, "updated_at": time.Now().UTC()}).Error; err != nil {
		return nil, err
	}
	entity.Label = label
	entity.UpdatedAt = time.Now().UTC()
	key := PGPKeyFromEntity(entity)
	return &key, nil
}

// DeletePGPKey removes one account-owned key.
func (s *Store) DeletePGPKey(ctx context.Context, accountID, id uuid.UUID) error {
	entity, err := s.getPGPKey(ctx, accountID, id)
	if err != nil {
		return err
	}
	return s.delete(ctx, entity, accountID, id)
}

func (s *Store) getPGPKey(ctx context.Context, accountID, id uuid.UUID) (*PGPKeyEntity, error) {
	var entity PGPKeyEntity
	if err := s.DB.WithContext(ctx).
		Where("id = ? AND account_id = ?", id, accountID).
		First(&entity).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &entity, nil
}

func (s *Store) count(ctx context.Context, entity any, accountID uuid.UUID) (int64, error) {
	var count int64
	err := s.DB.WithContext(ctx).Model(entity).Where("account_id = ?", accountID).Count(&count).Error
	return count, err
}

// delete hard-deletes a key row, matching the passkey surface: the partial
// unique index then frees the fingerprint for re-adding.
func (s *Store) delete(ctx context.Context, entity any, accountID, id uuid.UUID) error {
	result := s.DB.WithContext(ctx).Unscoped().
		Where("id = ? AND account_id = ?", id, accountID).Delete(entity)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func isDuplicate(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

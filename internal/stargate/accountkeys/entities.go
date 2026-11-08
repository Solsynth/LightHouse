// Package accountkeys owns an account's SSH and PGP public keys: the two
// module-owned tables, key parsing/normalization, and the persistence layer the
// HTTP controller and the module use.
package accountkeys

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// EntityBase mirrors the core store's timestamp and soft-delete columns.
// Keys are hard-deleted (like passkeys), so `deleted_at` exists only for
// schema consistency and the partial unique index.
type EntityBase struct {
	CreatedAt time.Time      `gorm:"column:created_at"`
	UpdatedAt time.Time      `gorm:"column:updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"column:deleted_at;index"`
}

// SSHKeyEntity is `account_ssh_keys`: one normalized `authorized_keys` line per
// row. Fingerprints are unique per account while the row is live, so re-adding
// the same key after deleting it succeeds.
type SSHKeyEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	AccountID   uuid.UUID `gorm:"column:account_id;type:uuid;not null"`
	Label       string    `gorm:"column:label;type:varchar(128);not null"`
	Algorithm   string    `gorm:"column:algorithm;type:varchar(64);not null"`
	PublicKey   string    `gorm:"column:public_key;type:text;not null"`
	Fingerprint string    `gorm:"column:fingerprint;type:varchar(128);not null"`
}

func (SSHKeyEntity) TableName() string { return "account_ssh_keys" }

// PGPKeyEntity is `account_pgp_keys`: one armored OpenPGP public key block per
// row, keyed by the primary key's fingerprint.
type PGPKeyEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	AccountID   uuid.UUID `gorm:"column:account_id;type:uuid;not null"`
	Label       string    `gorm:"column:label;type:varchar(128);not null"`
	Algorithm   string    `gorm:"column:algorithm;type:varchar(64);not null"`
	PublicKey   string    `gorm:"column:public_key;type:text;not null"`
	Fingerprint string    `gorm:"column:fingerprint;type:varchar(128);not null"`
	KeyID       string    `gorm:"column:key_id;type:varchar(64);not null"`
	UserIDs     string    `gorm:"column:user_ids;type:text;not null"`
}

func (PGPKeyEntity) TableName() string { return "account_pgp_keys" }

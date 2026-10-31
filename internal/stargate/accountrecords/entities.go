package accountrecords

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// EntityBase mirrors the C# ModelBase shared by the Passport record tables.
type EntityBase struct {
	CreatedAt time.Time      `gorm:"column:created_at"`
	UpdatedAt time.Time      `gorm:"column:updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"column:deleted_at;index"`
}

// ExperienceRecordEntity is Passport's `experience_records` table.
type ExperienceRecordEntity struct {
	ID              uuid.UUID `gorm:"column:id;primaryKey;type:uuid"`
	ReasonType      string    `gorm:"column:reason_type;size:1024;not null"`
	Reason          string    `gorm:"column:reason;size:1024;not null"`
	Delta           int64     `gorm:"column:delta;not null"`
	BonusMultiplier float64   `gorm:"column:bonus_multiplier;not null"`
	AccountID       uuid.UUID `gorm:"column:account_id;type:uuid;not null"`
	EntityBase
}

func (ExperienceRecordEntity) TableName() string { return "experience_records" }

// SocialCreditRecordEntity is Passport's `social_credit_records` table.
// Status mirrors SocialCreditRecordStatus (Active = 0, Expired = 1); the
// effective-credit calculation, like the C# one, derives expiry from the
// timestamps rather than trusting the stored status.
type SocialCreditRecordEntity struct {
	ID         uuid.UUID  `gorm:"column:id;primaryKey;type:uuid"`
	ReasonType string     `gorm:"column:reason_type;size:1024;not null"`
	Reason     string     `gorm:"column:reason;size:1024;not null"`
	Delta      float64    `gorm:"column:delta;not null"`
	Status     int        `gorm:"column:status;not null"`
	ExpiredAt  *time.Time `gorm:"column:expired_at"`
	AccountID  uuid.UUID  `gorm:"column:account_id;type:uuid;not null"`
	EntityBase
}

func (SocialCreditRecordEntity) TableName() string { return "social_credit_records" }

// BadgeEntity is Passport's `badges` table (SnAccountBadge). The legacy table
// name is `badges`, not `account_badges`.
type BadgeEntity struct {
	ID          uuid.UUID      `gorm:"column:id;primaryKey;type:uuid"`
	Type        string         `gorm:"column:type;size:1024;not null"`
	Label       *string        `gorm:"column:label;size:1024"`
	Caption     *string        `gorm:"column:caption;size:4096"`
	Meta        datatypes.JSON `gorm:"column:meta;type:jsonb;not null"`
	ActivatedAt *time.Time     `gorm:"column:activated_at"`
	ExpiredAt   *time.Time     `gorm:"column:expired_at"`
	AccountID   uuid.UUID      `gorm:"column:account_id;type:uuid;not null"`
	EntityBase
}

func (BadgeEntity) TableName() string { return "badges" }

// BoardItemEntity is Passport's `account_board_items` table.
type BoardItemEntity struct {
	ID                 uuid.UUID      `gorm:"column:id;primaryKey;type:uuid"`
	AccountID          uuid.UUID      `gorm:"column:account_id;type:uuid;not null"`
	Order              int            `gorm:"column:order;not null"`
	Kind               int            `gorm:"column:kind;not null"`
	WidgetKey          *string        `gorm:"column:widget_key;size:256"`
	CustomAppID        *uuid.UUID     `gorm:"column:custom_app_id;type:uuid"`
	CustomAppWidgetKey *string        `gorm:"column:custom_app_widget_key;size:256"`
	IsEnabled          bool           `gorm:"column:is_enabled;not null"`
	Payload            datatypes.JSON `gorm:"column:payload;type:jsonb;not null"`
	EntityBase
}

func (BoardItemEntity) TableName() string { return "account_board_items" }

// Models returns the module's GORM entities for AutoMigrate.
func Models() []any {
	return []any{
		&ExperienceRecordEntity{},
		&SocialCreditRecordEntity{},
		&BadgeEntity{},
		&BoardItemEntity{},
	}
}

// Hooks recreates the indexes the legacy migrations declared. AutoMigrate
// cannot express the composite uniqueness of the board table on its own.
func Hooks() []func(context.Context, *gorm.DB) error {
	return []func(context.Context, *gorm.DB) error{
		func(ctx context.Context, db *gorm.DB) error {
			return db.WithContext(ctx).Exec(`
				CREATE UNIQUE INDEX IF NOT EXISTS ix_account_board_items_account_id_order
				ON account_board_items (account_id, "order")`).Error
		},
		func(ctx context.Context, db *gorm.DB) error {
			return db.WithContext(ctx).Exec(`
				CREATE INDEX IF NOT EXISTS ix_account_board_items_account_id_custom_app_id_custom_app_wid
				ON account_board_items (account_id, custom_app_id, custom_app_widget_key)`).Error
		},
	}
}

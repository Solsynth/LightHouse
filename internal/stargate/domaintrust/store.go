package domaintrust

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ErrNotFound reports a missing rule or metric.
var ErrNotFound = errors.New("domaintrust: not found")

// ErrInvalidTrustLevel reports a trust level outside the blocked/neutral/
// verified range. Rule creation and updates accept the enum only: a rule with
// an unknown level compares as neither blocked nor verified, silently
// disabling the fleet's URL validation.
var ErrInvalidTrustLevel = errors.New("domaintrust: trust level must be 0 (blocked), 1 (neutral) or 2 (verified)")

// Store owns the two domain-trust tables.
type Store struct {
	DB *gorm.DB
}

// NewStore builds a store.
func NewStore(db *gorm.DB) *Store { return &Store{DB: db} }

// ─────────────────────────── rules ───────────────────────────

// ListBlocks returns rules ordered the way GetAllRulesAsync does (priority
// desc, then pattern) plus the unpaged total.
func (s *Store) ListBlocks(ctx context.Context, offset, limit int) ([]DomainBlockEntity, int64, error) {
	query := s.DB.WithContext(ctx).Model(&DomainBlockEntity{})
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []DomainBlockEntity
	err := query.Order("priority DESC").Order("domain_pattern ASC").
		Offset(offset).Limit(limit).Find(&rows).Error
	return rows, total, err
}

// GetBlock loads one rule.
func (s *Store) GetBlock(ctx context.Context, id uuid.UUID) (*DomainBlockEntity, error) {
	var row DomainBlockEntity
	if err := s.DB.WithContext(ctx).First(&row, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &row, nil
}

// CreateBlock inserts a rule.
func (s *Store) CreateBlock(ctx context.Context, row *DomainBlockEntity) error {
	return s.DB.WithContext(ctx).Create(row).Error
}

// SaveBlock persists changed fields.
func (s *Store) SaveBlock(ctx context.Context, row *DomainBlockEntity) error {
	return s.DB.WithContext(ctx).Save(row).Error
}

// DeleteBlock removes a rule. Passport called DbSet.Remove, so this is a hard
// delete, not a soft-delete timestamp.
func (s *Store) DeleteBlock(ctx context.Context, id uuid.UUID) (bool, error) {
	result := s.DB.WithContext(ctx).Unscoped().Delete(&DomainBlockEntity{}, "id = ?", id)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// ActiveBlocks loads every active rule, highest priority first, for matching.
func (s *Store) ActiveBlocks(ctx context.Context) ([]DomainBlockEntity, error) {
	var rows []DomainBlockEntity
	err := s.DB.WithContext(ctx).Where("is_active = ?", true).
		Order("priority DESC").Order("domain_pattern ASC").Find(&rows).Error
	return rows, err
}

// ─────────────────────────── metrics ───────────────────────────

// ListMetrics mirrors GetDomainMetricsAsync (check count desc, domain asc).
func (s *Store) ListMetrics(ctx context.Context, offset, limit int) ([]DomainValidationMetricEntity, int64, error) {
	query := s.DB.WithContext(ctx).Model(&DomainValidationMetricEntity{})
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []DomainValidationMetricEntity
	err := query.Order("check_count DESC").Order("domain ASC").
		Offset(offset).Limit(limit).Find(&rows).Error
	return rows, total, err
}

// GetMetric loads one metric by domain.
func (s *Store) GetMetric(ctx context.Context, domain string) (*DomainValidationMetricEntity, error) {
	var row DomainValidationMetricEntity
	if err := s.DB.WithContext(ctx).First(&row, "domain = ?", domain).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &row, nil
}

// IncrementMetric applies one buffered validation to the metric row, creating
// it when missing.
func (s *Store) IncrementMetric(ctx context.Context, domain string, allowed, verified bool, checkedAt time.Time) error {
	blocked := int64(0)
	if !allowed {
		blocked = 1
	}
	verifiedCount := int64(0)
	if verified {
		verifiedCount = 1
	}
	return s.DB.WithContext(ctx).Exec(`
		INSERT INTO domain_validation_metrics
			(id, domain, check_count, blocked_count, verified_count, last_checked_at, created_at, updated_at)
		VALUES (?, ?, 1, ?, ?, ?, now(), now())
		ON CONFLICT (domain) DO UPDATE SET
			check_count = domain_validation_metrics.check_count + 1,
			blocked_count = domain_validation_metrics.blocked_count + EXCLUDED.blocked_count,
			verified_count = domain_validation_metrics.verified_count + EXCLUDED.verified_count,
			last_checked_at = GREATEST(
				COALESCE(domain_validation_metrics.last_checked_at, EXCLUDED.last_checked_at),
				EXCLUDED.last_checked_at
			),
			updated_at = now()`,
		uuid.New(), domain, blocked, verifiedCount, checkedAt).Error
}

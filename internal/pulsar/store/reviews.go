package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
)

// ReviewStats aggregates the ratings of one catalog item.
type ReviewStats struct {
	Average float64 `json:"average"`
	Count   int64   `json:"count"`
}

// UpsertReview writes an account's review of a catalog item. One review per
// account per item: the caller's copy is the truth, so a re-send updates the
// body and rating in place instead of appending.
func (s *Store) UpsertReview(ctx context.Context, review *model.Review) error {
	return s.DB.WithContext(ctx).Save(review).Error
}

// GetReview fetches one review by id.
func (s *Store) GetReview(ctx context.Context, id uuid.UUID) (*model.Review, error) {
	var review model.Review
	if err := s.DB.WithContext(ctx).First(&review, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &review, nil
}

// GetCatalogReview returns the account's review of one catalog item, or
// gorm.ErrRecordNotFound when the account has none.
func (s *Store) GetCatalogReview(ctx context.Context, accountID, catalogID uuid.UUID) (*model.Review, error) {
	var review model.Review
	if err := s.DB.WithContext(ctx).
		Where("account_id = ? AND catalog_id = ?", accountID, catalogID).
		First(&review).Error; err != nil {
		return nil, err
	}
	return &review, nil
}

// ListCatalogReviews returns a page of reviews for one catalog item, newest
// first. A nil accountFilter lists every account's reviews; otherwise only
// that account's.
func (s *Store) ListCatalogReviews(ctx context.Context, catalogID uuid.UUID, accountFilter *uuid.UUID, offset, take int) ([]model.Review, int64, error) {
	query := s.DB.WithContext(ctx).Model(&model.Review{}).Where("catalog_id = ?", catalogID)
	if accountFilter != nil {
		query = query.Where("account_id = ?", *accountFilter)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var reviews []model.Review
	if err := query.Order("created_at DESC").Offset(offset).Limit(take).Find(&reviews).Error; err != nil {
		return nil, 0, err
	}
	return reviews, total, nil
}

// DeleteReview removes one review, gated on ownership so the id of someone
// else's review deletes nothing.
func (s *Store) DeleteReview(ctx context.Context, accountID, reviewID uuid.UUID) error {
	result := s.DB.WithContext(ctx).Delete(&model.Review{}, "id = ? AND account_id = ?", reviewID, accountID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// CatalogReviewStats averages the ratings of one catalog item over accounts.
func (s *Store) CatalogReviewStats(ctx context.Context, catalogID uuid.UUID) (ReviewStats, error) {
	var stats ReviewStats
	var ratingSum struct {
		Sum   *float64 `gorm:"column:sum"`
		Count int64    `gorm:"column:count"`
	}
	err := s.DB.WithContext(ctx).Model(&model.Review{}).
		Select("SUM(rating) AS sum, COUNT(*) AS count").
		Where("catalog_id = ? AND rating IS NOT NULL", catalogID).
		Scan(&ratingSum).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return stats, nil
		}
		return stats, err
	}
	stats.Count = ratingSum.Count
	if ratingSum.Sum != nil && ratingSum.Count > 0 {
		stats.Average = *ratingSum.Sum / float64(ratingSum.Count)
	}
	return stats, nil
}

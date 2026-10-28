package posts

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

// viewDedupTTL mirrors the C# PostService deduplication window.
const viewDedupTTL = time.Hour

// signalsPort implements ports.PostSignals for sibling modules.
type signalsPort struct{ h *handler }

// IncrementViews records one view. Unique views are deduplicated per account
// over a one-hour window, in Redis when available and in-process otherwise.
// Both counters flush through the module's existing view buffer.
func (p *signalsPort) IncrementViews(ctx context.Context, postID, accountID string) error {
	id, err := uuid.Parse(postID)
	if err != nil || id == uuid.Nil {
		return fmt.Errorf("posts: invalid post id %q", postID)
	}
	account := uuid.Nil
	unique := false
	if accountID != "" {
		account, err = uuid.Parse(accountID)
		if err != nil {
			return fmt.Errorf("posts: invalid account id %q", accountID)
		}
		unique = p.h.claimUniqueView(ctx, id, account)
	}
	p.h.enqueueViewSignal(id, account, unique)
	return nil
}

// RecordInterest upserts the account's interest profile for (kind, reference).
func (p *signalsPort) RecordInterest(ctx context.Context, accountID string, kind int, referenceID string, score float64, signal string) error {
	account, err := uuid.Parse(accountID)
	if err != nil || account == uuid.Nil {
		return fmt.Errorf("posts: invalid account id %q", accountID)
	}
	reference, err := uuid.Parse(referenceID)
	if err != nil || reference == uuid.Nil {
		return fmt.Errorf("posts: invalid reference id %q", referenceID)
	}
	now := time.Now().UTC()
	var signalType *string
	if signal != "" {
		signalType = &signal
	}
	const statement = `INSERT INTO post_interest_profiles
		(id, created_at, updated_at, account_id, kind, reference_id, score,
		 interaction_count, last_interacted_at, last_signal_type)
		VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?)
		ON CONFLICT (account_id, kind, reference_id) WHERE deleted_at IS NULL
		DO UPDATE SET
			score = LEAST(100, GREATEST(-100,
				post_interest_profiles.score + EXCLUDED.score)),
			interaction_count = post_interest_profiles.interaction_count + 1,
			last_interacted_at = EXCLUDED.last_interacted_at,
			last_signal_type = EXCLUDED.last_signal_type,
			updated_at = EXCLUDED.updated_at`
	return p.h.db.WithContext(ctx).Exec(statement,
		uuid.New(), now, now, account, kind, reference, clampInterest(score),
		now, signalType).Error
}

// SponsoredPost returns the post the sponsor slot currently points at, using
// the aggregated placement flag and the active placement window.
func (p *signalsPort) SponsoredPost(ctx context.Context) (*model.Post, error) {
	now := time.Now().UTC()
	var ent store.PostEntity
	query := `SELECT p.* FROM posts p
		JOIN post_aggregated_stats s
			ON s.post_id = p.id AND s.is_currently_placed = TRUE
		JOIN post_sponsor_placements pl
			ON pl.post_id = p.id AND pl.deleted_at IS NULL
			AND pl.valid_from <= ? AND pl.valid_until > ?
		WHERE p.deleted_at IS NULL
			AND p.visibility = ?
			AND (p.shadowban_reason IS NULL OR p.shadowban_reason = ?)
			AND p.publisher_id NOT IN (
				SELECT id FROM publishers
				WHERE shadowban_reason IS NOT NULL AND shadowban_reason <> 0
			)
		ORDER BY pl.valid_from DESC
		LIMIT 1`
	err := p.h.db.WithContext(ctx).Raw(query, now, now,
		int(model.VisibilityPublic), int(model.ShadowbanNone)).Scan(&ent).Error
	if err != nil {
		return nil, err
	}
	if ent.ID == uuid.Nil {
		return nil, nil
	}
	post, err := p.h.hydrateOne(ctx, &ent, nil, true, false)
	if err != nil {
		return nil, err
	}
	post.Sponsored = true
	return &post, nil
}

// MarkSponsoredShown bumps the impression counters for a sponsored post.
func (p *signalsPort) MarkSponsoredShown(ctx context.Context, postID string) error {
	id, err := uuid.Parse(postID)
	if err != nil || id == uuid.Nil {
		return fmt.Errorf("posts: invalid post id %q", postID)
	}
	now := time.Now().UTC()
	result := p.h.db.WithContext(ctx).Model(&store.PostAggregatedStatsEntity{}).
		Where("post_id = ?", id).
		Updates(map[string]any{
			"shown_count":   gorm.Expr("shown_count + 1"),
			"last_shown_at": now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 {
		return nil
	}
	stats := store.PostAggregatedStatsEntity{
		ID:          uuid.New(),
		PostID:      id,
		ShownCount:  1,
		LastShownAt: &now,
	}
	return p.h.db.WithContext(ctx).Create(&stats).Error
}

// claimUniqueView reports whether this account should count as a unique view.
func (h *handler) claimUniqueView(ctx context.Context, postID, accountID uuid.UUID) bool {
	key := fmt.Sprintf("post:view:%s:%s", postID, accountID)
	if h.core != nil && h.core.Redis != nil {
		var seen bool
		found, err := h.core.Redis.Get(ctx, key, &seen)
		if err == nil && found {
			return false
		}
		if err := h.core.Redis.Set(ctx, key, true, viewDedupTTL); err != nil {
			h.log.Warn("posts: record view dedup failed", "key", key, "error", err)
		}
		return true
	}

	now := time.Now().UTC()
	h.viewMu.Lock()
	defer h.viewMu.Unlock()
	if h.viewDedup == nil {
		h.viewDedup = map[string]time.Time{}
	}
	if expiresAt, ok := h.viewDedup[key]; ok {
		if expiresAt.After(now) {
			return false
		}
		delete(h.viewDedup, key)
	}
	h.viewDedup[key] = now.Add(viewDedupTTL)
	if len(h.viewDedup) > 10000 {
		for candidate, expiresAt := range h.viewDedup {
			if !expiresAt.After(now) {
				delete(h.viewDedup, candidate)
			}
		}
	}
	return true
}

// enqueueViewSignal adds a signal-driven view to the periodic flush buffer.
func (h *handler) enqueueViewSignal(postID, accountID uuid.UUID, unique bool) {
	h.viewMu.Lock()
	defer h.viewMu.Unlock()
	bucket, ok := h.views[postID]
	if !ok {
		bucket = &viewBucket{unique: map[string]struct{}{}}
		h.views[postID] = bucket
	}
	bucket.total++
	if unique && accountID != uuid.Nil {
		bucket.unique[accountID.String()] = struct{}{}
	}
}

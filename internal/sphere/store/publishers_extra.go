package store

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// PublisherByNameCI loads a publisher by its case-insensitive name. The C#
// controllers consistently compare `Name.ToLower()` against the lowercased
// route value, so every lookup goes through this helper.
func (s *Store) PublisherByNameCI(ctx context.Context, name string) (*PublisherEntity, error) {
	var entity PublisherEntity
	if err := s.DB.WithContext(ctx).
		Where("lower(name) = ?", strings.ToLower(name)).
		First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return &entity, nil
}

// PublisherNamesTaken reports whether a case-insensitive name already exists,
// optionally excluding one publisher id.
func (s *Store) PublisherNamesTaken(ctx context.Context, name string, except uuid.UUID) (bool, error) {
	query := s.DB.WithContext(ctx).Model(&PublisherEntity{}).
		Where("lower(name) = ?", strings.ToLower(name))
	if except != uuid.Nil {
		query = query.Where("id <> ?", except)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// PublisherMemberRow is a membership row including the ModelBase timestamps the
// legacy table carries but PublisherMemberEntity does not map.
type PublisherMemberRow struct {
	PublisherID uuid.UUID      `gorm:"column:publisher_id"`
	AccountID   uuid.UUID      `gorm:"column:account_id"`
	Role        int            `gorm:"column:role"`
	JoinedAt    *time.Time     `gorm:"column:joined_at"`
	CreatedAt   time.Time      `gorm:"column:created_at"`
	UpdatedAt   time.Time      `gorm:"column:updated_at"`
	DeletedAt   gorm.DeletedAt `gorm:"column:deleted_at"`
}

// TableName pins the row to the legacy table.
func (PublisherMemberRow) TableName() string { return "publisher_members" }

// PublisherMemberRows lists live membership rows ordered by creation time.
func (s *Store) PublisherMemberRows(ctx context.Context, publisherID uuid.UUID, joinedOnly bool, offset, take int) ([]PublisherMemberRow, int64, error) {
	query := s.DB.WithContext(ctx).Model(&PublisherMemberRow{}).
		Where("publisher_id = ?", publisherID).
		Where("deleted_at IS NULL")
	if joinedOnly {
		query = query.Where("joined_at IS NOT NULL")
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []PublisherMemberRow
	if err := query.Order("created_at ASC").Offset(offset).Limit(take).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// PublisherCounts aggregates the public statistics payload counters.
type PublisherCounts struct {
	Posts        int
	Upvotes      int
	Downvotes    int
	StickerPacks int
	Stickers     int
	Subscribers  int
}

// PublisherCountsFor computes the public counters in one round trip per table.
func (s *Store) PublisherCountsFor(ctx context.Context, publisherID uuid.UUID) (PublisherCounts, error) {
	var counts PublisherCounts
	db := s.DB.WithContext(ctx)


	var posts int64
	if err := db.Model(&PostEntity{}).Where("publisher_id = ?", publisherID).Count(&posts).Error; err != nil {
		return counts, err
	}
	counts.Posts = int(posts)

	var reactions []struct {
		Attitude int
		Total    int64
	}
	if err := db.Model(&PostReactionEntity{}).
		Select("attitude, count(*) AS total").
		Where("post_id IN (?)", db.Model(&PostEntity{}).Select("id").Where("publisher_id = ?", publisherID)).
		Group("attitude").
		Scan(&reactions).Error; err != nil {
		return counts, err
	}
	for _, row := range reactions {
		switch row.Attitude {
		case 1:
			counts.Upvotes = int(row.Total)
		case 2:
			counts.Downvotes = int(row.Total)
		}
	}

	var packIDs []uuid.UUID
	if err := db.Model(&StickerPackEntity{}).Where("publisher_id = ?", publisherID).Pluck("id", &packIDs).Error; err != nil {
		return counts, err
	}
	counts.StickerPacks = len(packIDs)
	if len(packIDs) > 0 {
		var stickers int64
		if err := db.Model(&StickerEntity{}).Where("pack_id IN ?", packIDs).Count(&stickers).Error; err != nil {
			return counts, err
		}
		counts.Stickers = int(stickers)
	}

	var subscribers int64
	if err := db.Model(&PublisherSubscriptionEntity{}).
		Where("publisher_id = ? AND ended_at IS NULL AND state = 1 AND is_blocking = ?", publisherID, false).
		Count(&subscribers).Error; err != nil {
		return counts, err
	}
	counts.Subscribers = int(subscribers)
	return counts, nil
}

// PublisherContentStatsRow is the aggregated writing statistics row.
type PublisherContentStatsRow struct {
	PostsCount        int        `gorm:"column:posts_count"`
	WordsCount        int64      `gorm:"column:words_count"`
	AttachmentsCount  int64      `gorm:"column:attachments_count"`
	DaysPostedCount   int        `gorm:"column:days_posted_count"`
	LongestStreakDays int        `gorm:"column:longest_streak_days"`
	FirstPostedAt     *time.Time `gorm:"column:first_posted_at"`
	LastPostedAt      *time.Time `gorm:"column:last_posted_at"`
}

// PublisherContentStats runs the same aggregation as PublisherService
// .GetPublisherContentStats: published, non-draft, non-deleted posts only.
func (s *Store) PublisherContentStats(ctx context.Context, publisherID uuid.UUID) (*PublisherContentStatsRow, error) {
	var row PublisherContentStatsRow
	err := s.DB.WithContext(ctx).Raw(`
		WITH published_posts AS (
			SELECT
				(published_at AT TIME ZONE 'UTC')::date AS day,
				published_at,
				CASE
					WHEN content IS NULL OR btrim(content) = '' THEN 0
					ELSE array_length(
						regexp_split_to_array(
							btrim(regexp_replace(content, '\s+', ' ', 'g')),
							' '
						),
						1
					)
				END AS word_count,
				jsonb_array_length(attachments) AS attachment_count
			FROM posts
			WHERE publisher_id = ?
				AND deleted_at IS NULL
				AND drafted_at IS NULL
				AND published_at IS NOT NULL
				AND published_at <= now()
		),
		daily AS (
			SELECT
				day,
				COUNT(*) AS posts_count,
				SUM(word_count) AS words_count,
				SUM(attachment_count) AS attachments_count,
				MIN(published_at) AS first_posted_at,
				MAX(published_at) AS last_posted_at
			FROM published_posts
			GROUP BY day
		),
		streaks AS (
			SELECT COUNT(*) AS length
			FROM (
				SELECT day - (ROW_NUMBER() OVER (ORDER BY day))::int AS streak_group
				FROM daily
			) groups
			GROUP BY streak_group
		)
		SELECT
			COALESCE(SUM(daily.posts_count), 0)::int AS posts_count,
			COALESCE(SUM(daily.words_count), 0)::bigint AS words_count,
			COALESCE(SUM(daily.attachments_count), 0)::bigint AS attachments_count,
			COUNT(daily.day)::int AS days_posted_count,
			COALESCE((SELECT MAX(length) FROM streaks), 0)::int AS longest_streak_days,
			MIN(daily.first_posted_at) AS first_posted_at,
			MAX(daily.last_posted_at) AS last_posted_at
		FROM daily
	`, publisherID).Scan(&row).Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// PublisherHeatmapRow is one day of post activity.
type PublisherHeatmapRow struct {
	Day   time.Time `gorm:"column:day"`
	Count int       `gorm:"column:count"`
}

// PublisherHeatmap groups post creation dates over a window.
func (s *Store) PublisherHeatmap(ctx context.Context, publisherID uuid.UUID, from, to time.Time) ([]PublisherHeatmapRow, error) {
	var rows []PublisherHeatmapRow
	err := s.DB.WithContext(ctx).Raw(`
		SELECT (created_at AT TIME ZONE 'UTC')::date AS day, COUNT(*)::int AS count
		FROM posts
		WHERE publisher_id = ?
			AND deleted_at IS NULL
			AND created_at >= ?
			AND created_at <= ?
		GROUP BY day
		ORDER BY day ASC
	`, publisherID, from, to).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// LatestContentAt returns the newest public post or public live stream per
// publisher. Publishers with no content are absent from the map.
func (s *Store) LatestContentAt(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]time.Time, error) {
	result := map[uuid.UUID]time.Time{}
	if len(ids) == 0 {
		return result, nil
	}
	db := s.DB.WithContext(ctx)

	var posts []struct {
		PublisherID uuid.UUID `gorm:"column:publisher_id"`
		Latest      time.Time `gorm:"column:latest"`
	}
	if err := db.Model(&PostEntity{}).
		Select("publisher_id, max(coalesce(published_at, created_at)) AS latest").
		Where("publisher_id IN ?", ids).
		Where("replied_post_id IS NULL").
		Where("visibility = ?", 0).
		Group("publisher_id").
		Scan(&posts).Error; err != nil {
		return nil, err
	}
	for _, row := range posts {
		result[row.PublisherID] = row.Latest
	}

	var streams []struct {
		PublisherID uuid.UUID `gorm:"column:publisher_id"`
		Latest      time.Time `gorm:"column:latest"`
	}
	if err := db.Model(&LiveStreamEntity{}).
		Select("publisher_id, max(coalesce(started_at, created_at)) AS latest").
		Where("publisher_id IN ?", ids).
		Where("visibility = ?", 0).
		Where("started_at IS NOT NULL").
		Group("publisher_id").
		Scan(&streams).Error; err != nil {
		return nil, err
	}
	for _, row := range streams {
		if current, ok := result[row.PublisherID]; !ok || row.Latest.After(current) {
			result[row.PublisherID] = row.Latest
		}
	}
	return result, nil
}

// ActiveLivePublisherIDs lists publishers with a running, public stream.
func (s *Store) ActiveLivePublisherIDs(ctx context.Context, ids []uuid.UUID) ([]uuid.UUID, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var publisherIDs []uuid.UUID
	err := s.DB.WithContext(ctx).Model(&LiveStreamEntity{}).
		Where("publisher_id IN ?", ids).
		Where("status = ?", 1).
		Distinct().
		Pluck("publisher_id", &publisherIDs).Error
	return publisherIDs, err
}

// PublisherLeaderboardRow is one leaderboard candidate.
type PublisherLeaderboardRow struct {
	ID      uuid.UUID      `gorm:"column:id"`
	Name    string         `gorm:"column:name"`
	Nick    string         `gorm:"column:nick"`
	Picture datatypes.JSON `gorm:"column:picture"`
	Rating  float64        `gorm:"column:rating"`
}

// PublisherLeaderboard lists every local publisher ordered by rating.
func (s *Store) PublisherLeaderboard(ctx context.Context) ([]PublisherLeaderboardRow, error) {
	var rows []PublisherLeaderboardRow
	err := s.DB.WithContext(ctx).Model(&PublisherEntity{}).
		Select("id, name, nick, picture, rating").
		Where("type <> ?", 2).
		Order("rating DESC").
		Scan(&rows).Error
	return rows, err
}

// PublisherLeaderboardTotal counts the publishers eligible for the leaderboard.
func (s *Store) PublisherLeaderboardTotal(ctx context.Context) (int64, error) {
	var total int64
	err := s.DB.WithContext(ctx).Model(&PublisherEntity{}).
		Where("type <> ?", 2).
		Count(&total).Error
	return total, err
}

// PublisherRatingTotal sums the append-only rating deltas for a publisher.
func (s *Store) PublisherRatingTotal(ctx context.Context, publisherID uuid.UUID) (float64, error) {
	var total float64
	err := s.DB.WithContext(ctx).Model(&PublisherRatingRecordEntity{}).
		Select("COALESCE(SUM(delta), 0)").
		Where("publisher_id = ?", publisherID).
		Scan(&total).Error
	return total, err
}

// SetPublisherRating writes the cached rating column.
func (s *Store) SetPublisherRating(ctx context.Context, publisherID uuid.UUID, rating float64) error {
	return s.DB.WithContext(ctx).Model(&PublisherEntity{}).
		Where("id = ?", publisherID).
		Update("rating", rating).Error
}

// AdvertisingPostStatsRow mirrors the sponsor service's advertising payload.
type AdvertisingPostStatsRow struct {
	PostID            uuid.UUID  `gorm:"column:post_id"`
	Title             *string    `gorm:"column:title"`
	Slug              *string    `gorm:"column:slug"`
	ActiveBidTotal    float64    `gorm:"column:active_bid_total"`
	BidCount          int        `gorm:"column:bid_count"`
	IsCurrentlyPlaced bool       `gorm:"column:is_currently_placed"`
	ShownCount        int64      `gorm:"column:shown_count"`
	LastShownAt       *time.Time `gorm:"column:last_shown_at"`
}

// AdvertisingPostStats lists the publisher's sponsored post counters.
func (s *Store) AdvertisingPostStats(ctx context.Context, publisherID uuid.UUID) ([]AdvertisingPostStatsRow, error) {
	var rows []AdvertisingPostStatsRow
	err := s.DB.WithContext(ctx).Raw(`
		SELECT
			s.post_id,
			p.title,
			p.slug,
			s.active_bid_total,
			s.active_bid_count AS bid_count,
			s.is_currently_placed,
			s.shown_count,
			s.last_shown_at
		FROM post_aggregated_stats s
		JOIN posts p ON p.id = s.post_id
		WHERE p.publisher_id = ?
			AND p.deleted_at IS NULL
			AND (s.active_bid_total > 0 OR s.shown_count > 0)
		ORDER BY s.active_bid_total DESC, s.shown_count DESC
	`, publisherID).Scan(&rows).Error
	return rows, err
}

// PublisherPostStat is the per-post input to the reward calculation.
type PublisherPostStat struct {
	ID           uuid.UUID `gorm:"column:id"`
	PublisherID  uuid.UUID `gorm:"column:publisher_id"`
	AwardedScore float64   `gorm:"column:awarded_score"`
}

// PostsCreatedBetween lists the posts used by the reward settlement windows.
func (s *Store) PostsCreatedBetween(ctx context.Context, from, to time.Time, publisherID *uuid.UUID) ([]PublisherPostStat, error) {
	query := s.DB.WithContext(ctx).Model(&PostEntity{}).
		Select("id, publisher_id, awarded_score").
		Where("created_at >= ? AND created_at <= ?", from, to)
	if publisherID != nil {
		query = query.Where("publisher_id = ?", *publisherID)
	}
	var rows []PublisherPostStat
	err := query.Scan(&rows).Error
	return rows, err
}

// ReactionCount is the number of reactions of one attitude on a post.
type ReactionCount struct {
	PostID   uuid.UUID `gorm:"column:post_id"`
	Attitude int       `gorm:"column:attitude"`
	Total    int       `gorm:"column:total"`
}

// ReactionCountsForPosts counts reactions grouped by post and attitude.
func (s *Store) ReactionCountsForPosts(ctx context.Context, postIDs []uuid.UUID) ([]ReactionCount, error) {
	if len(postIDs) == 0 {
		return nil, nil
	}
	var rows []ReactionCount
	err := s.DB.WithContext(ctx).Model(&PostReactionEntity{}).
		Select("post_id, attitude, COUNT(*)::int AS total").
		Where("post_id IN ?", postIDs).
		Group("post_id, attitude").
		Scan(&rows).Error
	return rows, err
}

// PostAwardWithPublisher is an unsettled positive award with its publisher.
type PostAwardWithPublisher struct {
	ID          uuid.UUID `gorm:"column:id"`
	Amount      float64   `gorm:"column:amount"`
	PostID      uuid.UUID `gorm:"column:post_id"`
	PublisherID uuid.UUID `gorm:"column:publisher_id"`
}

// UnsettledPositiveAwards lists awards Wallet has not settled yet.
func (s *Store) UnsettledPositiveAwards(ctx context.Context) ([]PostAwardWithPublisher, error) {
	var rows []PostAwardWithPublisher
	err := s.DB.WithContext(ctx).Raw(`
		SELECT a.id, a.amount, a.post_id, p.publisher_id
		FROM post_awards a
		JOIN posts p ON p.id = a.post_id
		WHERE a.settled_at IS NULL
			AND a.deleted_at IS NULL
			AND a.attitude = 1
	`).Scan(&rows).Error
	return rows, err
}

// MarkAwardsSettled stamps settled_at on the supplied awards.
func (s *Store) MarkAwardsSettled(ctx context.Context, ids []uuid.UUID, at time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	return s.DB.WithContext(ctx).Model(&PostAwardEntity{}).
		Where("id IN ?", ids).
		Update("settled_at", at).Error
}

// PublisherMemberAccountIDs lists the account ids that belong to a publisher.
func (s *Store) PublisherMemberAccountIDs(ctx context.Context, publisherID uuid.UUID) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	err := s.DB.WithContext(ctx).Model(&PublisherMemberRow{}).
		Where("publisher_id = ?", publisherID).
		Where("deleted_at IS NULL").
		Pluck("account_id", &ids).Error
	return ids, err
}

// PublisherMemberRoleFor returns the role of an account in a publisher, or
// false when there is no live membership.
func (s *Store) PublisherMemberRoleFor(ctx context.Context, publisherID, accountID uuid.UUID) (int, bool, error) {
	var row PublisherMemberRow
	err := s.DB.WithContext(ctx).Model(&PublisherMemberRow{}).
		Where("publisher_id = ? AND account_id = ?", publisherID, accountID).
		Where("deleted_at IS NULL").
		First(&row).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return 0, false, nil
		}
		return 0, false, err
	}
	return row.Role, true, nil
}

// ExpiredPendingFollowRequests returns pending follow requests older than the
// cutoff, matching the cleanup job.
func (s *Store) ExpiredPendingFollowRequests(ctx context.Context, before time.Time) ([]PublisherSubscriptionEntity, error) {
	var rows []PublisherSubscriptionEntity
	err := s.DB.WithContext(ctx).
		Where("state = ? AND ended_at IS NULL AND created_at < ?", 0, before).
		Find(&rows).Error
	return rows, err
}

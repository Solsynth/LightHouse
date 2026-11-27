package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
)

// ErrSuggestionOwn is returned when an account votes on its own contribution.
// A contributor confirming itself is not evidence.
var ErrSuggestionOwn = errors.New("cannot vote on your own contribution")

// ─────────────────────────── Shared lyrics ───────────────────────────

// UpsertSharedLyric writes one account's lyric contribution for one catalog
// item and language, replacing that account's earlier text. A re-send is an
// edit of the same opinion, so the row keeps its id and its standing.
func (s *Store) UpsertSharedLyric(ctx context.Context, lyric *model.SharedLyric) (*model.SharedLyric, bool, error) {
	db := s.DB.WithContext(ctx)
	var existing model.SharedLyric
	err := db.Where("catalog_id = ? AND account_id = ? AND language = ?",
		lyric.CatalogID, lyric.AccountID, lyric.Language).Take(&existing).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		lyric.ID = uuid.New()
		lyric.CreatedAt = time.Now().UTC()
		lyric.UpdatedAt = lyric.CreatedAt
		if err := db.Create(lyric).Error; err != nil {
			return nil, false, err
		}
		return lyric, true, nil
	case err != nil:
		return nil, false, err
	}

	now := time.Now().UTC()
	updates := map[string]any{
		"lines":                lyric.Lines,
		"synced":               lyric.Synced,
		"translation_language": lyric.TranslationLanguage,
		"translation_engine":   lyric.TranslationEngine,
		"origin":               lyric.Origin,
		"remote_id":            lyric.RemoteID,
		"updated_at":           now,
	}
	if err := db.Model(&model.SharedLyric{}).Where("id = ?", existing.ID).
		Updates(updates).Error; err != nil {
		return nil, false, err
	}
	if err := db.Take(&existing, "id = ?", existing.ID).Error; err != nil {
		return nil, false, err
	}
	return &existing, false, nil
}

// GetSharedLyric fetches one contribution by id.
func (s *Store) GetSharedLyric(ctx context.Context, id uuid.UUID) (*model.SharedLyric, error) {
	var lyric model.SharedLyric
	if err := s.DB.WithContext(ctx).Take(&lyric, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &lyric, nil
}

// DeleteSharedLyric removes one account's own contribution. Anyone else's is
// left alone: ownership is what lets an account withdraw its words.
func (s *Store) DeleteSharedLyric(ctx context.Context, accountID, id uuid.UUID) error {
	db := s.DB.WithContext(ctx)
	return db.Transaction(func(tx *gorm.DB) error {
		result := tx.Delete(&model.SharedLyric{}, "id = ? AND account_id = ?", id, accountID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return tx.Delete(&model.SharedLyricVote{}, "lyric_id = ?", id).Error
	})
}

// SharedLyricQuery filters a suggestion listing.
type SharedLyricQuery struct {
	CatalogID uuid.UUID
	Language  string
	// SyncedOnly keeps only timed lyrics — what a pane that follows the
	// transport can actually draw.
	SyncedOnly bool
	// ExcludeAccount drops one account's own rows, for "what other listeners
	// picked" listings.
	ExcludeAccount *uuid.UUID
}

// ListSharedLyrics returns the ranked suggestions for one catalog item and the
// total number of contributions the query matched.
//
// The order is the standing, then freshness: a contribution several listeners
// kept beats one nobody has, and two equally kept ones are resolved by which is
// newer. A synced contribution sits above a plain one of the same standing,
// because timed words are strictly more useful to a playback pane.
func (s *Store) ListSharedLyrics(ctx context.Context, q SharedLyricQuery, offset, take int) ([]model.SharedLyric, int64, error) {
	query := s.DB.WithContext(ctx).Model(&model.SharedLyric{}).
		Where("catalog_id = ?", q.CatalogID)
	if q.Language != "" {
		query = query.Where("LOWER(language) = ?", strings.ToLower(q.Language))
	}
	if q.SyncedOnly {
		query = query.Where("synced = ?", true)
	}
	if q.ExcludeAccount != nil {
		query = query.Where("account_id <> ?", *q.ExcludeAccount)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var rows []model.SharedLyric
	if err := query.
		Order("score DESC, synced DESC, adoption_count DESC, updated_at DESC").
		Offset(offset).Limit(take).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// AdoptSharedLyric records one account taking a contribution as its words for
// the recording. The first adopter is a vote: the contribution's standing and
// adoption count move together, so a suggestion list reflects what listeners
// actually kept rather than only what they clicked.
//
// Adoption is idempotent per account by the unique vote row: a second device
// adopting the same words is not a second listener.
func (s *Store) AdoptSharedLyric(ctx context.Context, accountID, lyricID uuid.UUID) (*model.SharedLyric, error) {
	db := s.DB.WithContext(ctx)
	var lyric model.SharedLyric
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Take(&lyric, "id = ?", lyricID).Error; err != nil {
			return err
		}
		var existing model.SharedLyricVote
		err := tx.Where("lyric_id = ? AND account_id = ?", lyricID, accountID).Take(&existing).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			if err := tx.Create(&model.SharedLyricVote{
				ID:        uuid.New(),
				LyricID:   lyricID,
				AccountID: accountID,
				Value:     1,
			}).Error; err != nil {
				return err
			}
		case err != nil:
			return err
		case existing.Value == 1:
			// Already adopted by this account: nothing left to count.
			return nil
		default:
			// A thumb-down turning into an adoption is a change of mind; the
			// vote moves, the adoption is counted.
			if err := tx.Model(&model.SharedLyricVote{}).Where("id = ?", existing.ID).
				Updates(map[string]any{"value": 1, "updated_at": time.Now().UTC()}).Error; err != nil {
				return err
			}
		}
		if err := recomputeSharedLyricScore(tx, lyricID); err != nil {
			return err
		}
		return tx.Take(&lyric, "id = ?", lyricID).Error
	})
	if err != nil {
		return nil, err
	}
	return &lyric, nil
}

// VoteSharedLyric records a thumb on a contribution. value must be +1 or -1.
// An account cannot vote on its own contribution: a contributor confirming
// itself would let one listener promote its own text above everybody's.
func (s *Store) VoteSharedLyric(ctx context.Context, accountID, lyricID uuid.UUID, value int) (*model.SharedLyric, error) {
	if value != 1 && value != -1 {
		return nil, fmt.Errorf("vote value must be 1 or -1")
	}
	db := s.DB.WithContext(ctx)
	var lyric model.SharedLyric
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Take(&lyric, "id = ?", lyricID).Error; err != nil {
			return err
		}
		if lyric.AccountID == accountID {
			return ErrSuggestionOwn
		}
		vote := model.SharedLyricVote{
			ID:        uuid.New(),
			LyricID:   lyricID,
			AccountID: accountID,
			Value:     value,
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "lyric_id"}, {Name: "account_id"}},
			DoUpdates: clause.Assignments(map[string]any{"value": value, "updated_at": time.Now().UTC()}),
		}).Create(&vote).Error; err != nil {
			return err
		}
		if err := recomputeSharedLyricScore(tx, lyricID); err != nil {
			return err
		}
		return tx.Take(&lyric, "id = ?", lyricID).Error
	})
	if err != nil {
		return nil, err
	}
	return &lyric, nil
}

// recomputeSharedLyricScore rebuilds one contribution's counters from its
// votes, in the caller's transaction. Recomputed rather than incremented so a
// retried request, a replacement vote and a withdrawal all converge on the
// same numbers.
//
// A positive vote is an adoption — adopting writes one, and a listener who
// thumbs a lyric up is saying the same thing — so both counters read the same
// rows. A downvote lowers the standing without ever counting as a keeper.
func recomputeSharedLyricScore(tx *gorm.DB, lyricID uuid.UUID) error {
	var counts struct {
		Up   int `gorm:"column:up"`
		Down int `gorm:"column:down"`
	}
	if err := tx.Model(&model.SharedLyricVote{}).
		Select("SUM(CASE WHEN value > 0 THEN 1 ELSE 0 END) AS up, SUM(CASE WHEN value < 0 THEN 1 ELSE 0 END) AS down").
		Where("lyric_id = ?", lyricID).Scan(&counts).Error; err != nil {
		return err
	}
	return tx.Model(&model.SharedLyric{}).Where("id = ?", lyricID).
		Updates(map[string]any{
			"upvotes":        counts.Up,
			"downvotes":      counts.Down,
			"adoption_count": counts.Up,
			"score":          counts.Up - counts.Down,
		}).Error
}

// ─────────────────────────── Metadata suggestions ───────────────────────────

// UpsertMetadataSuggestion writes one account's proposal for one field of one
// catalog item, replacing its earlier value for that field.
func (s *Store) UpsertMetadataSuggestion(ctx context.Context, suggestion *model.MetadataSuggestion) (*model.MetadataSuggestion, bool, error) {
	db := s.DB.WithContext(ctx)
	var existing model.MetadataSuggestion
	err := db.Where("catalog_id = ? AND account_id = ? AND field = ?",
		suggestion.CatalogID, suggestion.AccountID, suggestion.Field).Take(&existing).Error
	now := time.Now().UTC()
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		suggestion.ID = uuid.New()
		// The author is the first account standing behind the value, so its
		// own proposal is already one vote; [VoteMetadataSuggestion] adds the
		// rest on top of that same starting count.
		suggestion.Score = 1
		suggestion.Upvotes = 1
		suggestion.CreatedAt = now
		suggestion.UpdatedAt = now
		if err := db.Create(suggestion).Error; err != nil {
			return nil, false, err
		}
		return suggestion, true, nil
	case err != nil:
		return nil, false, err
	}

	// A new value is a new proposal: the standing earned by the old one does
	// not carry over, so the replacement restarts at the author's own vote.
	if existing.Value != suggestion.Value && !strings.EqualFold(existing.Value, suggestion.Value) {
		if err := db.Model(&model.MetadataSuggestion{}).Where("id = ?", existing.ID).
			Updates(map[string]any{
				"value":      suggestion.Value,
				"score":      1,
				"upvotes":    1,
				"downvotes":  0,
				"updated_at": now,
			}).Error; err != nil {
			return nil, false, err
		}
		if err := db.Delete(&model.MetadataSuggestionVote{}, "suggestion_id = ?", existing.ID).Error; err != nil {
			return nil, false, err
		}
	} else if err := db.Model(&model.MetadataSuggestion{}).Where("id = ?", existing.ID).
		Updates(map[string]any{"updated_at": now}).Error; err != nil {
		return nil, false, err
	}

	var saved model.MetadataSuggestion
	if err := db.Take(&saved, "id = ?", existing.ID).Error; err != nil {
		return nil, false, err
	}
	return &saved, false, nil
}

// VoteMetadataSuggestion records a thumb on somebody else's suggestion.
func (s *Store) VoteMetadataSuggestion(ctx context.Context, accountID, suggestionID uuid.UUID, value int) (*model.MetadataSuggestion, error) {
	if value != 1 && value != -1 {
		return nil, fmt.Errorf("vote value must be 1 or -1")
	}
	db := s.DB.WithContext(ctx)
	var suggestion model.MetadataSuggestion
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Take(&suggestion, "id = ?", suggestionID).Error; err != nil {
			return err
		}
		if suggestion.AccountID == accountID {
			return ErrSuggestionOwn
		}
		vote := model.MetadataSuggestionVote{
			ID:           uuid.New(),
			SuggestionID: suggestionID,
			AccountID:    accountID,
			Value:        value,
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "suggestion_id"}, {Name: "account_id"}},
			DoUpdates: clause.Assignments(map[string]any{"value": value, "updated_at": time.Now().UTC()}),
		}).Create(&vote).Error; err != nil {
			return err
		}
		var counts struct {
			Up   int `gorm:"column:up"`
			Down int `gorm:"column:down"`
		}
		if err := tx.Model(&model.MetadataSuggestionVote{}).
			Select("SUM(CASE WHEN value > 0 THEN 1 ELSE 0 END) AS up, SUM(CASE WHEN value < 0 THEN 1 ELSE 0 END) AS down").
			Where("suggestion_id = ?", suggestionID).Scan(&counts).Error; err != nil {
			return err
		}
		// The author's own proposal is one confirming vote, so the standing is
		// the author plus everybody who agrees, minus everybody who does not.
		up, down := counts.Up+1, counts.Down
		if err := tx.Model(&model.MetadataSuggestion{}).Where("id = ?", suggestionID).
			Updates(map[string]any{
				"upvotes":   up,
				"downvotes": down,
				"score":     up - down,
			}).Error; err != nil {
			return err
		}
		return tx.Take(&suggestion, "id = ?", suggestionID).Error
	})
	if err != nil {
		return nil, err
	}
	return &suggestion, nil
}

// MetadataFieldLeader is the value several accounts agree on for one field.
type MetadataFieldLeader struct {
	Field   string `json:"field"`
	Value   string `json:"value"`
	Score   int    `json:"score"`
	Support int    `json:"support"`
}

// MetadataFieldSuggestion is one value proposed for one field, with how many
// accounts stand behind it.
type MetadataFieldSuggestion struct {
	model.MetadataSuggestion
	// Support is the number of accounts that proposed this exact value,
	// counting the author. Two accounts who suggest the same string agree even
	// if neither voted on the other's row.
	Support int `json:"support"`
	// Mine is set by the handler from the caller's account: the store has no
	// session. A client draws "edit" on its own proposal and "vote" on the
	// rest, and the server refuses a self-vote either way.
	Mine bool `json:"mine"`
}

// ListMetadataSuggestions returns the ranked suggestions for one catalog item,
// optionally narrowed to one field, with per-value agreement folded in. viewer
// is the calling account, used only to mark its own groups; nil for an
// anonymous read.
//
// Rows proposing the same value are grouped: agreement on a spelling is what
// makes a suggestion a correction rather than one listener's taste, and a
// listing that repeated the same value five times would hide that.
func (s *Store) ListMetadataSuggestions(ctx context.Context, catalogID uuid.UUID, field string, viewer *uuid.UUID, offset, take int) ([]MetadataFieldSuggestion, int64, error) {
	query := s.DB.WithContext(ctx).Model(&model.MetadataSuggestion{}).
		Where("catalog_id = ?", catalogID)
	if field != "" {
		query = query.Where("field = ?", field)
	}

	// Folding happens in memory, so paging has to happen after it: a page
	// taken off the raw rows would split one value's agreements across two
	// pages and report each half as a different amount of support. The cap is
	// far above any honest listing: one record has six fields, and a page that
	// deep is not a listing anybody reads.
	var rows []model.MetadataSuggestion
	if err := query.
		Order("score DESC, updated_at DESC").
		Limit(maxFoldedSuggestions).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return s.foldSuggestions(ctx, rows, viewer, offset, take)
}

// maxFoldedSuggestions caps the rows one folded listing reads. A popular
// record may have hundreds of proposals; a listing nobody pages that far into
// does not need them all loaded.
const maxFoldedSuggestions = 2000

// MetadataFieldLeaders returns, per field, the value the most accounts agree
// on, when that agreement is worth acting on.
//
// A leader is reported only when at least two accounts proposed it: one
// account's proposal is not a consensus, and a metadata endpoint that served
// it would let any single listener rewrite a shared record.
func (s *Store) MetadataFieldLeaders(ctx context.Context, catalogID uuid.UUID, minSupport int) ([]MetadataFieldLeader, error) {
	if minSupport < 2 {
		minSupport = 2
	}
	db := s.DB.WithContext(ctx)
	var groups []struct {
		Field string `gorm:"column:field"`
		Value string `gorm:"column:value"`
		Count int    `gorm:"column:count"`
	}
	if err := db.Model(&model.MetadataSuggestion{}).
		Select("field, LOWER(value) AS value, COUNT(*) AS count").
		Where("catalog_id = ?", catalogID).
		Group("field, LOWER(value)").
		Order("count DESC").Scan(&groups).Error; err != nil {
		return nil, err
	}

	leaders := make([]MetadataFieldLeader, 0, len(groups))
	seen := map[string]bool{}
	for _, group := range groups {
		if group.Count < minSupport || seen[group.Field] {
			continue
		}
		seen[group.Field] = true
		// The displayed spelling is the most-supported exact string, not the
		// lowercased grouping key.
		var best model.MetadataSuggestion
		if err := db.Where("catalog_id = ? AND field = ? AND LOWER(value) = ?",
			catalogID, group.Field, group.Value).
			Order("score DESC, updated_at DESC").Take(&best).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		leaders = append(leaders, MetadataFieldLeader{
			Field:   group.Field,
			Value:   best.Value,
			Score:   best.Score,
			Support: group.Count,
		})
	}
	return leaders, nil
}

// foldSuggestions groups live rows by (field, value) and returns the top take
// groups after offset, each represented by its highest-scoring row. A group is
// the caller's when any of its members is — an account that proposed the same
// value as two others still sees the row as its own.
func (s *Store) foldSuggestions(ctx context.Context, rows []model.MetadataSuggestion, viewer *uuid.UUID, offset, take int) ([]MetadataFieldSuggestion, int64, error) {
	type groupKey struct{ field, value string }
	groups := make(map[groupKey][]model.MetadataSuggestion)
	order := make([]groupKey, 0, len(rows))
	for _, row := range rows {
		key := groupKey{field: row.Field, value: strings.ToLower(strings.TrimSpace(row.Value))}
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], row)
	}

	folded := make([]MetadataFieldSuggestion, 0, len(order))
	for _, key := range order {
		members := groups[key]
		best := members[0]
		score := 0
		mine := false
		for _, member := range members {
			score += member.Score
			if viewer != nil && member.AccountID == *viewer {
				mine = true
			}
			if member.Score > best.Score || (member.Score == best.Score && member.UpdatedAt.After(best.UpdatedAt)) {
				best = member
			}
		}
		best.Score = score
		folded = append(folded, MetadataFieldSuggestion{
			MetadataSuggestion: best,
			Support:            len(members),
			Mine:               mine,
		})
	}

	// Groups are re-ranked after folding: three accounts proposing one value
	// is stronger than one account's row that happened to score higher alone.
	sort.SliceStable(folded, func(i, j int) bool {
		if folded[i].Support != folded[j].Support {
			return folded[i].Support > folded[j].Support
		}
		return folded[i].Score > folded[j].Score
	})

	total := int64(len(folded))
	if offset >= len(folded) {
		return []MetadataFieldSuggestion{}, total, nil
	}
	end := offset + take
	if end > len(folded) {
		end = len(folded)
	}
	return folded[offset:end], total, nil
}

// FindCatalogItemByProviderKey resolves the record one client's track key
// names. The key is scoped by the writer's provider, and a key with no row is
// a recording nobody has shared yet rather than an error worth retrying.
func (s *Store) FindCatalogItemByProviderKey(ctx context.Context, provider, providerKey string) (*model.CatalogItem, error) {
	var item model.CatalogItem
	err := s.DB.WithContext(ctx).
		Where("provider = ? AND provider_key = ?", strings.ToLower(strings.TrimSpace(provider)), providerKey).
		First(&item).Error
	if err != nil {
		return nil, err
	}
	return &item, nil
}

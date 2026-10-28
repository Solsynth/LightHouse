// Package rewind implements Sphere's half of the yearly Rewind aggregation.
// Passport asks every service for one year's highlight document over gRPC and
// stitches the answers together, so the payload keys below are the contract.
package rewind

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"
	"unicode"

	"google.golang.org/grpc"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/lighthouse/internal/platform/module"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/core"
)

// ServiceID is the key Passport files this document under.
const ServiceID = "sphere"

// Factory returns the rewind module factory.
func Factory() module.Factory[core.Services] {
	return module.Factory[core.Services]{
		ID:             "app.solian.sphere.rewind",
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Requires:       []string{"app.solian.sphere.posts", "app.solian.sphere.publishers"},
		Capabilities:   []string{"rewind"},
		New:            func() module.Instance[core.Services] { return &instance{} },
	}
}

type instance struct{ core *core.Services }

func (i *instance) Configure(_ context.Context, host *module.Host[core.Services]) error {
	i.core = host.Core
	gen.RegisterDyRewindServiceServer(host.GRPC, &server{core: host.Core})
	return nil
}

func (i *instance) Stop(context.Context) error { return nil }

type server struct {
	gen.UnimplementedDyRewindServiceServer
	core *core.Services
}

// yearWindow mirrors the C# window: [year-1-12-26, year-12-26).
func yearWindow(year int) (time.Time, time.Time) {
	start := time.Date(year-1, time.December, 26, 0, 0, 0, 0, time.UTC)
	end := time.Date(year, time.December, 26, 0, 0, 0, 0, time.UTC)
	return start, end
}

// GetRewindEvent builds the account's Sphere highlights for one year.
func (s *server) GetRewindEvent(ctx context.Context, req *gen.DyRequestRewindEvent) (*gen.DyRewindEvent, error) {
	accountID := strings.TrimSpace(req.GetAccountId())
	if accountID == "" {
		return &gen.DyRewindEvent{ServiceId: ServiceID}, nil
	}
	start, end := yearWindow(int(req.GetYear()))

	data := map[string]any{
		"total_post_count":     int64(0),
		"total_upvote_count":   int64(0),
		"top_words":            []any{},
		"most_popular_post":    nil,
		"most_productive_day":  nil,
		"most_loved_publisher": nil,
		"most_loved_audience":  nil,
	}
	if s.core != nil && s.core.DB != nil {
		if err := s.collect(ctx, accountID, start, end, data); err != nil {
			return nil, err
		}
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	return &gen.DyRewindEvent{AccountId: accountID, ServiceId: ServiceID, Data: raw}, nil
}

func (s *server) collect(ctx context.Context, accountID string, start, end time.Time, data map[string]any) error {
	db := s.core.DB.WithContext(ctx)

	// Publishers owned by the account. Posts hang off publishers, not accounts.
	var publisherIDs []string
	if err := db.Table("publisher_members").
		Where("account_id = ?", accountID).
		Distinct().Pluck("publisher_id::text", &publisherIDs).Error; err != nil {
		return err
	}

	var totalPosts, totalUpvotes int64
	if len(publisherIDs) > 0 {
		if err := db.Raw(
			`SELECT count(*), coalesce(sum(upvotes), 0)
			 FROM posts
			 WHERE publisher_id::text IN ? AND created_at >= ? AND created_at < ? AND deleted_at IS NULL`,
			publisherIDs, start, end).Row().Scan(&totalPosts, &totalUpvotes); err != nil {
			return err
		}
	}
	data["total_post_count"] = totalPosts
	data["total_upvote_count"] = totalUpvotes

	if len(publisherIDs) == 0 {
		return nil
	}

	// Top words across the year's post text. CJK text has no spaces, so this
	// tokenizer counts whole runs of letters/digits; the C# service used a
	// Chinese segmenter, so CJK word counts differ (the shape does not).
	type postText struct {
		ID      string  `gorm:"column:id"`
		Content *string `gorm:"column:content"`
	}
	var texts []postText
	if err := db.Raw(
		`SELECT id::text AS id, content FROM posts
		 WHERE publisher_id::text IN ? AND created_at >= ? AND created_at < ?
		   AND deleted_at IS NULL AND content IS NOT NULL`,
		publisherIDs, start, end).Scan(&texts).Error; err != nil {
		return err
	}
	contents := make([]string, 0, len(texts))
	for _, text := range texts {
		if text.Content != nil {
			contents = append(contents, *text.Content)
		}
	}
	data["top_words"] = topWords(contents, 100)

	// Most popular post by upvotes.
	var popular struct {
		ID         string  `gorm:"column:id"`
		Title      *string `gorm:"column:title"`
		Slug       *string `gorm:"column:slug"`
		Content    *string `gorm:"column:content"`
		Upvotes    int     `gorm:"column:upvotes"`
		PublisherID string `gorm:"column:publisher_id"`
	}
	if err := db.Raw(
		`SELECT id::text AS id, title, slug, content, upvotes, publisher_id::text AS publisher_id
		 FROM posts
		 WHERE publisher_id::text IN ? AND created_at >= ? AND created_at < ? AND deleted_at IS NULL
		 ORDER BY upvotes DESC LIMIT 1`,
		publisherIDs, start, end).Scan(&popular).Error; err != nil {
		return err
	}
	if popular.ID != "" {
		data["most_popular_post"] = map[string]any{
			"id":           popular.ID,
			"title":        popular.Title,
			"slug":         popular.Slug,
			"content":      popular.Content,
			"upvotes":      popular.Upvotes,
			"publisher_id": popular.PublisherID,
		}
	}

	// Most productive day.
	var day struct {
		Day   time.Time `gorm:"column:day"`
		Count int       `gorm:"column:count"`
	}
	if err := db.Raw(
		`SELECT date_trunc('day', created_at) AS day, count(*) AS count
		 FROM posts
		 WHERE publisher_id::text IN ? AND created_at >= ? AND created_at < ? AND deleted_at IS NULL
		 GROUP BY day ORDER BY count DESC, day ASC LIMIT 1`,
		publisherIDs, start, end).Scan(&day).Error; err != nil {
		return err
	}
	if !day.Day.IsZero() {
		data["most_productive_day"] = map[string]any{
			"date":       day.Day.UTC().Format("2006-01-02"),
			"post_count": day.Count,
		}
	}

	// Publisher that received the most upvotes.
	var lovedPublisher struct {
		PublisherID string `gorm:"column:publisher_id"`
		Name        string `gorm:"column:name"`
		Nick        string `gorm:"column:nick"`
		Upvotes     int    `gorm:"column:upvotes"`
	}
	if err := db.Raw(
		`SELECT p.publisher_id::text AS publisher_id, pub.name, pub.nick, sum(p.upvotes) AS upvotes
		 FROM posts p JOIN publishers pub ON pub.id = p.publisher_id
		 WHERE p.publisher_id::text IN ? AND p.created_at >= ? AND p.created_at < ? AND p.deleted_at IS NULL
		 GROUP BY p.publisher_id, pub.name, pub.nick
		 ORDER BY upvotes DESC LIMIT 1`,
		publisherIDs, start, end).Scan(&lovedPublisher).Error; err != nil {
		return err
	}
	if lovedPublisher.PublisherID != "" {
		data["most_loved_publisher"] = map[string]any{
			"publisher": map[string]any{
				"id":   lovedPublisher.PublisherID,
				"name": lovedPublisher.Name,
				"nick": lovedPublisher.Nick,
			},
			"upvote_counts": lovedPublisher.Upvotes,
		}
	}

	// Account that gave the most reactions to the account's posts.
	var lovedAudience struct {
		AccountID string `gorm:"column:account_id"`
		Count     int    `gorm:"column:count"`
	}
	if err := db.Raw(
		`SELECT r.account_id::text AS account_id, count(*) AS count
		 FROM post_reactions r JOIN posts p ON p.id = r.post_id
		 WHERE p.publisher_id::text IN ? AND p.created_at >= ? AND p.created_at < ?
		   AND r.account_id IS NOT NULL AND r.attitude = 1 AND r.deleted_at IS NULL AND p.deleted_at IS NULL
		 GROUP BY r.account_id ORDER BY count DESC LIMIT 1`,
		publisherIDs, start, end).Scan(&lovedAudience).Error; err != nil {
		return err
	}
	if lovedAudience.AccountID != "" {
		data["most_loved_audience"] = map[string]any{
			"account_id":    lovedAudience.AccountID,
			"upvote_counts": lovedAudience.Count,
		}
	}
	return nil
}

// topWords counts letter/digit runs across the posts and returns the most
// frequent ones.
func topWords(contents []string, limit int) []map[string]any {
	counts := map[string]int{}
	for _, content := range contents {
		var current strings.Builder
		flush := func() {
			word := strings.ToLower(current.String())
			current.Reset()
			if len([]rune(word)) < 2 {
				return
			}
			counts[word]++
		}
		for _, r := range content {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				current.WriteRune(r)
			} else {
				flush()
			}
		}
		flush()
	}
	type entry struct {
		word  string
		count int
	}
	entries := make([]entry, 0, len(counts))
	for word, count := range counts {
		entries = append(entries, entry{word: word, count: count})
	}
	sort.Slice(entries, func(a, b int) bool {
		if entries[a].count != entries[b].count {
			return entries[a].count > entries[b].count
		}
		return entries[a].word < entries[b].word
	})
	if len(entries) > limit {
		entries = entries[:limit]
	}
	out := make([]map[string]any, 0, len(entries))
	for _, item := range entries {
		out = append(out, map[string]any{"word": item.word, "count": item.count})
	}
	return out
}

var _ = grpc.SupportPackageIsVersion9

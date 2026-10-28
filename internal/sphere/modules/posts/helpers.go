package posts

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"src.solsynth.dev/sosys/go/pkg/errs"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/lighthouse/internal/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/internal/platform/httpx"
	"src.solsynth.dev/sosys/lighthouse/internal/platform/module"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/core"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/ports"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

// Order values accepted by the post listings.
const (
	orderDate       = "date"
	orderCreatedAt  = "created_at"
	orderPopularity = "popularity"
)

const (
	maxTitleLength       = 1024
	maxDescriptionLength = 4096
	maxSlugLength        = 1024
	maxTags              = 16
	maxCategories        = 8
	maxAttachments       = 32
	maxLanguageLength    = 16
	maxCollectionIDs     = 16
)

// handler carries the shared service graph for every route in the module.
type handler struct {
	core  *core.Services
	db    *gorm.DB
	st    *store.Store
	ports *module.PortRegistry
	log   *slog.Logger

	viewMu    sync.Mutex
	views     map[uuid.UUID]*viewBucket
	viewDedup map[string]time.Time

	interestMu sync.Mutex
	interest   []interestSignal
}

type viewBucket struct {
	total  int
	unique map[string]struct{}
}

type interestSignal struct {
	accountID uuid.UUID
	postID    uuid.UUID
	delta     float64
	kind      string
	at        time.Time
}

func newHandler(host *module.Host[core.Services]) *handler {
	log := host.Log
	if log == nil {
		log = slog.Default()
	}
	return &handler{
		core:  host.Core,
		db:    host.Core.DB,
		st:    store.New(host.Core.DB),
		ports: host.Ports,
		log:   log,
		views:     map[uuid.UUID]*viewBucket{},
		viewDedup: map[string]time.Time{},
	}
}

func (h *handler) maxTake() int {
	if h.core != nil && h.core.Config != nil {
		return h.core.Config.Security.MaxTake
	}
	return 100
}

// registerRoutes wires every HTTP route owned by the posts module.
func (h *handler) registerRoutes(host *module.Host[core.Services]) {
	h.registerPostReadRoutes(host)
	h.registerPostWriteRoutes(host)
	h.registerTaxonomyRoutes(host)
	h.registerCollectionRoutes(host)
	h.registerWatchRoutes(host)
	h.registerAdminRoutes(host)
}

// ---------------------------------------------------------------------------
// Viewers and membership
// ---------------------------------------------------------------------------

type viewer struct {
	accountID uuid.UUID
	account   *gen.DyAccount
	superuser bool

	memberPubs map[uuid.UUID]struct{}
	friends    map[uuid.UUID]bool
	close      map[uuid.UUID]bool
}

func (v *viewer) authed() bool { return v != nil && v.accountID != uuid.Nil }

func (v *viewer) isMember(publisherID uuid.UUID) bool {
	if v == nil || publisherID == uuid.Nil {
		return false
	}
	_, ok := v.memberPubs[publisherID]
	return ok
}

func (h *handler) newViewer(c *gin.Context) *viewer {
	v := &viewer{
		memberPubs: map[uuid.UUID]struct{}{},
		friends:    map[uuid.UUID]bool{},
		close:      map[uuid.UUID]bool{},
	}
	account := authn.CurrentUser(c)
	if account == nil {
		return v
	}
	id, err := uuid.Parse(account.GetId())
	if err != nil {
		return v
	}
	v.accountID = id
	v.account = account
	v.superuser = account.GetIsSuperuser()
	v.memberPubs = h.memberPublisherSet(c.Request.Context(), id)
	return v
}

func (h *handler) memberPublisherSet(ctx context.Context, accountID uuid.UUID) map[uuid.UUID]struct{} {
	out := map[uuid.UUID]struct{}{}
	if accountID == uuid.Nil {
		return out
	}
	var memberIDs []uuid.UUID
	if err := h.db.WithContext(ctx).Model(&store.PublisherMemberEntity{}).
		Where("account_id = ?", accountID).Pluck("publisher_id", &memberIDs).Error; err != nil {
		h.log.Warn("posts: load publisher memberships failed", "error", err)
	}
	for _, id := range memberIDs {
		out[id] = struct{}{}
	}
	var ownedIDs []uuid.UUID
	if err := h.db.WithContext(ctx).Model(&store.PublisherEntity{}).
		Where("account_id = ?", accountID).Pluck("id", &ownedIDs).Error; err == nil {
		for _, id := range ownedIDs {
			out[id] = struct{}{}
		}
	}
	return out
}

// isMemberWithRole mirrors PublisherService.IsMemberWithRole, treating the
// owning account of an individual publisher as its owner.
func (h *handler) isMemberWithRole(ctx context.Context, publisherID, accountID uuid.UUID, minRole int) bool {
	if publisherID == uuid.Nil || accountID == uuid.Nil {
		return false
	}
	var publisher store.PublisherEntity
	if err := h.db.WithContext(ctx).Where("id = ?", publisherID).First(&publisher).Error; err == nil {
		if publisher.AccountID != nil && *publisher.AccountID == accountID {
			return true
		}
	}
	var member store.PublisherMemberEntity
	err := h.db.WithContext(ctx).
		Where("publisher_id = ? AND account_id = ?", publisherID, accountID).
		First(&member).Error
	if err != nil {
		return false
	}
	return member.Role >= minRole
}

func (h *handler) publisherByName(ctx context.Context, name string) (*store.PublisherEntity, error) {
	var publisher store.PublisherEntity
	err := h.db.WithContext(ctx).Where("lower(name) = ?", strings.ToLower(strings.TrimSpace(name))).
		First(&publisher).Error
	if err != nil {
		return nil, err
	}
	return &publisher, nil
}

func (h *handler) publisherByID(ctx context.Context, id uuid.UUID) (*store.PublisherEntity, error) {
	var publisher store.PublisherEntity
	if err := h.db.WithContext(ctx).Where("id = ?", id).First(&publisher).Error; err != nil {
		return nil, err
	}
	return &publisher, nil
}

func (h *handler) loadPublishers(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]*store.PublisherEntity, error) {
	out := map[uuid.UUID]*store.PublisherEntity{}
	if len(ids) == 0 {
		return out, nil
	}
	var rows []store.PublisherEntity
	if err := h.db.WithContext(ctx).Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	for i := range rows {
		out[rows[i].ID] = &rows[i]
	}
	return out, nil
}

// isFriend reports whether the viewer and the given account are friends,
// through the accounts port when available.
func (h *handler) isFriend(ctx context.Context, v *viewer, accountID uuid.UUID) bool {
	if v == nil || !v.authed() || accountID == uuid.Nil || accountID == v.accountID {
		return accountID != uuid.Nil && v != nil && accountID == v.accountID
	}
	if cached, ok := v.friends[accountID]; ok {
		return cached
	}
	result := false
	if h.ports != nil {
		if accounts, ok := module.Get[ports.Accounts](h.ports, ports.PortAccounts); ok {
			if value, err := accounts.HasRelationship(ctx, v.accountID.String(), accountID.String()); err == nil {
				result = value
			}
		}
	}
	v.friends[accountID] = result
	return result
}

func (h *handler) isSubscriber(ctx context.Context, accountID, publisherID uuid.UUID) bool {
	if accountID == uuid.Nil || publisherID == uuid.Nil {
		return false
	}
	var count int64
	err := h.db.WithContext(ctx).Model(&store.PublisherSubscriptionEntity{}).
		Where("publisher_id = ? AND account_id = ? AND ended_at IS NULL AND state = ? AND is_blocking = false",
			publisherID, accountID, int(model.SubscriptionAccepted)).
		Count(&count).Error
	return err == nil && count > 0
}

// ---------------------------------------------------------------------------
// Visibility
// ---------------------------------------------------------------------------

type gatekeep struct {
	gatekept   map[uuid.UUID]struct{}
	subscribed map[uuid.UUID]struct{}
}

func (h *handler) gatekeepInfo(ctx context.Context, v *viewer, publisherIDs []uuid.UUID) gatekeep {
	info := gatekeep{
		gatekept:   map[uuid.UUID]struct{}{},
		subscribed: map[uuid.UUID]struct{}{},
	}
	if len(publisherIDs) == 0 {
		return info
	}
	var gatekeptIDs []uuid.UUID
	if err := h.db.WithContext(ctx).Model(&store.PublisherEntity{}).
		Where("id IN ? AND gatekept_follows = true", publisherIDs).
		Pluck("id", &gatekeptIDs).Error; err != nil {
		return info
	}
	for _, id := range gatekeptIDs {
		info.gatekept[id] = struct{}{}
	}
	if len(gatekeptIDs) == 0 || v == nil || !v.authed() {
		return info
	}
	var subscribed []uuid.UUID
	if err := h.db.WithContext(ctx).Model(&store.PublisherSubscriptionEntity{}).
		Where("account_id = ? AND ended_at IS NULL AND state = ? AND is_blocking = false AND publisher_id IN ?",
			v.accountID, int(model.SubscriptionAccepted), gatekeptIDs).
		Pluck("publisher_id", &subscribed).Error; err == nil {
		for _, id := range subscribed {
			info.subscribed[id] = struct{}{}
		}
	}
	return info
}

// applyVisibility constrains a post query to what the viewer may see in a
// listing. Post-level friend checks that cannot be expressed in SQL are applied
// later by filterVisible.
func (h *handler) applyVisibility(q *gorm.DB, v *viewer, listing bool, _ gatekeep, showQuietPublic bool) *gorm.DB {
	now := time.Now().UTC()
	memberIDs := v.memberIDs()

	if listing {
		q = q.Where("drafted_at IS NULL")
		if v.authed() {
			q = q.Where("(visibility <> ? OR publisher_id IN ?)", int(model.VisibilityUnlisted), memberIDs)
		} else {
			q = q.Where("visibility <> ?", int(model.VisibilityUnlisted))
		}
	}

	const gatekeptPublishers = "publisher_id NOT IN (SELECT id FROM publishers WHERE gatekept_follows = true)"
	if !v.authed() {
		return q.Where("published_at IS NOT NULL AND published_at <= ?", now).
			Where("visibility = ?", int(model.VisibilityPublic)).
			Where(gatekeptPublishers)
	}

	const subscribedPublishers = `publisher_id IN (SELECT publisher_id FROM publisher_subscriptions
		WHERE account_id = ? AND ended_at IS NULL AND state = ? AND is_blocking = false)`

	q = q.Where("((drafted_at IS NULL AND published_at IS NOT NULL AND published_at <= ?) OR publisher_id IN ?)", now, memberIDs)
	q = q.Where("(visibility <> ? OR publisher_id IN ?)", int(model.VisibilityPrivate), memberIDs)
	q = q.Where("(visibility <> ? OR publisher_id IN ?)", int(model.VisibilityCloseFriendsOnly), memberIDs)
	if !showQuietPublic {
		q = q.Where(
			"(visibility <> ? OR publisher_id IN ? OR "+subscribedPublishers+")",
			int(model.VisibilityQuietPublic), memberIDs, v.accountID, int(model.SubscriptionAccepted),
		)
	}
	q = q.Where(
		"("+gatekeptPublishers+" OR publisher_id IN ? OR "+subscribedPublishers+")",
		memberIDs, v.accountID, int(model.SubscriptionAccepted),
	)
	return q
}

// filterVisible drops posts the viewer may not read because of a post-level
// relationship check (friends / close friends) or a gatekept publisher.
func (h *handler) filterVisible(ctx context.Context, posts []model.Post, v *viewer, info gatekeep, listing bool) []model.Post {
	out := make([]model.Post, 0, len(posts))
	publisherAccounts := map[uuid.UUID]uuid.UUID{}
	var publisherIDs []uuid.UUID
	for i := range posts {
		id, err := uuid.Parse(posts[i].PublisherID)
		if err != nil {
			continue
		}
		if _, ok := publisherAccounts[id]; !ok {
			publisherAccounts[id] = uuid.Nil
			publisherIDs = append(publisherIDs, id)
		}
	}
	if len(publisherIDs) > 0 {
		publishers, err := h.loadPublishers(ctx, publisherIDs)
		if err == nil {
			for id, publisher := range publishers {
				if publisher.AccountID != nil {
					publisherAccounts[id] = *publisher.AccountID
				}
			}
		}
	}
	info = h.gatekeepInfo(ctx, v, publisherIDs)
	for i := range posts {
		publisherID, _ := uuid.Parse(posts[i].PublisherID)
		accountID := publisherAccounts[publisherID]
		if _, gatekept := info.gatekept[publisherID]; gatekept {
			if !v.isMember(publisherID) {
				if _, subscribed := info.subscribed[publisherID]; !subscribed {
					continue
				}
			}
		}
		if !h.canViewPost(ctx, &posts[i], v, accountID) {
			continue
		}
		out = append(out, posts[i])
	}
	return out
}

func (h *handler) canViewPost(ctx context.Context, post *model.Post, v *viewer, publisherAccountID uuid.UUID) bool {
	now := time.Now().UTC()
	publisherID, _ := uuid.Parse(post.PublisherID)
	member := v.isMember(publisherID)

	if post.DraftedAt != nil && !member {
		return false
	}
	published := post.PublishedAt != nil && !post.PublishedAt.Time().After(now)
	if !published && !member {
		return false
	}
	if post.DeletedAt != nil {
		return false
	}
	switch post.Visibility {
	case int(model.VisibilityPrivate):
		return member
	case int(model.VisibilityFriends):
		return member || (v.authed() && h.isFriend(ctx, v, publisherAccountID))
	case int(model.VisibilityCloseFriendsOnly):
		return member || (v.authed() && h.isFriend(ctx, v, publisherAccountID))
	}
	if !v.authed() {
		return post.Visibility == int(model.VisibilityPublic) || post.Visibility == int(model.VisibilityUnlisted)
	}
	return true
}

// ensurePostVisible applies the detail-view rules: private/friends/close-friend
// gating and the gatekept-publisher subscription requirement.
func (h *handler) ensurePostVisible(ctx context.Context, post *model.Post, v *viewer, publisher *store.PublisherEntity) (int, string) {
	if post == nil {
		return 404, ""
	}
	publisherID, _ := uuid.Parse(post.PublisherID)
	member := v.isMember(publisherID)
	var accountID uuid.UUID
	if publisher != nil && publisher.AccountID != nil {
		accountID = *publisher.AccountID
	}
	if post.DraftedAt != nil && !member {
		return 404, ""
	}
	if post.PublishedAt == nil && !member {
		return 404, ""
	}
	switch post.Visibility {
	case int(model.VisibilityPrivate):
		if !member {
			return 404, ""
		}
	case int(model.VisibilityFriends):
		if !member && !(v.authed() && h.isFriend(ctx, v, accountID)) {
			return 404, ""
		}
	case int(model.VisibilityCloseFriendsOnly):
		if !member && !(v.authed() && h.isFriend(ctx, v, accountID)) {
			if !v.authed() {
				return 403, "Close friends access required"
			}
			return 403, "Close friends access required"
		}
	}
	if publisher != nil && publisher.GatekeptFollows != nil && *publisher.GatekeptFollows {
		if !member && !h.isSubscriber(ctx, v.accountID, publisherID) {
			if !v.authed() {
				return 403, "Subscriber access required"
			}
			return 403, "Subscriber access required"
		}
	}
	if post.Visibility == int(model.VisibilityQuietPublic) && !member {
		if !v.authed() || !h.isSubscriber(ctx, v.accountID, publisherID) {
			return 403, "Subscriber access required"
		}
	}
	return 0, ""
}

// ---------------------------------------------------------------------------
// Query helpers
// ---------------------------------------------------------------------------

func normalizeOrder(order string) string {
	switch strings.ToLower(strings.TrimSpace(order)) {
	case "createdat", "created_at", "created":
		return orderCreatedAt
	default:
		return strings.ToLower(strings.TrimSpace(order))
	}
}

func orderByDate(order string) bool {
	normalized := normalizeOrder(order)
	return normalized == orderDate || normalized == orderCreatedAt
}

// orderExpr builds an ORDER BY entry whose SQL carries bind parameters.
func orderExpr(sql string, args ...any) clause.OrderBy {
	return clause.OrderBy{Expression: gorm.Expr(sql, args...)}
}

func applyPostOrdering(q *gorm.DB, order string, orderDesc, shuffle bool, search string) *gorm.DB {
	if shuffle {
		return q.Order("random()")
	}
	normalized := normalizeOrder(order)
	if len(search) >= 3 && !orderByDate(normalized) {
		pattern := "%" + search + "%"
		q = q.Order(orderExpr(
			"(title ILIKE ? OR description ILIKE ? OR content ILIKE ?) DESC",
			pattern, pattern, pattern,
		))
		q = q.Order(orderExpr("similarity(COALESCE(title, ''), ?) DESC", search))
		q = q.Order(orderExpr("similarity(COALESCE(description, ''), ?) DESC", search))
		q = q.Order(orderExpr("similarity(COALESCE(content, ''), ?) DESC", search))
	}
	if normalized == orderPopularity {
		if orderDesc {
			return q.Order("(upvotes * 10 - downvotes * 10 + awarded_score) DESC").
				Order("COALESCE(published_at, created_at) DESC")
		}
		return q.Order("(upvotes * 10 - downvotes * 10 + awarded_score) ASC").
			Order("COALESCE(published_at, created_at) DESC")
	}
	if orderDesc {
		return q.Order("COALESCE(published_at, created_at) DESC")
	}
	return q.Order("COALESCE(published_at, created_at) ASC")
}

func applyTextSearch(q *gorm.DB, term string) *gorm.DB {
	term = strings.TrimSpace(term)
	if term == "" {
		return q
	}
	pattern := "%" + term + "%"
	if len([]rune(term)) < 3 {
		return q.Where("(title ILIKE ? OR description ILIKE ? OR content ILIKE ?)", pattern, pattern, pattern)
	}
	return q.Where(
		"(title ILIKE ? OR description ILIKE ? OR content ILIKE ? OR similarity(title, ?) > 0.3 OR similarity(description, ?) > 0.3 OR similarity(content, ?) > 0.3)",
		pattern, pattern, pattern, term, term, term,
	)
}

func parsePublisherNames(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		name := strings.ToLower(strings.TrimSpace(part))
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

func (h *handler) resolvePublisherNames(ctx context.Context, names []string) ([]store.PublisherEntity, []string) {
	if len(names) == 0 {
		return nil, nil
	}
	var rows []store.PublisherEntity
	if err := h.db.WithContext(ctx).Where("lower(name) IN ?", names).Find(&rows).Error; err != nil {
		return nil, names
	}
	resolved := map[string]struct{}{}
	for _, row := range rows {
		resolved[strings.ToLower(row.Name)] = struct{}{}
	}
	var missing []string
	for _, name := range names {
		if _, ok := resolved[name]; !ok {
			missing = append(missing, name)
		}
	}
	return rows, missing
}

func publisherNotFoundError(code string, missing []string) *errs.ApiError {
	message := "Publisher not found."
	detail := ""
	if len(missing) > 1 {
		message = "Publishers not found."
		detail = "Publishers not found: " + strings.Join(missing, ", ") + "."
	} else if len(missing) == 1 {
		detail = fmt.Sprintf("No publisher named %q exists.", missing[0])
	}
	apiErr := errs.New(code, message, 404)
	apiErr.Detail = &detail
	return apiErr
}

func parseUUID(value string) (uuid.UUID, bool) {
	id, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		return uuid.Nil, false
	}
	return id, true
}

func queryInt(c *gin.Context, name string, fallback int) int {
	raw := c.Query(name)
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return value
}

func queryBoolPtr(c *gin.Context, name string) *bool {
	raw, ok := c.GetQuery(name)
	if !ok || raw == "" {
		return nil
	}
	value := raw == "true" || raw == "1"
	return &value
}

func queryIntPtr(c *gin.Context, name string) *int {
	raw, ok := c.GetQuery(name)
	if !ok || raw == "" {
		return nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return nil
	}
	return &value
}

func queryInt64Ptr(c *gin.Context, name string) *int64 {
	raw, ok := c.GetQuery(name)
	if !ok || raw == "" {
		return nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return nil
	}
	return &value
}

func queryList(c *gin.Context, name string) []string {
	values, ok := c.GetQueryArray(name)
	if ok && len(values) > 0 {
		return values
	}
	raw := c.Query(name)
	if raw == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func normalizeOptionalText(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// sanitizeText mirrors TextSanitizer.Sanitize: control characters other than
// newline, carriage return, tab and space are dropped, as are format and
// non-spacing mark runes.
func sanitizeText(text *string) *string {
	if text == nil || *text == "" {
		return text
	}
	var b strings.Builder
	b.Grow(len(*text))
	for _, r := range *text {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' && r != ' ' {
			continue
		}
		if unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Mn, r) {
			continue
		}
		b.WriteRune(r)
	}
	out := b.String()
	return &out
}

func isNotFound(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound)
}

// pageFrom parses offset/take, writing the pagination error when invalid.
func (h *handler) pageFrom(c *gin.Context) (httpx.Page, bool) {
	return httpx.PageQuery(c, h.maxTake())
}

// paginate clamps offset/take the way the admin controllers do.
func paginate(c *gin.Context, defaultTake int, maxTake int) (int, int) {
	offset := queryInt(c, "offset", 0)
	if offset < 0 {
		offset = 0
	}
	take := queryInt(c, "take", defaultTake)
	if take < 1 {
		take = defaultTake
	}
	if take > maxTake {
		take = maxTake
	}
	return offset, take
}

// applyDetailScope constrains a single-post lookup by publication state only.
// Visibility (private/friends/close friends) and gatekeeping are enforced by
// ensurePostVisible, because those checks need the publisher account.
func (h *handler) applyDetailScope(q *gorm.DB, v *viewer) *gorm.DB {
	now := time.Now().UTC()
	if !v.authed() {
		return q.Where("published_at IS NOT NULL AND published_at <= ?", now).
			Where("visibility IN ?", []int{int(model.VisibilityPublic), int(model.VisibilityUnlisted)})
	}
	memberIDs := make([]uuid.UUID, 0, len(v.memberPubs))
	for id := range v.memberPubs {
		memberIDs = append(memberIDs, id)
	}
	return q.Where("((drafted_at IS NULL AND published_at IS NOT NULL AND published_at <= ?) OR publisher_id IN ?)", now, memberIDs)
}

// memberIDs returns the viewer's publisher ids as a slice.
func (v *viewer) memberIDs() []uuid.UUID {
	if v == nil {
		return nil
	}
	out := make([]uuid.UUID, 0, len(v.memberPubs))
	for id := range v.memberPubs {
		out = append(out, id)
	}
	return out
}

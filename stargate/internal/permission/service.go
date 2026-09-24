package permission

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"src.solsynth.dev/sosys/stargate/internal/store"
)

// Evaluation semantics (mirroring PermissionService.cs):
//
//   - An account's effective permissions are the union of the permission nodes
//     of every permission group it is a member of, plus its direct account
//     nodes. The `default` group applies to everyone because EnsureSeeded
//     enrolls every account in it.
//   - A node grants its key while expired_at is null/future and affected_at is
//     null/past; both are compared against a single "now" taken per call.
//   - Punishments of type PermissionModification (0) block specific keys for
//     the account: a blocked key denies even when a group grants it. Blocked
//     entries match case-insensitively and may contain '*' wildcards.
//   - When no exact node matches, wildcard nodes (key LIKE '%*%') are
//     considered and the most specific pattern wins
//     (score = max(1, 1000 - wildcards*100 - length); case-insensitive exact
//     match wins outright). Only 100 wildcard candidates are examined.
//
// The C# service caches results in Redis under
//
//	perm:{type}:{actor}:{key}   perm-cg:{actor}   perm-g:{actor}   perm-blocked:{actor}
//
// (1 minute expiration). This port is deliberately DB-backed with no Redis
// cache layer; the key shapes are noted here for parity.
//
// The gRPC surface this feeds (PermissionServiceGrpc.cs) exposes the methods
// HasPermission, GetPermission, AddPermissionNode, AddPermissionNodeToGroup,
// RemovePermissionNode, RemovePermissionNodeFromGroup.
type Service struct {
	DB *gorm.DB
}

func New(database any) *Service {
	if handle, ok := database.(*gorm.DB); ok {
		return &Service{DB: handle}
	}
	value := reflect.ValueOf(database)
	method := value.MethodByName("Config")
	if method.IsValid() {
		results := method.Call(nil)
		if len(results) == 1 {
			config := results[0]
			connConfig := config.Elem().FieldByName("ConnConfig")
			if connConfig.IsValid() {
				connString := connConfig.MethodByName("ConnString")
				if connString.IsValid() {
					dsn := connString.Call(nil)[0].String()
					handle, err := gorm.Open(postgres.Open(dsn), &gorm.Config{SkipDefaultTransaction: true})
					if err == nil {
						return &Service{DB: handle}
					}
				}
			}
		}
	}
	panic("permission.New requires *gorm.DB")
}

// PermissionNodeActorType mirrors the C# PermissionNodeActorType enum.
const (
	actorTypeAccount = 0 // Account
	actorTypeGroup   = 1 // Group
)

// PunishmentType mirrors the C# PunishmentType enum (Padlock Models/Punishment.cs).
const (
	punishmentTypePermissionModification = 0
	punishmentTypeBlockLogin             = 1
	punishmentTypeDisableAccount         = 2
	punishmentTypeStrike                 = 3
)

// HasPermission reports whether accountID holds key. It follows the C#
// PermissionService.HasPermissionAsync semantics described in the package
// comment: memberships (including the `default` group everyone belongs to)
// are unioned, expiry is respected, and keys blocked by a
// PermissionModification punishment deny even when granted.
func (s *Service) HasPermission(ctx context.Context, accountID uuid.UUID, key string) (bool, error) {
	if key == "" {
		return false, errors.New("permission: key cannot be empty")
	}
	actor := accountID.String()
	now := time.Now().UTC()

	blocked, err := s.blockedPermissions(ctx, actor, now)
	if err != nil {
		return false, err
	}
	if isPermissionBlocked(blocked, key) {
		return false, nil
	}

	value, found, err := s.findPermissionNode(ctx, actor, key, now)
	if err != nil {
		return false, err
	}
	return found && value, nil
}

// ListPermissionKeys returns the account's effective permission keys: the
// union of its group memberships' nodes and its direct account nodes, sorted
// by key and de-duplicated. Mirrors ListEffectivePermissionsAsync — note that
// the C# list does not apply the punishment-block filter (blocked keys are
// still listed; HasPermission is the authority for enforcement).
func (s *Service) ListPermissionKeys(ctx context.Context, accountID uuid.UUID) ([]string, error) {
	actor := accountID.String()
	now := time.Now().UTC()

	var nodes []store.PermissionNodeEntity
	err := s.DB.WithContext(ctx).Model(&store.PermissionNodeEntity{}).
		Select("key").
		Where(actorScopeWhere, actor, actorTypeAccount, actor, now, now).
		Where("(expired_at IS NULL OR expired_at > ?)", now).
		Where("(affected_at IS NULL OR affected_at <= ?)", now).
		Order("key").
		Find(&nodes).Error
	if err != nil {
		return nil, fmt.Errorf("permission: list keys: %w", err)
	}

	seen := make(map[string]struct{}, len(nodes))
	keys := make([]string, 0, len(nodes))
	for _, node := range nodes {
		if _, ok := seen[node.Key]; ok {
			continue
		}
		seen[node.Key] = struct{}{}
		keys = append(keys, node.Key)
	}
	return keys, nil
}

// actorScopeWhere matches the C# FindPermissionNodeAsync scope: direct account
// nodes (group_id IS NULL, actor, type=Account=0) plus nodes of any group the
// actor is currently a member of (membership expiry and affected_at checked
// here, exactly like the C# GetOrCacheUserGroupsAsync + Contains). The
// soft-delete filters of the outer node query are added by GORM; the
// membership subquery spells its own out because it is hand-written SQL.
// Placeholders: actor, type, actor, now, now.
const actorScopeWhere = `(
	(group_id IS NULL AND actor = ? AND type = ?)
	OR group_id IN (
		SELECT gm.group_id
		FROM permission_group_members gm
		WHERE gm.actor = ?
		  AND gm.deleted_at IS NULL
		  AND (gm.expired_at IS NULL OR gm.expired_at > ?)
		  AND (gm.affected_at IS NULL OR gm.affected_at <= ?)
	)
)`

// blockedPermissions returns the case-folded set of permission keys blocked
// for the actor by active PermissionModification punishments. The C# query
// filters type, expiry and the global soft-delete filter (the latter is added
// by GORM for the soft-deletable entity), and flattens the blocked_permissions
// jsonb arrays with an OrdinalIgnoreCase comparer.
func (s *Service) blockedPermissions(ctx context.Context, actor string, now time.Time) (map[string]struct{}, error) {
	var punishments []store.PunishmentEntity
	err := s.DB.WithContext(ctx).Model(&store.PunishmentEntity{}).
		Select("blocked_permissions").
		Where("account_id = ?::uuid", actor).
		Where("type = ?", punishmentTypePermissionModification).
		Where("(expired_at IS NULL OR expired_at > ?)", now).
		Find(&punishments).Error
	if err != nil {
		return nil, fmt.Errorf("permission: blocked: %w", err)
	}

	blocked := make(map[string]struct{})
	for _, punishment := range punishments {
		if punishment.BlockedPermissions == nil {
			continue
		}
		var perms []string
		if err := json.Unmarshal(*punishment.BlockedPermissions, &perms); err != nil {
			return nil, fmt.Errorf("permission: scan blocked: %w", err)
		}
		for _, p := range perms {
			blocked[strings.ToLower(p)] = struct{}{}
		}
	}
	return blocked, nil
}

// isPermissionBlocked mirrors IsPermissionBlocked: exact case-insensitive
// match or a '*' wildcard match (the C# MatchesWildcard is itself
// case-insensitive; patterns are pre-folded here).
func isPermissionBlocked(blocked map[string]struct{}, key string) bool {
	lower := strings.ToLower(key)
	if _, ok := blocked[lower]; ok {
		return true
	}
	for pattern := range blocked {
		if strings.Contains(pattern, "*") && matchesWildcard(pattern, lower) {
			return true
		}
	}
	return false
}

// matchesWildcard is a direct port of PermissionService.MatchesWildcard
// ('*' matches any run; input is pre-folded so the C# char.ToUpperInvariant
// comparisons reduce to plain equality).
func matchesWildcard(pattern, target string) bool {
	patternIndex, targetIndex := 0, 0
	wildcardIndex, wildcardTargetIndex := -1, -1

	for targetIndex < len(target) {
		if patternIndex < len(pattern) && pattern[patternIndex] != '*' && pattern[patternIndex] == target[targetIndex] {
			patternIndex++
			targetIndex++
			continue
		}
		if patternIndex < len(pattern) && pattern[patternIndex] == '*' {
			wildcardIndex = patternIndex
			wildcardTargetIndex = targetIndex
			patternIndex++
			continue
		}
		if wildcardIndex < 0 {
			return false
		}
		patternIndex = wildcardIndex + 1
		wildcardTargetIndex++
		targetIndex = wildcardTargetIndex
	}

	for patternIndex < len(pattern) && pattern[patternIndex] == '*' {
		patternIndex++
	}
	return patternIndex == len(pattern)
}

// findPermissionNode resolves key for the actor following the C#
// FindPermissionNodeAsync: exact node match first (case-sensitive, matching
// the EF `=` translation), then the best wildcard match among up to 100
// candidates (EnableWildcardMatching=true, MaxWildcardMatches=100).
func (s *Service) findPermissionNode(ctx context.Context, actor, key string, now time.Time) (bool, bool, error) {
	// Exact match — highest priority.
	value, found, err := queryPermissionValue(s.DB.WithContext(ctx).Model(&store.PermissionNodeEntity{}).
		Select("value").
		Where(`"key" = ?`, key).
		Where(actorScopeWhere, actor, actorTypeAccount, actor, now, now).
		Where("(expired_at IS NULL OR expired_at > ?)", now).
		Where("(affected_at IS NULL OR affected_at <= ?)", now).
		Limit(1))
	if err != nil || found {
		return value, found, err
	}

	// Wildcard candidates.
	var candidates []store.PermissionNodeEntity
	err = s.DB.WithContext(ctx).Model(&store.PermissionNodeEntity{}).
		Select("key", "value").
		Where(`"key" LIKE ?`, "%*%").
		Where(actorScopeWhere, actor, actorTypeAccount, actor, now, now).
		Where("(expired_at IS NULL OR expired_at > ?)", now).
		Where("(affected_at IS NULL OR affected_at <= ?)", now).
		Order("key").
		Limit(100).
		Find(&candidates).Error
	if err != nil {
		return false, false, fmt.Errorf("permission: wildcard query: %w", err)
	}

	bestKey := ""
	bestValue := false
	bestScore := -1
	for _, candidate := range candidates {
		score := calculatePatternMatchScore(candidate.Key, key)
		if score <= bestScore {
			continue
		}
		value, err := decodePermissionValue(candidate.Value)
		if err != nil {
			return false, false, fmt.Errorf("permission: scan wildcard: %w", err)
		}
		bestKey, bestValue, bestScore = candidate.Key, value, score
	}
	return bestValue, bestKey != "", nil
}

// queryPermissionValue runs the prepared node lookup (already scoped, ordered
// and limited by the caller, and already carrying the request context) and
// decodes the single jsonb value column into a bool (seeded nodes store
// `true`; other JSON values are decoded like the C#
// DeserializePermissionValue<bool>).
func queryPermissionValue(query *gorm.DB) (bool, bool, error) {
	var node store.PermissionNodeEntity
	err := query.Take(&node).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("permission: node query: %w", err)
	}
	value, err := decodePermissionValue(node.Value)
	if err != nil {
		return false, false, fmt.Errorf("permission: node query: %w", err)
	}
	return value, true, nil
}

// decodePermissionValue decodes a permission_nodes.value jsonb literal.
func decodePermissionValue(raw datatypes.JSON) (bool, error) {
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, err
	}
	return value, nil
}

// calculatePatternMatchScore mirrors PermissionService.CalculatePatternMatchScore:
// case-insensitive exact match wins; otherwise only patterns containing '*'
// that actually match score, more specific (fewer wildcards, longer) wins.
func calculatePatternMatchScore(pattern, target string) int {
	if strings.EqualFold(pattern, target) {
		return math.MaxInt
	}
	if !strings.Contains(pattern, "*") {
		return -1
	}
	// Both sides are folded to lowercase before matching, so the C# per-char
	// case-insensitive comparison reduces to a plain match.
	if !matchesWildcard(strings.ToLower(pattern), strings.ToLower(target)) {
		return -1
	}
	wildcardCount := strings.Count(pattern, "*")
	length := len(pattern)
	score := 1000 - wildcardCount*100 - length
	if score < 1 {
		return 1
	}
	return score
}

// ─────────────────────────────── Seeding ───────────────────────────────

// Group keys, matching PermissionSeedService constants.
const (
	DefaultGroupKey   = "default"
	VerifiedGroupKey  = "verified"
	ModeratorGroupKey = "moderator"
	DeveloperGroupKey = "developer"
)

// legacyAllUsersGroupKey is removed by the C# seed on every run.
const legacyAllUsersGroupKey = "all-users"

// defaultPermissionKeys mirrors PermissionSeedService.DefaultPermissionKeys.
var defaultPermissionKeys = []string{
	TestsTake,
	AccountsConnectionsView,
	ChatCreate,
	ChatUpdate,
	ChatDelete,
	ChatMessagesCreate,
	ChatMessagesUpdate,
	ChatMessagesDelete,
	ChatMessagesReact,
	ChatMembersManage,
	ChatMembersTimeout,
	ChatMembersKick,
	ChatInvitesManage,
	ChatE2eeManage,
	ChatSync,
	ChatCallStart,
	ChatCallEnd,
	ChatCallInvite,
	ChatCallKick,
	ChatCallMute,
	ChatGroupsManage,
	ChatPinsManage,
	NotificationsPut,
	NotificationsReadAll,
	NotificationsPreferencesManage,
	NotificationsSubscriptionsManage,
	WalletsCreate,
	OrdersCreate,
	OrdersUpdate,
	OrdersPay,
	OrdersView,
	SubscriptionsCreate,
	SubscriptionsCancel,
	SubscriptionsCheckout,
	SubscriptionGiftsPurchase,
	SubscriptionGiftsRedeem,
	SubscriptionGiftsSend,
	SubscriptionGiftsCancel,
	AuthSessionsManage,
	AuthFactorsManage,
	AuthApiKeysManage,
	AuthAppsAuthorize,
	AuthRecover,
	AccountContactsManage,
	AccountDevicesManage,
	AccountAuthorizedAppsManage,
	E2eeKeysManage,
	E2eeMlsManage,
	E2eeDevicesManage,
	ChatReadAll,
	AccountsStatusesCreate,
	AccountsStatusesUpdate,
	NfcTagsCreate,
	NfcTagsUpdate,
	NfcTagsDelete,
	NfcTagsClaim,
	NfcTagsLock,
	CalendarEventsCreate,
	CalendarEventsUpdate,
	CalendarEventsDelete,
	CalendarSubscriptionsManage,
	CalendarCheckinManage,
	StickersPacksCreate,
	StickersPacksUpdate,
	StickersPacksDelete,
	StickersPacksOwn,
	StickersPacksOrder,
	StickersCreate,
	StickersUpdate,
	StickersDelete,
	StickersContentUpdate,
	SurveysCreate,
	SurveysUpdate,
	SurveysDelete,
	SurveysPublish,
	SurveysArchive,
	SurveysClone,
	NotableDaysCreate,
	NotableDaysUpdate,
	NotableDaysDelete,
	TicketsCreate,
	ProgressionBadgesManage,
	FilesUpload,
}

// verifiedPermissionKeys mirrors PermissionSeedService.VerifiedPermissionKeys.
var verifiedPermissionKeys = []string{
	PostsView,
	PostsCreateBlog,
	PostsCreate,
	PostsUpdate,
	PostsDelete,
	PostsPublish,
	PostsReact,
	PostsBoost,
	PostsBookmark,
	PostsAward,
	PostsSponsor,
	PostsPin,
	PostsBatchDelete,
	PostsBatchVisibility,
	PostCollectionsCreate,
	PostCollectionsUpdate,
	PostCollectionsDelete,
	PostCollectionsPostsManage,
	PostCategoriesSubscribe,
	PostsTagsCreate,
	PostsTagsUpdate,
	PostsTagsDelete,
	PostsTagsAssign,
	PostsTagsClaim,
	PostsTagsEvent,
	PostSubscriptionsManage,
	PublishersCreate,
	PublishersUpdate,
	PublishersDelete,
	PublishersMembersManage,
	PublishersInvitesManage,
	PublishersFeaturesManage,
	PublishersFediverseManage,
	PublishersDomainsManage,
	PublishersSubscriptionsManage,
	TimelinesFeedback,
	SurveysAnswer,
	SurveysSubscribe,
	LiveStreamsCreate,
	LiveStreamsUpdate,
	LiveStreamsDelete,
	LiveStreamsStart,
	LiveStreamsEnd,
	LiveStreamsHls,
	LiveStreamsPin,
	LiveStreamsAwards,
	LiveStreamsThumbnail,
	AccountsProfileBoard,
	AccountsProfileBoardManage,
	AccountsBoardManage,
	PresencesScan,
	PresencesActivityManage,
	PresencesArtworkManage,
	RelationshipsCreate,
	RelationshipsUpdate,
	RelationshipsDelete,
	RelationshipsFriendsManage,
	RelationshipsBlockManage,
	RelationshipsMuteManage,
	RelationshipsCloseFriendsManage,
	RelationshipsAliasManage,
	RelationshipsSync,
	RealmsCreate,
	RealmsUpdate,
	RealmsDelete,
	RealmsInvitesManage,
	RealmsMembersManage,
	RealmsLabelsManage,
	RealmsBoostsManage,
	MeetCreate,
	MeetUpdate,
	MeetDelete,
	MeetComplete,
	MeetJoin,
	MeetPinManage,
	MeetVisibilityUpdate,
	LocationPinsCreate,
	LocationPinsUpdate,
	LocationPinsDelete,
	NearbyPresenceManage,
	NearbyResolve,
	RewindCreate,
	// ── WattEngine: user self-service nodes ──
	WorkspacesCreate,
	WorkspacesView,
	WorkspacesUpdate,
	WorkspacesDelete,
	WorkspacesMembersManage,
	WorkspacesPlansManage,
	BoardsView,
	BoardsCreate,
	BoardsUpdate,
	BoardsDelete,
	TasksView,
	TasksCreate,
	TasksUpdate,
	TasksDelete,
	TasksAssignmentsManage,
	TasksCommentsManage,
	TasksIntegrationsManage,
	FlywheelView,
	FlywheelAppsManage,
	FlywheelBlobsManage,
	FlywheelBlobsDelete,
}

// moderatorPermissionKeys mirrors PermissionSeedService.ModeratorPermissionKeys.
var moderatorPermissionKeys = []string{
	PostsModerate,
	PostsLock,
	RealmsModerate,
	TicketsCreate,
	TicketsUpdate,
	TicketsDelete,
	TicketsMessagesCreate,
	TicketsStatusUpdate,
	TicketsAssign,
	AccountsView,
	AccountsManage,
	AccountsDeletion,
	AccountsActionLogsView,
	AccountsProfileManage,
	AccountsConnectionsManage,
	AccountsPasskeysView,
	AccountsPasskeysManage,
	AccountsRelationshipsView,
	PunishmentsView,
	PunishmentsCreate,
	PunishmentsUpdate,
	PunishmentsDelete,
	NotificationsSend,
	EmailsSend,
}

// developerPermissionKeys mirrors PermissionSeedService.DeveloperPermissionKeys.
var developerPermissionKeys = []string{
	DevelopersCreate,
	DevelopersManage,
	CustomAppsCreate,
	CustomAppsUpdate,
	CustomAppsDelete,
	CustomAppsSecretsManage,
	BotAccountsCreate,
	BotAccountsUpdate,
	BotAccountsDelete,
	BotAccountsKeysManage,
	BotAccountsChatManage,
	AppProductsCreate,
	AppProductsUpdate,
	AppProductsDelete,
	DevProjectsCreate,
	DevProjectsUpdate,
	DevProjectsDelete,
	MiniAppsView,
	MiniAppsCreate,
	MiniAppsUpdate,
	MiniAppsDelete,
	MiniAppsPackageUpload,
}

// seedGroup describes one group's node set for EnsureSeeded.
type seedGroup struct {
	key  string
	keys []string
}

// GrantPermissionGroup mirrors AccountService.GrantPermissionGroup (the
// Padlock consumer of Passport's accounts.tests.permission-group-granted
// event): it adds the account to the group by key, or re-activates an
// existing membership (clears affected_at/expired_at), and reports whether
// the group exists. The actor permission-cache clear is the caller's job.
func (s *Service) GrantPermissionGroup(ctx context.Context, accountID uuid.UUID, groupKey string) (bool, error) {
	if groupKey == "" {
		return false, nil
	}
	var group store.PermissionGroupEntity
	err := s.DB.WithContext(ctx).Model(&store.PermissionGroupEntity{}).
		Where(`"key" = ?`, groupKey).
		Take(&group).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("permission: lookup group %q: %w", groupKey, err)
	}
	actor := accountID.String()
	now := time.Now().UTC()
	member := store.PermissionGroupMemberEntity{
		GroupID: group.ID,
		Actor:   actor,
		EntityBase: store.EntityBase{
			CreatedAt: now,
			UpdatedAt: now,
		},
	}
	if err := s.DB.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "group_id"}, {Name: "actor"}},
		DoUpdates: clause.Assignments(map[string]any{
			"affected_at": nil,
			"expired_at":  nil,
			"updated_at":  now,
		}),
	}).Create(&member).Error; err != nil {
		return false, fmt.Errorf("permission: grant group %q to %s: %w", groupKey, actor, err)
	}
	return true, nil
}

// EnsureSeeded synchronizes the permission registry exactly like the C#
// PermissionSeedService.EnsureSeededAsync: the legacy `all-users` group is
// removed, then the default/verified/moderator/developer groups are ensured
// with their node sets (missing keys inserted, existing keys preserved), and
// every account is enrolled in `default` while activated accounts are
// enrolled in `verified`. Idempotent and safe to run on every boot.
func (s *Service) EnsureSeeded(ctx context.Context) error {
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()

		// Legacy "all-users" group is removed. The C# Remove() goes through the
		// EF soft-delete interceptor (deleted_at = now), so this is an UPDATE,
		// not a DELETE — members/nodes pointing at it stay untouched, matching
		// the C# behavior. GORM adds the `deleted_at IS NULL` filter for the
		// soft-deletable model on its own.
		if err := tx.WithContext(ctx).Model(&store.PermissionGroupEntity{}).
			Where(`"key" = ?`, legacyAllUsersGroupKey).
			Updates(map[string]any{"deleted_at": now, "updated_at": now}).Error; err != nil {
			return fmt.Errorf("permission: seed remove legacy group: %w", err)
		}

		groups := []seedGroup{
			{key: DefaultGroupKey, keys: defaultPermissionKeys},
			{key: VerifiedGroupKey, keys: verifiedPermissionKeys},
			{key: ModeratorGroupKey, keys: moderatorPermissionKeys},
			{key: DeveloperGroupKey, keys: developerPermissionKeys},
		}
		for _, g := range groups {
			if err := ensureGroup(ctx, tx, g.key, g.keys); err != nil {
				return err
			}
		}

		// `default` group: every account (soft-delete filtered), matching
		// db.Accounts.Select(x => x.Id.ToString()).
		// `verified` group: only activated accounts.
		memberSets := []struct {
			groupKey      string
			activatedOnly bool
		}{
			{groupKey: DefaultGroupKey},
			{groupKey: VerifiedGroupKey, activatedOnly: true},
		}
		for _, ms := range memberSets {
			if err := ensureMembers(ctx, tx, ms.groupKey, ms.activatedOnly, now); err != nil {
				return err
			}
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelSerializable})
	return err
}

// ensureMembers enrolls every account (optionally only activated ones) in the
// group named by groupKey. Missing or soft-deleted groups enroll nobody, which
// is what the original INSERT ... SELECT ... JOIN permission_groups produced.
func ensureMembers(ctx context.Context, tx *gorm.DB, groupKey string, activatedOnly bool, now time.Time) error {
	var group store.PermissionGroupEntity
	err := tx.WithContext(ctx).Model(&store.PermissionGroupEntity{}).
		Where(`"key" = ?`, groupKey).
		Take(&group).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("permission: seed find group %q for members: %w", groupKey, err)
	}

	accountsQuery := tx.WithContext(ctx).Model(&store.AccountEntity{}).Select("id")
	if activatedOnly {
		accountsQuery = accountsQuery.Where("activated_at IS NOT NULL")
	}
	var accounts []store.AccountEntity
	if err := accountsQuery.Find(&accounts).Error; err != nil {
		return fmt.Errorf("permission: seed list accounts for %q: %w", groupKey, err)
	}
	if len(accounts) == 0 {
		return nil
	}

	members := make([]store.PermissionGroupMemberEntity, 0, len(accounts))
	for _, account := range accounts {
		members = append(members, store.PermissionGroupMemberEntity{
			GroupID: group.ID,
			Actor:   account.ID.String(),
			EntityBase: store.EntityBase{
				CreatedAt: now,
				UpdatedAt: now,
			},
		})
	}
	if err := tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).
		CreateInBatches(members, 500).Error; err != nil {
		return fmt.Errorf("permission: seed members for %q: %w", groupKey, err)
	}
	return nil
}

// ensureGroup mirrors PermissionSeedService.EnsureGroupAsync: find or create
// the group, then insert nodes for the keys it does not already have
// (actor "group:{key}", type Group, value true). Nodes are soft-delete
// filtered, matching the EF global query filter.
func ensureGroup(ctx context.Context, tx *gorm.DB, key string, keys []string) error {
	now := time.Now().UTC()

	var group store.PermissionGroupEntity
	err := tx.WithContext(ctx).Model(&store.PermissionGroupEntity{}).
		Where(`"key" = ?`, key).
		Take(&group).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		group = store.PermissionGroupEntity{
			ID:  uuid.New(),
			Key: key,
			EntityBase: store.EntityBase{
				CreatedAt: now,
				UpdatedAt: now,
			},
		}
		if err := tx.WithContext(ctx).Create(&group).Error; err != nil {
			return fmt.Errorf("permission: seed create group %q: %w", key, err)
		}
	} else if err != nil {
		return fmt.Errorf("permission: seed find group %q: %w", key, err)
	}

	var nodes []store.PermissionNodeEntity
	if err := tx.WithContext(ctx).Model(&store.PermissionNodeEntity{}).
		Select("key").
		Where("group_id = ?", group.ID).
		Find(&nodes).Error; err != nil {
		return fmt.Errorf("permission: seed list nodes %q: %w", key, err)
	}
	existing := make(map[string]struct{}, len(nodes))
	for _, node := range nodes {
		existing[node.Key] = struct{}{}
	}

	for _, permissionKey := range keys {
		if _, ok := existing[permissionKey]; ok {
			continue
		}
		node := store.PermissionNodeEntity{
			ID: uuid.New(),
			EntityBase: store.EntityBase{
				CreatedAt: now,
				UpdatedAt: now,
			},
			Actor:   "group:" + key,
			GroupID: &group.ID,
			Key:     permissionKey,
			Type:    actorTypeGroup,
			Value:   datatypes.JSON("true"),
		}
		if err := tx.WithContext(ctx).Create(&node).Error; err != nil {
			return fmt.Errorf("permission: seed insert node %q in %q: %w", permissionKey, key, err)
		}
	}
	return nil
}

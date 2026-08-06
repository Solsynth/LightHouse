package permission

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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
	DB *pgxpool.Pool
}

// New returns a permission Service backed by the given pool.
func New(db *pgxpool.Pool) *Service {
	return &Service{DB: db}
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

	rows, err := s.DB.Query(ctx, `
		SELECT n.key
		FROM permission_nodes n
		WHERE n.deleted_at IS NULL
		  AND (n.expired_at IS NULL OR n.expired_at > $2)
		  AND (n.affected_at IS NULL OR n.affected_at <= $2)
		  AND `+actorScopeSQL+`
		ORDER BY n.key
	`, actor, now)
	if err != nil {
		return nil, fmt.Errorf("permission: list keys: %w", err)
	}
	defer rows.Close()

	seen := make(map[string]struct{}, 64)
	keys := make([]string, 0, 64)
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, fmt.Errorf("permission: scan key: %w", err)
		}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("permission: list keys: %w", err)
	}
	return keys, nil
}

// actorScopeSQL matches the C# FindPermissionNodeAsync scope: direct account
// nodes (group_id IS NULL, actor, type=Account=0) plus nodes of any group the
// actor is currently a member of (membership expiry and affected_at checked
// here, exactly like the C# GetOrCacheUserGroupsAsync + Contains).
// Parameters: $1 actor, $2 now.
const actorScopeSQL = `(
	(n.group_id IS NULL AND n.actor = $1 AND n.type = 0)
	OR n.group_id IN (
		SELECT gm.group_id
		FROM permission_group_members gm
		WHERE gm.actor = $1
		  AND gm.deleted_at IS NULL
		  AND (gm.expired_at IS NULL OR gm.expired_at > $2)
		  AND (gm.affected_at IS NULL OR gm.affected_at <= $2)
	)
)`

// blockedPermissions returns the case-folded set of permission keys blocked
// for the actor by active PermissionModification punishments. The C# query
// filters type, expiry and the global soft-delete filter, and flattens the
// blocked_permissions jsonb arrays with an OrdinalIgnoreCase comparer.
func (s *Service) blockedPermissions(ctx context.Context, actor string, now time.Time) (map[string]struct{}, error) {
	rows, err := s.DB.Query(ctx, `
		SELECT blocked_permissions
		FROM punishments
		WHERE account_id = $1
		  AND type = $2
		  AND deleted_at IS NULL
		  AND (expired_at IS NULL OR expired_at > $3)
	`, actor, punishmentTypePermissionModification, now)
	if err != nil {
		return nil, fmt.Errorf("permission: blocked: %w", err)
	}
	defer rows.Close()

	blocked := make(map[string]struct{})
	for rows.Next() {
		var perms []string
		if err := rows.Scan(&perms); err != nil {
			return nil, fmt.Errorf("permission: scan blocked: %w", err)
		}
		for _, p := range perms {
			blocked[strings.ToLower(p)] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("permission: blocked: %w", err)
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
	value, found, err := s.queryPermissionValue(ctx, `
		SELECT n.value
		FROM permission_nodes n
		WHERE n.deleted_at IS NULL
		  AND n.key = $3
		  AND (n.expired_at IS NULL OR n.expired_at > $2)
		  AND (n.affected_at IS NULL OR n.affected_at <= $2)
		  AND `+actorScopeSQL+`
		LIMIT 1
	`, actor, now, key)
	if err != nil || found {
		return value, found, err
	}

	// Wildcard candidates.
	rows, err := s.DB.Query(ctx, `
		SELECT n.key, n.value
		FROM permission_nodes n
		WHERE n.deleted_at IS NULL
		  AND n.key LIKE '%*%'
		  AND (n.expired_at IS NULL OR n.expired_at > $2)
		  AND (n.affected_at IS NULL OR n.affected_at <= $2)
		  AND `+actorScopeSQL+`
		ORDER BY n.key
		LIMIT 100
	`, actor, now)
	if err != nil {
		return false, false, fmt.Errorf("permission: wildcard query: %w", err)
	}
	defer rows.Close()

	bestKey := ""
	bestValue := false
	bestScore := -1
	for rows.Next() {
		var nodeKey string
		var nodeValue bool
		if err := rows.Scan(&nodeKey, &nodeValue); err != nil {
			return false, false, fmt.Errorf("permission: scan wildcard: %w", err)
		}
		score := calculatePatternMatchScore(nodeKey, key)
		if score <= bestScore {
			continue
		}
		bestKey, bestValue, bestScore = nodeKey, nodeValue, score
	}
	if err := rows.Err(); err != nil {
		return false, false, fmt.Errorf("permission: wildcard query: %w", err)
	}
	return bestValue, bestKey != "", nil
}

// queryPermissionValue runs a SELECT returning a single jsonb value column
// and scans it into a bool (seeded nodes store `true`; other JSON values are
// decoded like the C# DeserializePermissionValue<bool>).
func (s *Service) queryPermissionValue(ctx context.Context, sql string, args ...any) (bool, bool, error) {
	row := s.DB.QueryRow(ctx, sql, args...)
	var value bool
	err := row.Scan(&value)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("permission: node query: %w", err)
	}
	return value, true, nil
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
	var groupID uuid.UUID
	if err := s.DB.QueryRow(ctx, `SELECT id FROM permission_groups
		WHERE "key" = $1 AND deleted_at IS NULL`, groupKey).Scan(&groupID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("permission: lookup group %q: %w", groupKey, err)
	}
	actor := accountID.String()
	if _, err := s.DB.Exec(ctx, `INSERT INTO permission_group_members
		(group_id, actor, affected_at, expired_at, created_at, updated_at)
		VALUES ($1, $2, NULL, NULL, now(), now())
		ON CONFLICT (group_id, actor) DO UPDATE SET affected_at = NULL, expired_at = NULL, updated_at = now()`,
		groupID, actor); err != nil {
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
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return fmt.Errorf("permission: seed begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Legacy "all-users" group is removed. The C# Remove() goes through the
	// EF soft-delete interceptor (deleted_at = now), so this is an UPDATE,
	// not a DELETE — members/nodes pointing at it stay untouched, matching
	// the C# behavior.
	if _, err := tx.Exec(ctx, `
		UPDATE permission_groups
		SET deleted_at = now(), updated_at = now()
		WHERE key = $1 AND deleted_at IS NULL
	`, legacyAllUsersGroupKey); err != nil {
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
		groupKey    string
		whereClause string
	}{
		{groupKey: DefaultGroupKey, whereClause: ""},
		{groupKey: VerifiedGroupKey, whereClause: "AND a.activated_at IS NOT NULL"},
	}
	for _, ms := range memberSets {
		// ON CONFLICT makes the insert idempotent; the C# computes the
		// missing set then inserts, which is equivalent for the target set.
		if _, err := tx.Exec(ctx, `
			INSERT INTO permission_group_members (group_id, actor, created_at, updated_at)
			SELECT g.id, a.id::text, now(), now()
			FROM accounts a
			JOIN permission_groups g ON g.key = $1 AND g.deleted_at IS NULL
			WHERE a.deleted_at IS NULL `+ms.whereClause+`
			ON CONFLICT (group_id, actor) DO NOTHING
		`, ms.groupKey); err != nil {
			return fmt.Errorf("permission: seed members for %q: %w", ms.groupKey, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("permission: seed commit: %w", err)
	}
	return nil
}

// ensureGroup mirrors PermissionSeedService.EnsureGroupAsync: find or create
// the group, then insert nodes for the keys it does not already have
// (actor "group:{key}", type Group, value true). Nodes are soft-delete
// filtered, matching the EF global query filter.
func ensureGroup(ctx context.Context, tx pgx.Tx, key string, keys []string) error {
	var groupID uuid.UUID
	err := tx.QueryRow(ctx, `
		SELECT id FROM permission_groups WHERE key = $1 AND deleted_at IS NULL
	`, key).Scan(&groupID)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := tx.QueryRow(ctx, `
			INSERT INTO permission_groups (id, key, created_at, updated_at)
			VALUES (gen_random_uuid(), $1, now(), now())
			RETURNING id
		`, key).Scan(&groupID); err != nil {
			return fmt.Errorf("permission: seed create group %q: %w", key, err)
		}
	} else if err != nil {
		return fmt.Errorf("permission: seed find group %q: %w", key, err)
	}

	existing := make(map[string]struct{}, len(keys))
	rows, err := tx.Query(ctx, `
		SELECT key FROM permission_nodes WHERE group_id = $1 AND deleted_at IS NULL
	`, groupID)
	if err != nil {
		return fmt.Errorf("permission: seed list nodes %q: %w", key, err)
	}
	for rows.Next() {
		var nodeKey string
		if err := rows.Scan(&nodeKey); err != nil {
			rows.Close()
			return fmt.Errorf("permission: seed scan node %q: %w", key, err)
		}
		existing[nodeKey] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("permission: seed list nodes %q: %w", key, err)
	}
	rows.Close()

	for _, permissionKey := range keys {
		if _, ok := existing[permissionKey]; ok {
			continue
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO permission_nodes (id, actor, type, key, value, group_id, created_at, updated_at)
			VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, now(), now())
		`, "group:"+key, actorTypeGroup, permissionKey, "true", groupID); err != nil {
			return fmt.Errorf("permission: seed insert node %q in %q: %w", permissionKey, key, err)
		}
	}
	return nil
}

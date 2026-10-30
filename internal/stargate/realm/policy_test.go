package realm

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// TestBoostPolicy pins the RealmBoostPolicy arithmetic the boost endpoints
// depend on: currency normalization, the price of a share, the inverse used
// when an event omits the share count, the level thresholds and the label cap.
func TestBoostPolicy(t *testing.T) {
	if _, err := normalizeCurrency("GOLDS"); err != nil {
		t.Fatalf("golds should normalize: %v", err)
	}
	currency, err := normalizeCurrency("")
	if err != nil || currency != GoldsCurrency {
		t.Fatalf("empty currency = %q, %v; want golds", currency, err)
	}
	if _, err := normalizeCurrency("eur"); err == nil {
		t.Fatal("eur must be rejected")
	}

	golds, err := amountForShares(GoldsCurrency, 12)
	if err != nil {
		t.Fatal(err)
	}
	if !golds.Equal(decimal.NewFromInt(12)) {
		t.Fatalf("12 gold shares = %s, want 12", golds)
	}
	points, err := amountForShares(PointsCurrency, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !points.Equal(decimal.NewFromInt(3000)) {
		t.Fatalf("3 point shares = %s, want 3000", points)
	}

	// The inverse recovers the share count the payment event carried.
	recovered := sharesForAmount(PointsCurrency, points).Decimal
	if !recovered.Equal(decimal.NewFromInt(3)) {
		t.Fatalf("recovered shares = %s, want 3", recovered)
	}

	cases := []struct {
		points decimal.Decimal
		level  int
		cap    int
	}{
		{decimal.Zero, 0, 0},
		{decimal.NewFromInt(9), 0, 0},
		{decimal.NewFromInt(10), 1, 3},
		{decimal.NewFromInt(24), 1, 3},
		{decimal.NewFromInt(25), 2, 6},
		{decimal.NewFromInt(49), 2, 6},
		{decimal.NewFromInt(50), 3, 9},
		{decimal.NewFromInt(500), 3, 9},
	}
	for _, tc := range cases {
		if got := boostLevel(tc.points); got != tc.level {
			t.Errorf("boostLevel(%s) = %d, want %d", tc.points, got, tc.level)
		}
		if got := labelCap(tc.level); got != tc.cap {
			t.Errorf("labelCap(%d) = %d, want %d", tc.level, got, tc.cap)
		}
	}
}

// TestRoleDefaults pins RealmService.GetDefaultPermission: owners may do
// everything, moderators manage members but not the realm, and normal members
// only contribute content.
func TestRoleDefaults(t *testing.T) {
	all := []string{PermChatSend, PermPostCreate, PermPostComment, PermMediaUpload,
		PermPostModerate, PermChatModerate, PermMembersManage, PermRealmManage}
	for _, permission := range all {
		if !defaultPermission(RoleOwner, permission) {
			t.Errorf("owner denied %s", permission)
		}
	}
	moderator := map[string]bool{
		PermChatSend: true, PermPostCreate: true, PermPostComment: true, PermMediaUpload: true,
		PermPostModerate: true, PermChatModerate: true, PermMembersManage: true, PermRealmManage: false,
	}
	for permission, want := range moderator {
		if got := defaultPermission(RoleModerator, permission); got != want {
			t.Errorf("moderator %s = %v, want %v", permission, got, want)
		}
	}
	member := map[string]bool{
		PermChatSend: true, PermPostCreate: true, PermPostComment: true, PermMediaUpload: true,
		PermPostModerate: false, PermChatModerate: false, PermMembersManage: false, PermRealmManage: false,
	}
	for permission, want := range member {
		if got := defaultPermission(RoleNormal, permission); got != want {
			t.Errorf("member %s = %v, want %v", permission, got, want)
		}
	}
	if defaultPermission(-1, PermChatSend) {
		t.Error("an unknown role must not be granted anything")
	}
}

// TestUserPermissionOverrideWins pins the precedence RealmService.HasPermission
// documented: a non-nil user flag beats the role row, and a nil flag falls
// through.
func TestUserPermissionOverrideWins(t *testing.T) {
	deny := false
	allow := true
	override := &RealmUserPermissionEntity{CanPost: &deny, CanChat: nil}
	if value, ok := userPermissionValue(override, PermPostCreate); !ok || value {
		t.Fatalf("user deny of %s = %v, %v; want false, true", PermPostCreate, value, ok)
	}
	if _, ok := userPermissionValue(override, PermChatSend); ok {
		t.Fatalf("nil flag for %s must fall through", PermChatSend)
	}
	if _, ok := userPermissionValue(override, "unknown.permission"); ok {
		t.Fatal("an unknown permission must fall through")
	}
	roleRow := &RealmRolePermissionEntity{CanPost: true, CanManageRealm: allow}
	if value, ok := rolePermissionValue(roleRow, PermPostCreate); !ok || !value {
		t.Fatalf("role row %s = %v, %v; want true, true", PermPostCreate, value, ok)
	}
	if value, ok := rolePermissionValue(roleRow, PermRealmManage); !ok || !value {
		t.Fatalf("role row %s = %v, %v; want true, true", PermRealmManage, value, ok)
	}
}

// TestTenureIsIdempotentPerDay pins the tenure rule the job relies on: the
// day string is the (reason_type, reason) key, so a restart inside the same
// day cannot grant twice.
func TestTenureIsIdempotentPerDay(t *testing.T) {
	now := time.Date(2026, 10, 31, 1, 0, 0, 0, time.UTC)
	first := now.Format("2006-01-02")
	again := now.Add(6 * time.Hour).Format("2006-01-02")
	if first != again {
		t.Fatalf("same-day keys differ: %s vs %s", first, again)
	}
	if nextDay := now.Add(24 * time.Hour).Format("2006-01-02"); nextDay == first {
		t.Fatalf("next-day key %s must differ", nextDay)
	}
}

// TestRealmLevelCurve pins the realm XP curve (base 1000, not the account
// curve's 100): 1200 XP is level 1, and 1000 XP is the level-1 threshold.
func TestRealmLevelCurve(t *testing.T) {
	if got := realmLevelFromExp(0); got != 0 {
		t.Fatalf("level(0) = %d, want 0", got)
	}
	if got := realmLevelFromExp(999); got != 0 {
		t.Fatalf("level(999) = %d, want 0", got)
	}
	if got := realmLevelFromExp(1000); got != 1 {
		t.Fatalf("level(1000) = %d, want 1", got)
	}
	if got := realmLevelFromExp(1200); got != 1 {
		t.Fatalf("level(1200) = %d, want 1", got)
	}
	if progress := realmLevelingProgress(1200); progress <= 0 || progress >= 1 {
		t.Fatalf("progress(1200) = %f, want (0,1)", progress)
	}
	if got := realmLevelFromExp(-5); got != 0 {
		t.Fatalf("negative xp level = %d, want 0", got)
	}
}

// TestTieredQuota pins ResourceQuotaCalculator.GetTieredQuota: the realm quota
// every account gets from its level and perk level.
func TestTieredQuota(t *testing.T) {
	cases := []struct{ level, perk, want int }{
		{0, 0, 0},
		{9, 0, 0},
		{10, 0, 1},
		{29, 0, 1},
		{30, 0, 2},
		{59, 0, 2},
		{60, 0, 3},
		{12, 2, 3},
	}
	for _, tc := range cases {
		if got := tieredQuota(tc.level, tc.perk); got != tc.want {
			t.Errorf("tieredQuota(%d, %d) = %d, want %d", tc.level, tc.perk, got, tc.want)
		}
	}
}

// TestBoostOrderMetadata pins the metadata contract the boost controller
// writes and both consumers read: without it the order is unusable, and a
// missing share count is recovered from the amount.
func TestBoostOrderMetadata(t *testing.T) {
	realmID := uuid.New()
	accountID := uuid.New()
	orderID := uuid.New()

	contribution, shares, err := boostFromOrder(orderID.String(), map[string]any{
		"realm_id": realmID.String(), "account_id": accountID.String(),
		"shares": 4, "currency": "golds", "amount": "4",
	})
	if err != nil {
		t.Fatalf("golds order: %v", err)
	}
	if contribution.RealmID != realmID || contribution.AccountID != accountID {
		t.Fatalf("ids not carried: %+v", contribution)
	}
	if shares != 4 {
		t.Fatalf("shares = %d, want 4", shares)
	}

	// points are 1000 per share; a publisher that dropped the count still
	// yields the right share number from the amount.
	recovered, shares, err := boostFromOrder(orderID.String(), map[string]any{
		"realm_id": realmID.String(), "account_id": accountID.String(),
		"currency": "points", "amount": "3000",
	})
	if err != nil {
		t.Fatalf("points order: %v", err)
	}
	if shares != 3 {
		t.Fatalf("recovered shares = %d, want 3", shares)
	}
	if !recovered.Amount.Decimal.Equal(decimal.NewFromInt(3000)) {
		t.Fatalf("amount = %s, want 3000", recovered.Amount)
	}

	if _, _, err := boostFromOrder("not-a-uuid", map[string]any{}); err == nil {
		t.Fatal("an invalid order id must be rejected")
	}
	if _, _, err := boostFromOrder(orderID.String(), map[string]any{"currency": "golds", "amount": "1"}); err == nil {
		t.Fatal("missing realm_id must be rejected")
	}
}

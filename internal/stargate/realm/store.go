package realm

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/store"
)

// ErrNotFound reports a missing realm, membership, label or permission row.
// The HTTP layer maps it to the PASSPORT_REALM_* 404 codes.
var ErrNotFound = errors.New("realm: not found")

// Store is the realm persistence layer. It owns the eight module tables and
// never touches a core table except the account/profile reads the member
// payload hydrates from.
type Store struct {
	DB *gorm.DB
}

// NewStore builds the persistence layer over the module's database handle.
func NewStore(db *gorm.DB) *Store { return &Store{DB: db} }

func mapNotFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	return err
}

// ─────────────────────────── realms ───────────────────────────

// GetRealmBySlug resolves a realm case-insensitively, mirroring
// RealmService.GetBySlug.
func (s *Store) GetRealmBySlug(ctx context.Context, slug string) (*RealmEntity, error) {
	var entity RealmEntity
	if err := s.DB.WithContext(ctx).Where("LOWER(slug) = LOWER(?)", slug).First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return &entity, nil
}

// GetRealm resolves a realm by id.
func (s *Store) GetRealm(ctx context.Context, id uuid.UUID) (*RealmEntity, error) {
	var entity RealmEntity
	if err := s.DB.WithContext(ctx).First(&entity, "id = ?", id).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return &entity, nil
}

// GetRealmsByIDs loads a batch of realms, preserving nothing about order.
func (s *Store) GetRealmsByIDs(ctx context.Context, ids []uuid.UUID) ([]RealmEntity, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var entities []RealmEntity
	if err := s.DB.WithContext(ctx).Where("id IN ?", ids).Find(&entities).Error; err != nil {
		return nil, err
	}
	return entities, nil
}

// RealmSlugTaken reports whether another realm owns the slug.
func (s *Store) RealmSlugTaken(ctx context.Context, slug string, exceptID *uuid.UUID) (bool, error) {
	query := s.DB.WithContext(ctx).Model(&RealmEntity{}).Where("LOWER(slug) = LOWER(?)", slug)
	if exceptID != nil {
		query = query.Where("id <> ?", *exceptID)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// CountOwnedRealms counts the realms an account owns (RealmQuotaService).
func (s *Store) CountOwnedRealms(ctx context.Context, accountID uuid.UUID) (int64, error) {
	var count int64
	if err := s.DB.WithContext(ctx).Model(&RealmEntity{}).
		Where("account_id = ?", accountID).Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// OwnedRealms lists the realms an account owns, oldest first.
func (s *Store) OwnedRealms(ctx context.Context, accountID uuid.UUID) ([]RealmEntity, error) {
	var entities []RealmEntity
	if err := s.DB.WithContext(ctx).Where("account_id = ?", accountID).
		Order("created_at").Find(&entities).Error; err != nil {
		return nil, err
	}
	return entities, nil
}

// RealmFilter is the admin realm listing filter.
type RealmFilter struct {
	Query       string
	IsPublic    *bool
	IsCommunity *bool
	Verified    *bool
	AccountID   *uuid.UUID
	Offset      int
	Take        int
}

// ListRealms pages the admin realm listing plus the total count.
func (s *Store) ListRealms(ctx context.Context, filter RealmFilter) ([]RealmEntity, int64, error) {
	query := s.DB.WithContext(ctx).Model(&RealmEntity{})
	if filter.Query != "" {
		pattern := "%" + filter.Query + "%"
		query = query.Where("slug ILIKE ? OR name ILIKE ? OR description ILIKE ?", pattern, pattern, pattern)
	}
	if filter.IsPublic != nil {
		query = query.Where("is_public = ?", *filter.IsPublic)
	}
	if filter.IsCommunity != nil {
		query = query.Where("is_community = ?", *filter.IsCommunity)
	}
	if filter.Verified != nil {
		if *filter.Verified {
			query = query.Where("verification IS NOT NULL")
		} else {
			query = query.Where("verification IS NULL")
		}
	}
	if filter.AccountID != nil {
		query = query.Where("account_id = ?", *filter.AccountID)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var entities []RealmEntity
	if err := query.Order("updated_at DESC").
		Offset(maxInt(filter.Offset, 0)).Limit(clampTake(filter.Take, 50)).
		Find(&entities).Error; err != nil {
		return nil, 0, err
	}
	return entities, total, nil
}

// ListCommunityRealms pages the public community listing (RealmPublicController)
// ordered by member count.
func (s *Store) ListCommunityRealms(ctx context.Context, query string, offset, take int) ([]RealmEntity, int64, error) {
	statement := s.DB.WithContext(ctx).Model(&RealmEntity{}).Where("is_community = ?", true)
	if query != "" {
		pattern := "%" + query + "%"
		statement = statement.Where("name ILIKE ? OR description ILIKE ?", pattern, pattern)
	}
	var total int64
	if err := statement.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var entities []RealmEntity
	if err := statement.
		Order("(SELECT COUNT(*) FROM realm_members m WHERE m.realm_id = realms.id AND m.deleted_at IS NULL) DESC").
		Offset(maxInt(offset, 0)).Limit(clampTake(take, 10)).
		Find(&entities).Error; err != nil {
		return nil, 0, err
	}
	return entities, total, nil
}

// ListPublicRealms is the gRPC public listing: community realms when
// isCommunity, otherwise public realms, ordered by the requested key.
func (s *Store) ListPublicRealms(ctx context.Context, isCommunity bool, orderBy string, take int) ([]RealmEntity, error) {
	query := s.DB.WithContext(ctx).Model(&RealmEntity{})
	if isCommunity {
		query = query.Where("is_community = ?", true)
	} else {
		query = query.Where("is_public = ?", true)
	}
	switch orderBy {
	case "random":
		query = query.Order("RANDOM()")
	case "name":
		query = query.Order("name")
	case "popularity":
		query = query.Order("(SELECT COUNT(*) FROM realm_members m WHERE m.realm_id = realms.id AND m.deleted_at IS NULL) DESC")
	default:
		query = query.Order("created_at DESC")
	}
	var entities []RealmEntity
	if err := query.Limit(clampTake(take, 0)).Find(&entities).Error; err != nil {
		return nil, err
	}
	return entities, nil
}

// SearchPublicRealms mirrors RealmServiceGrpc.SearchRealms: prefix match on
// slug or name over public realms.
func (s *Store) SearchPublicRealms(ctx context.Context, query string, limit int) ([]RealmEntity, error) {
	pattern := query + "%"
	var entities []RealmEntity
	if err := s.DB.WithContext(ctx).Model(&RealmEntity{}).
		Where("is_public = ?", true).
		Where("slug LIKE ? OR name LIKE ?", pattern, pattern).
		Limit(clampTake(limit, 0)).Find(&entities).Error; err != nil {
		return nil, err
	}
	return entities, nil
}

// SaveRealm stores the realm row (insert or update).
func (s *Store) SaveRealm(ctx context.Context, entity *RealmEntity) error {
	return s.DB.WithContext(ctx).Save(entity).Error
}

// CreateRealmWithOwner inserts the realm and its owner membership in one
// transaction, so a failed membership insert never leaves an ownerless realm.
func (s *Store) CreateRealmWithOwner(ctx context.Context, entity *RealmEntity, now time.Time) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(entity).Error; err != nil {
			return err
		}
		member := &RealmMemberEntity{
			RealmID: entity.ID, AccountID: entity.AccountID, Role: RoleOwner, JoinedAt: &now,
		}
		return tx.Create(member).Error
	})
}

// DeleteRealm soft-deletes a realm and every membership in one transaction,
// mirroring the C# delete flow (RealmController.Delete / RealmAdminController).
func (s *Store) DeleteRealm(ctx context.Context, id uuid.UUID, now time.Time) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Delete(&RealmEntity{}, "id = ?", id).Error; err != nil {
			return err
		}
		return tx.Model(&RealmMemberEntity{}).Where("realm_id = ?", id).
			Updates(map[string]any{"deleted_at": now, "leave_at": now}).Error
	})
}

// ─────────────────────────── members ───────────────────────────

// GetMember resolves a membership row, joined or not.
func (s *Store) GetMember(ctx context.Context, realmID, accountID uuid.UUID) (*RealmMemberEntity, error) {
	var entity RealmMemberEntity
	if err := s.DB.WithContext(ctx).
		Where("realm_id = ? AND account_id = ?", realmID, accountID).
		First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return &entity, nil
}

// GetActiveMember resolves a joined, not-left membership row.
func (s *Store) GetActiveMember(ctx context.Context, realmID, accountID uuid.UUID) (*RealmMemberEntity, error) {
	var entity RealmMemberEntity
	if err := s.DB.WithContext(ctx).
		Where("realm_id = ? AND account_id = ?", realmID, accountID).
		Where("joined_at IS NOT NULL AND leave_at IS NULL").
		First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return &entity, nil
}

// GetActiveMembersByAccounts loads the active rows for a realm scoped to the
// given accounts, keyed by account id.
func (s *Store) GetActiveMembersByAccounts(ctx context.Context, realmID uuid.UUID, accountIDs []uuid.UUID) (map[uuid.UUID]*RealmMemberEntity, error) {
	out := map[uuid.UUID]*RealmMemberEntity{}
	if len(accountIDs) == 0 {
		return out, nil
	}
	var entities []RealmMemberEntity
	if err := s.DB.WithContext(ctx).
		Where("realm_id = ? AND account_id IN ?", realmID, accountIDs).
		Where("joined_at IS NOT NULL AND leave_at IS NULL").
		Find(&entities).Error; err != nil {
		return nil, err
	}
	for i := range entities {
		out[entities[i].AccountID] = &entities[i]
	}
	return out, nil
}

// MemberRole reports the active membership role; the bool is false for a
// non-member, which callers treat as "no role".
func (s *Store) MemberRole(ctx context.Context, realmID, accountID uuid.UUID) (int, bool, error) {
	member, err := s.GetActiveMember(ctx, realmID, accountID)
	if errors.Is(err, ErrNotFound) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return member.Role, true, nil
}

// SaveMember stores a membership row.
func (s *Store) SaveMember(ctx context.Context, entity *RealmMemberEntity) error {
	return s.DB.WithContext(ctx).Save(entity).Error
}

// CreateMember inserts a membership row.
func (s *Store) CreateMember(ctx context.Context, entity *RealmMemberEntity) error {
	return s.DB.WithContext(ctx).Create(entity).Error
}

// JoinedRealmIDs lists the ids of the realms an account belongs to, newest
// membership last (the gRPC GetUserRealms cache payload).
func (s *Store) JoinedRealmIDs(ctx context.Context, accountID uuid.UUID) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	if err := s.DB.WithContext(ctx).Model(&RealmMemberEntity{}).
		Where("account_id = ?", accountID).
		Where("joined_at IS NOT NULL AND leave_at IS NULL").
		Pluck("realm_id", &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

// JoinedRealms lists the realms an account belongs to (GET /api/realms).
func (s *Store) JoinedRealms(ctx context.Context, accountID uuid.UUID) ([]RealmEntity, error) {
	var entities []RealmEntity
	if err := s.DB.WithContext(ctx).Model(&RealmEntity{}).
		Where("id IN (?)", s.DB.WithContext(ctx).Model(&RealmMemberEntity{}).
			Select("realm_id").
			Where("account_id = ?", accountID).
			Where("joined_at IS NOT NULL AND leave_at IS NULL")).
		Find(&entities).Error; err != nil {
		return nil, err
	}
	return entities, nil
}

// PendingInvites lists the unrealised invitations for an account
// (GET /api/realms/invites).
func (s *Store) PendingInvites(ctx context.Context, accountID uuid.UUID) ([]RealmMemberEntity, error) {
	var entities []RealmMemberEntity
	if err := s.DB.WithContext(ctx).
		Where("account_id = ?", accountID).
		Where("joined_at IS NULL AND leave_at IS NULL").
		Order("created_at").Find(&entities).Error; err != nil {
		return nil, err
	}
	return entities, nil
}

// MemberFilter is the member listing filter shared by the public and admin
// controllers.
type MemberFilter struct {
	RealmID     uuid.UUID
	PendingOnly bool
	JoinedOnly  bool
	Role        *int
	LabelID     *uuid.UUID
	AccountIDs  []uuid.UUID
	Offset      int
	Take        int
}

// ListMembers pages the realm membership rows plus the total count.
func (s *Store) ListMembers(ctx context.Context, filter MemberFilter) ([]RealmMemberEntity, int64, error) {
	query := s.DB.WithContext(ctx).Model(&RealmMemberEntity{}).Where("realm_id = ?", filter.RealmID)
	switch {
	case filter.PendingOnly:
		query = query.Where("joined_at IS NULL AND leave_at IS NULL")
	case filter.JoinedOnly:
		query = query.Where("joined_at IS NOT NULL AND leave_at IS NULL")
	default:
		query = query.Where("leave_at IS NULL")
	}
	if filter.Role != nil {
		query = query.Where("role = ?", *filter.Role)
	}
	if filter.LabelID != nil {
		query = query.Where("label_id = ?", *filter.LabelID)
	}
	if filter.AccountIDs != nil {
		query = query.Where("account_id IN ?", filter.AccountIDs)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var entities []RealmMemberEntity
	if err := query.Order("experience DESC").Order("created_at").
		Offset(maxInt(filter.Offset, 0)).Limit(clampTake(filter.Take, 20)).
		Find(&entities).Error; err != nil {
		return nil, 0, err
	}
	return entities, total, nil
}

// MemberCounts is the admin realm detail tally.
type MemberCounts struct {
	Members       int64
	PendingInvite int64
	Labels        int64
	ActiveBoosts  int64
}

// CountRealmDetail gathers the four counters RealmAdminController.GetRealm
// reports, in four queries instead of one per counter.
func (s *Store) CountRealmDetail(ctx context.Context, realmID uuid.UUID, boostCutoff time.Time) (MemberCounts, error) {
	var counts MemberCounts
	if err := s.DB.WithContext(ctx).Model(&RealmMemberEntity{}).
		Where("realm_id = ? AND joined_at IS NOT NULL AND leave_at IS NULL", realmID).
		Count(&counts.Members).Error; err != nil {
		return counts, err
	}
	if err := s.DB.WithContext(ctx).Model(&RealmMemberEntity{}).
		Where("realm_id = ? AND joined_at IS NULL AND leave_at IS NULL", realmID).
		Count(&counts.PendingInvite).Error; err != nil {
		return counts, err
	}
	if err := s.DB.WithContext(ctx).Model(&RealmLabelEntity{}).
		Where("realm_id = ?", realmID).Count(&counts.Labels).Error; err != nil {
		return counts, err
	}
	if err := s.DB.WithContext(ctx).Model(&RealmBoostContributionEntity{}).
		Where("realm_id = ? AND created_at >= ?", realmID, boostCutoff).
		Count(&counts.ActiveBoosts).Error; err != nil {
		return counts, err
	}
	return counts, nil
}

// ─────────────────────────── boosts ───────────────────────────

// ActiveBoostPoints sums the shares of the contributions inside the window.
// Shares are currency-dependent, so the aggregation happens in Go after a
// single windowed read, exactly like RealmService.RefreshBoostStates.
func (s *Store) ActiveBoostPoints(ctx context.Context, realmIDs []uuid.UUID, cutoff time.Time) (map[uuid.UUID]Decimal, error) {
	out := map[uuid.UUID]Decimal{}
	if len(realmIDs) == 0 {
		return out, nil
	}
	var rows []RealmBoostContributionEntity
	if err := s.DB.WithContext(ctx).
		Where("realm_id IN ? AND created_at >= ?", realmIDs, cutoff).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		shares := sharesForAmount(row.Currency, row.Amount.Decimal)
		current, ok := out[row.RealmID]
		if !ok {
			current = Decimal{}
		}
		out[row.RealmID] = NewDecimal(current.Add(shares.Decimal))
	}
	return out, nil
}

// BoostExists reports whether an order already produced a contribution,
// which makes a replayed payment event a no-op.
func (s *Store) BoostExists(ctx context.Context, orderID uuid.UUID) (bool, error) {
	var count int64
	if err := s.DB.WithContext(ctx).Model(&RealmBoostContributionEntity{}).
		Where("order_id = ?", orderID).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// BoostLeaderboardEntry is one row of GET /{slug}/boosts/leaderboard.
type BoostLeaderboardEntry struct {
	AccountID     uuid.UUID
	AmountGolds   Decimal
	AmountPoints  Decimal
	Shares        Decimal
	Boosts        int64
	LastBoostedAt time.Time
}

// BoostLeaderboard aggregates the active contributions per account. The C#
// controller pulled every row and grouped in memory; the aggregation is done
// in SQL here because the leaderboard is a public read.
func (s *Store) BoostLeaderboard(ctx context.Context, realmID uuid.UUID, cutoff time.Time, take int) ([]BoostLeaderboardEntry, error) {
	var rows []struct {
		AccountID uuid.UUID
		Currency  string
		Amount    Decimal
		CreatedAt time.Time
	}
	if err := s.DB.WithContext(ctx).Model(&RealmBoostContributionEntity{}).
		Select("account_id, currency, amount, created_at").
		Where("realm_id = ? AND created_at >= ?", realmID, cutoff).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	type aggregate struct {
		golds, points, shares Decimal
		boosts                int64
		last                  time.Time
	}
	byAccount := map[uuid.UUID]*aggregate{}
	for _, row := range rows {
		agg, ok := byAccount[row.AccountID]
		if !ok {
			agg = &aggregate{}
			byAccount[row.AccountID] = agg
		}
		agg.boosts++
		if row.CreatedAt.After(agg.last) {
			agg.last = row.CreatedAt
		}
		switch currency, err := normalizeCurrency(row.Currency); {
		case err == nil && currency == PointsCurrency:
			agg.points = NewDecimal(agg.points.Add(row.Amount.Decimal))
		case err == nil:
			agg.golds = NewDecimal(agg.golds.Add(row.Amount.Decimal))
		}
		shares := sharesForAmount(row.Currency, row.Amount.Decimal)
		agg.shares = NewDecimal(agg.shares.Add(shares.Decimal))
	}
	out := make([]BoostLeaderboardEntry, 0, len(byAccount))
	for accountID, agg := range byAccount {
		out = append(out, BoostLeaderboardEntry{
			AccountID: accountID, AmountGolds: agg.golds, AmountPoints: agg.points,
			Shares: agg.shares, Boosts: agg.boosts, LastBoostedAt: agg.last,
		})
	}
	sortLeaderboard(out)
	if take > 0 && len(out) > take {
		out = out[:take]
	}
	return out, nil
}

// sortLeaderboard orders by shares, then most recent boost, mirroring the C#
// OrderByDescending(shares).ThenByDescending(last_boosted_at).
func sortLeaderboard(rows []BoostLeaderboardEntry) {
	sort.SliceStable(rows, func(i, j int) bool {
		left, right := rows[i].Shares.Decimal, rows[j].Shares.Decimal
		if !left.Equal(right) {
			return left.GreaterThan(right)
		}
		return rows[i].LastBoostedAt.After(rows[j].LastBoostedAt)
	})
}

// AddBoostContribution records one paid boost.
func (s *Store) AddBoostContribution(ctx context.Context, entity *RealmBoostContributionEntity) error {
	return s.DB.WithContext(ctx).Create(entity).Error
}

// ─────────────────────────── labels ───────────────────────────

// ListLabels returns a realm's labels ordered by name.
func (s *Store) ListLabels(ctx context.Context, realmID uuid.UUID) ([]RealmLabelEntity, error) {
	var entities []RealmLabelEntity
	if err := s.DB.WithContext(ctx).Where("realm_id = ?", realmID).
		Order("name").Find(&entities).Error; err != nil {
		return nil, err
	}
	return entities, nil
}

// CountLabels counts a realm's labels (the boost-level cap check).
func (s *Store) CountLabels(ctx context.Context, realmID uuid.UUID) (int64, error) {
	var count int64
	if err := s.DB.WithContext(ctx).Model(&RealmLabelEntity{}).
		Where("realm_id = ?", realmID).Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// GetLabel resolves a label scoped to its realm.
func (s *Store) GetLabel(ctx context.Context, realmID, labelID uuid.UUID) (*RealmLabelEntity, error) {
	var entity RealmLabelEntity
	if err := s.DB.WithContext(ctx).
		Where("id = ? AND realm_id = ?", labelID, realmID).First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return &entity, nil
}

// CreateLabel inserts a label.
func (s *Store) CreateLabel(ctx context.Context, entity *RealmLabelEntity) error {
	return s.DB.WithContext(ctx).Create(entity).Error
}

// SaveLabel updates a label.
func (s *Store) SaveLabel(ctx context.Context, entity *RealmLabelEntity) error {
	return s.DB.WithContext(ctx).Save(entity).Error
}

// DeleteLabel clears the label from its members and removes it, in one
// transaction (RealmController.DeleteLabel).
func (s *Store) DeleteLabel(ctx context.Context, realmID, labelID uuid.UUID) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&RealmMemberEntity{}).
			Where("realm_id = ? AND label_id = ?", realmID, labelID).
			Update("label_id", nil).Error; err != nil {
			return err
		}
		return tx.Delete(&RealmLabelEntity{}, "id = ? AND realm_id = ?", labelID, realmID).Error
	})
}

// ─────────────────────────── experience ───────────────────────────

// RecentExperience returns a member's newest XP records.
func (s *Store) RecentExperience(ctx context.Context, realmID, accountID uuid.UUID, take int) ([]RealmExperienceRecordEntity, error) {
	var entities []RealmExperienceRecordEntity
	if err := s.DB.WithContext(ctx).
		Where("realm_id = ? AND account_id = ?", realmID, accountID).
		Order("created_at DESC").Limit(clampTake(take, 50)).
		Find(&entities).Error; err != nil {
		return nil, err
	}
	return entities, nil
}

// ExperienceExists reports whether the idempotency key was already granted.
func (s *Store) ExperienceExists(ctx context.Context, realmID, accountID uuid.UUID, reasonType, reason string) (bool, error) {
	var count int64
	if err := s.DB.WithContext(ctx).Model(&RealmExperienceRecordEntity{}).
		Where("realm_id = ? AND account_id = ? AND reason_type = ? AND reason = ?",
			realmID, accountID, reasonType, reason).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// ExperienceSince reports whether the account already earned this reason type
// inside the cooldown window.
func (s *Store) ExperienceSince(ctx context.Context, realmID, accountID uuid.UUID, reasonType string, since time.Time) (bool, error) {
	var count int64
	if err := s.DB.WithContext(ctx).Model(&RealmExperienceRecordEntity{}).
		Where("realm_id = ? AND account_id = ? AND reason_type = ? AND created_at >= ?",
			realmID, accountID, reasonType, since).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// AddExperience records the XP grant and bumps the member's counter in one
// transaction, so the ledger and the running total never diverge.
func (s *Store) AddExperience(ctx context.Context, entity *RealmExperienceRecordEntity) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(entity).Error; err != nil {
			return err
		}
		return tx.Model(&RealmMemberEntity{}).
			Where("realm_id = ? AND account_id = ?", entity.RealmID, entity.AccountID).
			UpdateColumn("experience", gorm.Expr("experience + ?", entity.Delta)).Error
	})
}

// TenureMembers lists every active membership for the daily tenure grant.
func (s *Store) TenureMembers(ctx context.Context) ([]RealmMemberEntity, error) {
	var entities []RealmMemberEntity
	if err := s.DB.WithContext(ctx).
		Where("joined_at IS NOT NULL AND leave_at IS NULL").
		Find(&entities).Error; err != nil {
		return nil, err
	}
	return entities, nil
}

// ─────────────────────────── permissions ───────────────────────────

// GetUserPermission returns the account's override row, if any.
func (s *Store) GetUserPermission(ctx context.Context, realmID, accountID uuid.UUID) (*RealmUserPermissionEntity, error) {
	var entity RealmUserPermissionEntity
	if err := s.DB.WithContext(ctx).
		Where("realm_id = ? AND account_id = ?", realmID, accountID).
		First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return &entity, nil
}

// SaveUserPermission upserts the override row for (realm, account).
func (s *Store) SaveUserPermission(ctx context.Context, entity *RealmUserPermissionEntity) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing RealmUserPermissionEntity
		err := tx.Where("realm_id = ? AND account_id = ?", entity.RealmID, entity.AccountID).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return tx.Create(entity).Error
		}
		if err != nil {
			return err
		}
		entity.ID = existing.ID
		entity.CreatedAt = existing.CreatedAt
		entity.DeletedAt = existing.DeletedAt
		return tx.Model(&existing).Select(
			"can_chat", "can_post", "can_comment", "can_upload_media",
			"can_moderate_posts", "can_moderate_chat", "can_manage_members", "can_manage_realm",
		).Updates(entity).Error
	})
}

// ListRolePermissions lists a realm's role permission rows ordered by level.
func (s *Store) ListRolePermissions(ctx context.Context, realmID uuid.UUID) ([]RealmRolePermissionEntity, error) {
	var entities []RealmRolePermissionEntity
	if err := s.DB.WithContext(ctx).Where("realm_id = ?", realmID).
		Order("role_level").Find(&entities).Error; err != nil {
		return nil, err
	}
	return entities, nil
}

// GetRolePermission resolves one role level's permission row.
func (s *Store) GetRolePermission(ctx context.Context, realmID uuid.UUID, roleLevel int) (*RealmRolePermissionEntity, error) {
	var entity RealmRolePermissionEntity
	if err := s.DB.WithContext(ctx).
		Where("realm_id = ? AND role_level = ?", realmID, roleLevel).
		First(&entity).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return &entity, nil
}

// SaveRolePermission upserts one role level's permission row.
func (s *Store) SaveRolePermission(ctx context.Context, entity *RealmRolePermissionEntity) (*RealmRolePermissionEntity, error) {
	var saved RealmRolePermissionEntity
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing RealmRolePermissionEntity
		err := tx.Where("realm_id = ? AND role_level = ?", entity.RealmID, entity.RoleLevel).
			First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err := tx.Create(entity).Error; err != nil {
				return err
			}
			saved = *entity
			return nil
		}
		if err != nil {
			return err
		}
		existing.CanChat = entity.CanChat
		existing.CanPost = entity.CanPost
		existing.CanComment = entity.CanComment
		existing.CanUploadMedia = entity.CanUploadMedia
		existing.CanModeratePosts = entity.CanModeratePosts
		existing.CanModerateChat = entity.CanModerateChat
		existing.CanManageMembers = entity.CanManageMembers
		existing.CanManageRealm = entity.CanManageRealm
		if err := tx.Save(&existing).Error; err != nil {
			return err
		}
		saved = existing
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &saved, nil
}

// ─────────────────────────── moderation ───────────────────────────

// ModeratePost records a post removal from the realm, rejecting duplicates
// the way RealmService.ModeratePost did.
func (s *Store) ModeratePost(ctx context.Context, entity *RealmPostModerationLogEntity) error {
	existing, err := s.IsPostModerated(ctx, entity.RealmID, entity.PostID)
	if err != nil {
		return err
	}
	if existing {
		return ErrAlreadyModerated
	}
	if entity.ModeratedAt.IsZero() {
		entity.ModeratedAt = time.Now().UTC()
	}
	return s.DB.WithContext(ctx).Create(entity).Error
}

// ErrAlreadyModerated reports a duplicate post moderation.
var ErrAlreadyModerated = errors.New("realm: post already moderated")

// IsPostModerated reports whether the post was removed from the realm.
func (s *Store) IsPostModerated(ctx context.Context, realmID, postID uuid.UUID) (bool, error) {
	var count int64
	if err := s.DB.WithContext(ctx).Model(&RealmPostModerationLogEntity{}).
		Where("realm_id = ? AND post_id = ?", realmID, postID).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// ModerationLogs pages a realm's moderation log, newest first.
func (s *Store) ModerationLogs(ctx context.Context, realmID uuid.UUID, offset, take int) ([]RealmPostModerationLogEntity, int64, error) {
	query := s.DB.WithContext(ctx).Model(&RealmPostModerationLogEntity{}).Where("realm_id = ?", realmID)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var entities []RealmPostModerationLogEntity
	if err := query.Order("moderated_at DESC").
		Offset(maxInt(offset, 0)).Limit(clampTake(take, 20)).
		Find(&entities).Error; err != nil {
		return nil, 0, err
	}
	return entities, total, nil
}

// ─────────────────────────── accounts (core reads) ───────────────────────────

// AccountLookup is the subset of the core store the realm payloads hydrate
// from. It is satisfied by *store.Store.
type AccountLookup interface {
	GetAccountByID(ctx context.Context, id uuid.UUID) (*model.Account, error)
	GetAccountsByIDs(ctx context.Context, ids []uuid.UUID) ([]model.Account, error)
	GetProfilesByAccountIDs(ctx context.Context, ids []uuid.UUID) (map[string]*model.Profile, error)
	SearchAccounts(ctx context.Context, query string, limit int) ([]model.Account, error)
}

var _ AccountLookup = (*store.Store)(nil)

// HydrateMembers attaches the account payloads (and profiles, which carry the
// level the member listing sorts by) to the membership rows, mirroring
// RealmService.LoadMemberAccounts, in two queries instead of N.
func HydrateMembers(ctx context.Context, accounts AccountLookup, members []RealmMemberEntity) error {
	if accounts == nil || len(members) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, 0, len(members))
	seen := make(map[uuid.UUID]struct{}, len(members))
	for _, member := range members {
		if _, ok := seen[member.AccountID]; ok {
			continue
		}
		seen[member.AccountID] = struct{}{}
		ids = append(ids, member.AccountID)
	}
	loaded, err := accounts.GetAccountsByIDs(ctx, ids)
	if err != nil {
		return err
	}
	byID := make(map[string]*model.Account, len(loaded))
	for i := range loaded {
		byID[loaded[i].Id] = &loaded[i]
	}
	profiles, err := accounts.GetProfilesByAccountIDs(ctx, ids)
	if err != nil {
		return err
	}
	for i := range members {
		account := byID[members[i].AccountID.String()]
		if account == nil {
			continue
		}
		account.Profile = profiles[account.Id]
		if account.Profile != nil {
			account.Profile.ComputeLeveling()
		}
		members[i].Account = account
	}
	return nil
}

// ErrAccountNotFound reports a missing account for an invite target.
var ErrAccountNotFound = errors.New("realm: account not found")

// ─────────────────────────── helpers ───────────────────────────

func clampTake(value, fallback int) int {
	if value <= 0 {
		if fallback <= 0 {
			return 1
		}
		return fallback
	}
	if value > 200 {
		return 200
	}
	return value
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

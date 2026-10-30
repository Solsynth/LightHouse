package publishers

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/go/pkg/models"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/lighthouse/internal/platform/accountjson"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/ports"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

// errPublisherNotFound is returned by helpers when the row is gone.
var errPublisherNotFound = errors.New("publisher not found")

// ---------------------------------------------------------------------------
// Lookups and hydration
// ---------------------------------------------------------------------------

func (i *instance) publisherByName(ctx context.Context, name string) (*store.PublisherEntity, error) {
	return i.store.PublisherByNameCI(ctx, name)
}

func (i *instance) accountRef(account *gen.DyAccount) any {
	return accountjson.FromProto(account)
}

func (i *instance) realmRef(realm *gen.DyRealm) *model.RealmRef {
	if realm == nil {
		return nil
	}
	ref := &model.RealmRef{
		ID:          realm.GetId(),
		Slug:        realm.GetSlug(),
		Name:        realm.GetName(),
		IsPublic:    realm.GetIsPublic(),
		Description: nil,
	}
	if realm.GetDescription() != "" {
		description := realm.GetDescription()
		ref.Description = &description
	}
	if picture := realm.GetPicture(); picture != nil {
		ref.Picture = models.FromProtoValue(picture)
	}
	if background := realm.GetBackground(); background != nil {
		ref.Background = models.FromProtoValue(background)
	}
	return ref
}

func (i *instance) getAccount(ctx context.Context, id string) *gen.DyAccount {
	if id == "" {
		return nil
	}
	if i.accounts != nil {
		account, err := i.accounts.GetAccount(ctx, id)
		if err == nil && account != nil {
			return account
		}
	}
	return nil
}

func (i *instance) getAccounts(ctx context.Context, ids []string) map[string]*gen.DyAccount {
	out := map[string]*gen.DyAccount{}
	unique := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		unique = append(unique, id)
	}
	if len(unique) == 0 {
		return out
	}
	if i.accounts != nil {
		if accounts, err := i.accounts.GetAccounts(ctx, unique); err == nil {
			for _, account := range accounts {
				if account != nil {
					out[account.GetId()] = account
				}
			}
		}
	}
	return out
}

func (i *instance) realmBatch(ctx context.Context, ids []string) map[string]*gen.DyRealm {
	out := map[string]*gen.DyRealm{}
	if len(ids) == 0 || i.core == nil || i.core.Clients == nil || i.core.Clients.Realm == nil {
		return out
	}
	resp, err := i.core.Clients.Realm.GetRealmBatch(ctx, &gen.DyGetRealmBatchRequest{Ids: ids})
	if err != nil || resp == nil {
		return out
	}
	for _, realm := range resp.GetRealms() {
		if realm != nil {
			out[realm.GetId()] = realm
		}
	}
	return out
}

func (i *instance) realmBySlug(ctx context.Context, slug string) *gen.DyRealm {
	if i.core == nil || i.core.Clients == nil || i.core.Clients.Realm == nil {
		return nil
	}
	realm, err := i.core.Clients.Realm.GetRealm(ctx, &gen.DyGetRealmRequest{
		Query: &gen.DyGetRealmRequest_Slug{Slug: slug},
	})
	if err != nil {
		return nil
	}
	return realm
}

// publisherModels maps rows to wire types. Accounts are loaded for individual
// publishers only, matching LoadIndividualPublisherAccounts.
func (i *instance) publisherModels(ctx context.Context, entities []*store.PublisherEntity, withAccounts, withRealms bool) []model.Publisher {
	publishers := make([]model.Publisher, 0, len(entities))
	accountIDs := make([]string, 0, len(entities))
	for _, entity := range entities {
		publisher := store.PublisherToModel(entity)
		publishers = append(publishers, publisher)
		if withAccounts && entity.AccountID != nil && entity.Type == publisherIndividual {
			accountIDs = append(accountIDs, entity.AccountID.String())
		}
	}

	if withRealms {
		realmIDs := make([]string, 0, len(entities))
		for _, entity := range entities {
			if entity.RealmID != nil {
				realmIDs = append(realmIDs, entity.RealmID.String())
			}
		}
		realms := i.realmBatch(ctx, realmIDs)
		for index, entity := range entities {
			if entity.RealmID == nil {
				continue
			}
			if realm, ok := realms[entity.RealmID.String()]; ok {
				publishers[index].Realm = i.realmRef(realm)
			}
		}
	}

	if len(accountIDs) > 0 {
		accounts := i.getAccounts(ctx, accountIDs)
		for index, entity := range entities {
			if entity.AccountID == nil {
				continue
			}
			if account, ok := accounts[entity.AccountID.String()]; ok {
				publishers[index].Account = i.accountRef(account)
			}
		}
	}
	return publishers
}

// hydrateRealm assigns the realm snapshot to already mapped publishers.
func (i *instance) hydrateRealm(ctx context.Context, publishers []model.Publisher, entities []*store.PublisherEntity) {
	realmIDs := make([]string, 0, len(entities))
	for _, entity := range entities {
		if entity.RealmID != nil {
			realmIDs = append(realmIDs, entity.RealmID.String())
		}
	}
	realms := i.realmBatch(ctx, realmIDs)
	for index, entity := range entities {
		if entity.RealmID == nil {
			continue
		}
		if realm, ok := realms[entity.RealmID.String()]; ok {
			publishers[index].Realm = i.realmRef(realm)
		}
	}
}

// ---------------------------------------------------------------------------
// Membership
// ---------------------------------------------------------------------------

// memberRole returns the role of an account in a publisher. Pending invites
// count, matching the C# IsMemberWithRole query.
func (i *instance) memberRole(ctx context.Context, publisherID, accountID uuid.UUID) (int, bool) {
	role, ok, err := i.store.PublisherMemberRoleFor(ctx, publisherID, accountID)
	if err != nil {
		return 0, false
	}
	return role, ok
}

func (i *instance) isMemberWithRole(ctx context.Context, publisherID, accountID uuid.UUID, required int) bool {
	role, ok := i.memberRole(ctx, publisherID, accountID)
	return ok && role >= required
}

func (i *instance) memberModel(ctx context.Context, row *store.PublisherMemberRow) model.PublisherMember {
	member := model.PublisherMember{
		PublisherID: row.PublisherID.String(),
		AccountID:   row.AccountID.String(),
		JoinedAt:    store.TimePtr(row.JoinedAt),
		Role:        row.Role,
	}
	if account := i.getAccount(ctx, row.AccountID.String()); account != nil {
		member.Account = i.accountRef(account)
	}
	return member
}

func (i *instance) memberModels(ctx context.Context, rows []store.PublisherMemberRow) []model.PublisherMember {
	members := make([]model.PublisherMember, 0, len(rows))
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.AccountID.String())
	}
	accounts := i.getAccounts(ctx, ids)
	for _, row := range rows {
		member := model.PublisherMember{
			PublisherID: row.PublisherID.String(),
			AccountID:   row.AccountID.String(),
			JoinedAt:    store.TimePtr(row.JoinedAt),
			Role:        row.Role,
		}
		if account, ok := accounts[row.AccountID.String()]; ok {
			member.Account = i.accountRef(account)
		}
		members = append(members, member)
	}
	return members
}

// ---------------------------------------------------------------------------
// Subscriptions
// ---------------------------------------------------------------------------

func (i *instance) subscriptionDTO(entity *store.PublisherSubscriptionEntity, publisher *store.PublisherEntity) *subscriptionDTO {
	if entity == nil {
		return nil
	}
	dto := &subscriptionDTO{
		PublisherSubscription: model.PublisherSubscription{
			ID:                  entity.ID.String(),
			AccountID:           store.UUIDPtrString(entity.AccountID),
			FollowerPublisherID: store.UUIDPtrString(entity.FollowerPublisherID),
			PublisherID:         entity.PublisherID.String(),
			State:               entity.State,
			FollowedAt:          store.TimePtr(entity.FollowedAt),
			ReviewedAt:          store.TimePtr(entity.ReviewedAt),
			ReviewedByAccountID: store.UUIDPtrString(entity.ReviewedByAccountID),
			RejectReason:        entity.RejectReason,
			IsMuting:            entity.IsMuting,
			IsBlocking:          entity.IsBlocking,
			Notify:              entity.Notify,
			LastReadAt:          store.TimePtr(entity.LastReadAt),
			RealmID:             store.UUIDPtrString(entity.RealmID),
			EndedAt:             store.TimePtr(entity.EndedAt),
			EndReason:           entity.EndReason,
			EndedByAccountID:    store.UUIDPtrString(entity.EndedByAccountID),
			CreatedAt:           model.NewTime(entity.CreatedAt),
			UpdatedAt:           model.NewTime(entity.UpdatedAt),
			DeletedAt:           store.DeletedPtr(entity.DeletedAt),
		},
		IsActive:  entity.EndedAt == nil && entity.State == subscriptionAccepted && !entity.IsBlocking,
		IsPending: entity.EndedAt == nil && entity.State == subscriptionPending,
	}
	if publisher != nil {
		mapped := store.PublisherToModel(publisher)
		dto.Publisher = &mapped
	}
	return dto
}

func (i *instance) getSubscription(ctx context.Context, accountID, publisherID uuid.UUID) (*store.PublisherSubscriptionEntity, error) {
	var entity store.PublisherSubscriptionEntity
	err := i.store.DB.WithContext(ctx).
		Where("account_id = ? AND publisher_id = ? AND ended_at IS NULL AND state = ? AND is_blocking = ?",
			accountID, publisherID, subscriptionAccepted, false).
		First(&entity).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &entity, nil
}

func (i *instance) getSubscriptionIncludingEnded(ctx context.Context, accountID, publisherID uuid.UUID) (*store.PublisherSubscriptionEntity, error) {
	var entity store.PublisherSubscriptionEntity
	err := i.store.DB.WithContext(ctx).
		Where("account_id = ? AND publisher_id = ?", accountID, publisherID).
		First(&entity).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &entity, nil
}

func (i *instance) getFollowRequest(ctx context.Context, publisherID, accountID uuid.UUID) (*store.PublisherSubscriptionEntity, error) {
	return i.getSubscriptionIncludingEnded(ctx, accountID, publisherID)
}

func (i *instance) createSubscription(ctx context.Context, accountID, publisherID uuid.UUID) (*store.PublisherSubscriptionEntity, error) {
	existing, err := i.getSubscription(ctx, accountID, publisherID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}
	ended, err := i.getSubscriptionIncludingEnded(ctx, accountID, publisherID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if ended != nil {
		ended.EndedAt = nil
		ended.EndReason = nil
		ended.EndedByAccountID = nil
		ended.Notify = true
		ended.State = subscriptionAccepted
		ended.IsBlocking = false
		ended.FollowedAt = &now
		if err := i.store.DB.WithContext(ctx).Save(ended).Error; err != nil {
			return nil, err
		}
		i.invalidateSubscribedPublishers(ctx, accountID)
		return ended, nil
	}
	entity := &store.PublisherSubscriptionEntity{
		ID:          uuid.New(),
		AccountID:   &accountID,
		PublisherID: publisherID,
		State:       subscriptionAccepted,
		FollowedAt:  &now,
		Notify:      true,
	}
	if err := i.store.DB.WithContext(ctx).Create(entity).Error; err != nil {
		return nil, err
	}
	i.invalidateSubscribedPublishers(ctx, accountID)
	return entity, nil
}

func (i *instance) cancelSubscription(ctx context.Context, accountID, publisherID uuid.UUID) (bool, error) {
	entity, err := i.getSubscription(ctx, accountID, publisherID)
	if err != nil || entity == nil {
		return false, err
	}
	now := time.Now().UTC()
	reason := endReasonUserLeft
	entity.EndedAt = &now
	entity.EndReason = &reason
	if err := i.store.DB.WithContext(ctx).Save(entity).Error; err != nil {
		return false, err
	}
	i.invalidateSubscribedPublishers(ctx, accountID)
	return true, nil
}

func (i *instance) addSubscriber(ctx context.Context, accountID, publisherID uuid.UUID) (*store.PublisherSubscriptionEntity, error) {
	existing, err := i.getSubscriptionIncludingEnded(ctx, accountID, publisherID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if existing != nil && existing.EndedAt == nil {
		return nil, errors.New("Already subscribed")
	}
	if existing != nil && existing.EndReason != nil {
		switch *existing.EndReason {
		case endReasonRemovedByPublisher:
			return nil, errors.New("Account was removed by publisher and cannot be re-added")
		case endReasonUserLeft:
			return nil, errors.New("Account left voluntarily and cannot be re-added")
		}
	}
	if existing != nil {
		existing.EndedAt = nil
		existing.EndReason = nil
		existing.EndedByAccountID = nil
		existing.Notify = false
		existing.State = subscriptionAccepted
		existing.FollowedAt = &now
		if err := i.store.DB.WithContext(ctx).Save(existing).Error; err != nil {
			return nil, err
		}
		i.invalidateSubscribedPublishers(ctx, accountID)
		return i.getSubscription(ctx, accountID, publisherID)
	}
	entity := &store.PublisherSubscriptionEntity{
		ID:          uuid.New(),
		AccountID:   &accountID,
		PublisherID: publisherID,
		State:       subscriptionAccepted,
		FollowedAt:  &now,
		Notify:      false,
	}
	if err := i.store.DB.WithContext(ctx).Create(entity).Error; err != nil {
		return nil, err
	}
	i.invalidateSubscribedPublishers(ctx, accountID)
	return entity, nil
}

func (i *instance) removeSubscriber(ctx context.Context, accountID, publisherID, removedBy uuid.UUID) (bool, error) {
	entity, err := i.getSubscription(ctx, accountID, publisherID)
	if err != nil || entity == nil {
		return false, err
	}
	now := time.Now().UTC()
	reason := endReasonRemovedByPublisher
	entity.EndedAt = &now
	entity.EndReason = &reason
	entity.EndedByAccountID = &removedBy
	if err := i.store.DB.WithContext(ctx).Save(entity).Error; err != nil {
		return false, err
	}
	i.invalidateSubscribedPublishers(ctx, accountID)
	return true, nil
}

func (i *instance) updateNotify(ctx context.Context, accountID, publisherID uuid.UUID, notify bool) (*store.PublisherSubscriptionEntity, error) {
	entity, err := i.getSubscription(ctx, accountID, publisherID)
	if err != nil || entity == nil {
		return nil, err
	}
	entity.Notify = notify
	if err := i.store.DB.WithContext(ctx).Save(entity).Error; err != nil {
		return nil, err
	}
	return entity, nil
}

func (i *instance) updateLastReadAt(ctx context.Context, accountID, publisherID uuid.UUID, at *time.Time) (*store.PublisherSubscriptionEntity, error) {
	entity, err := i.getSubscription(ctx, accountID, publisherID)
	if err != nil || entity == nil {
		return nil, err
	}
	now := time.Now().UTC()
	if at != nil {
		now = *at
	}
	entity.LastReadAt = &now
	if err := i.store.DB.WithContext(ctx).Save(entity).Error; err != nil {
		return nil, err
	}
	return entity, nil
}

func (i *instance) markAllSubscriptionsRead(ctx context.Context, accountID uuid.UUID) (int, error) {
	var subscriptions []store.PublisherSubscriptionEntity
	if err := i.store.DB.WithContext(ctx).
		Where("account_id = ? AND ended_at IS NULL AND state = ?", accountID, subscriptionAccepted).
		Find(&subscriptions).Error; err != nil {
		return 0, err
	}
	if len(subscriptions) == 0 {
		return 0, nil
	}
	publisherIDs := make([]uuid.UUID, 0, len(subscriptions))
	for _, subscription := range subscriptions {
		publisherIDs = append(publisherIDs, subscription.PublisherID)
	}
	latest, err := i.store.LatestContentAt(ctx, publisherIDs)
	if err != nil {
		return 0, err
	}
	updated := 0
	for index := range subscriptions {
		value, ok := latest[subscriptions[index].PublisherID]
		if !ok {
			continue
		}
		if subscriptions[index].LastReadAt != nil && !subscriptions[index].LastReadAt.Before(value) {
			continue
		}
		subscriptions[index].LastReadAt = &value
		updated++
	}
	if updated > 0 {
		if err := i.store.DB.WithContext(ctx).Save(&subscriptions).Error; err != nil {
			return 0, err
		}
	}
	return updated, nil
}

func (i *instance) setPublisherBlocking(ctx context.Context, accountID, publisherID uuid.UUID, blocking bool) (*store.PublisherSubscriptionEntity, error) {
	entity, err := i.getSubscriptionIncludingEnded(ctx, accountID, publisherID)
	if err != nil {
		return nil, err
	}
	if entity == nil {
		entity = &store.PublisherSubscriptionEntity{
			ID:          uuid.New(),
			AccountID:   &accountID,
			PublisherID: publisherID,
			State:       subscriptionAccepted,
			Notify:      true,
		}
		if err := i.store.DB.WithContext(ctx).Create(entity).Error; err != nil {
			return nil, err
		}
	}
	entity.IsBlocking = blocking
	if blocking {
		entity.IsMuting = false
	}
	if err := i.store.DB.WithContext(ctx).Save(entity).Error; err != nil {
		return nil, err
	}
	i.invalidateSubscribedPublishers(ctx, accountID)
	return entity, nil
}

func (i *instance) setPublisherMuting(ctx context.Context, accountID, publisherID uuid.UUID, muting bool) (*store.PublisherSubscriptionEntity, error) {
	entity, err := i.getSubscriptionIncludingEnded(ctx, accountID, publisherID)
	if err != nil {
		return nil, err
	}
	if entity == nil {
		if !muting {
			return nil, nil
		}
		entity = &store.PublisherSubscriptionEntity{
			ID:          uuid.New(),
			AccountID:   &accountID,
			PublisherID: publisherID,
			State:       subscriptionAccepted,
			Notify:      true,
		}
		if err := i.store.DB.WithContext(ctx).Create(entity).Error; err != nil {
			return nil, err
		}
	}
	entity.IsMuting = muting
	if err := i.store.DB.WithContext(ctx).Save(entity).Error; err != nil {
		return nil, err
	}
	return entity, nil
}

func (i *instance) blockedPublishers(ctx context.Context, accountID uuid.UUID) ([]store.PublisherSubscriptionEntity, error) {
	var rows []store.PublisherSubscriptionEntity
	err := i.store.DB.WithContext(ctx).
		Where("account_id = ? AND ended_at IS NULL AND is_blocking = ?", accountID, true).
		Order("updated_at DESC").
		Find(&rows).Error
	return rows, err
}

func (i *instance) mutedPublishers(ctx context.Context, accountID uuid.UUID) ([]store.PublisherSubscriptionEntity, error) {
	var rows []store.PublisherSubscriptionEntity
	err := i.store.DB.WithContext(ctx).
		Where("account_id = ? AND ended_at IS NULL AND is_muting = ?", accountID, true).
		Order("updated_at DESC").
		Find(&rows).Error
	return rows, err
}

func (i *instance) invalidateSubscribedPublishers(ctx context.Context, accountID uuid.UUID) {
	if i.core == nil || i.core.Redis == nil {
		return
	}
	_ = i.core.Redis.Remove(ctx, fmt.Sprintf("accounts:%s:subscribed-publishers", accountID))
	_ = i.core.Redis.Remove(ctx, fmt.Sprintf("accounts:%s:publishers", accountID))
}

// ---------------------------------------------------------------------------
// Follow requests
// ---------------------------------------------------------------------------

func (i *instance) createFollowRequest(ctx context.Context, publisher *store.PublisherEntity, accountID uuid.UUID) (*store.PublisherSubscriptionEntity, error) {
	if publisher.AccountID != nil && i.core != nil && i.core.Clients != nil && i.core.Clients.Profile != nil {
		blockedStatus := int32(-100)
		response, err := i.core.Clients.Profile.GetRelationship(ctx, &gen.DyGetRelationshipRequest{
			AccountId:       accountID.String(),
			RelatedId:       publisher.AccountID.String(),
			Status:          &blockedStatus,
			EitherDirection: true,
		})
		if err == nil && response.GetRelationship() != nil {
			return nil, errors.New("You cannot follow a publisher you have blocked or who has blocked you.")
		}
	}
	existing, err := i.getSubscriptionIncludingEnded(ctx, accountID, publisher.ID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if existing.State == subscriptionPending && existing.EndedAt == nil {
			return nil, errors.New("Follow request already pending")
		}
		if existing.State == subscriptionAccepted && existing.EndedAt == nil {
			return nil, errors.New("Already following")
		}
		existing.State = subscriptionPending
		existing.EndedAt = nil
		existing.EndReason = nil
		existing.EndedByAccountID = nil
		existing.IsBlocking = false
		existing.ReviewedAt = nil
		existing.ReviewedByAccountID = nil
		existing.RejectReason = nil
		if err := i.store.DB.WithContext(ctx).Save(existing).Error; err != nil {
			return nil, err
		}
		return existing, nil
	}
	entity := &store.PublisherSubscriptionEntity{
		ID:          uuid.New(),
		AccountID:   &accountID,
		PublisherID: publisher.ID,
		State:       subscriptionPending,
		Notify:      true,
	}
	if err := i.store.DB.WithContext(ctx).Create(entity).Error; err != nil {
		return nil, err
	}
	return entity, nil
}

func (i *instance) cancelFollowRequest(ctx context.Context, publisherID, accountID uuid.UUID) error {
	return i.store.DB.WithContext(ctx).
		Where("publisher_id = ? AND account_id = ?", publisherID, accountID).
		Delete(&store.PublisherSubscriptionEntity{}).Error
}

func (i *instance) approveFollowRequest(ctx context.Context, requestID, reviewer uuid.UUID) (*store.PublisherSubscriptionEntity, error) {
	var entity store.PublisherSubscriptionEntity
	if err := i.store.DB.WithContext(ctx).First(&entity, "id = ?", requestID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("Follow request not found")
		}
		return nil, err
	}
	if entity.State != subscriptionPending {
		return nil, errors.New("Request is not pending")
	}
	now := time.Now().UTC()
	entity.State = subscriptionAccepted
	entity.FollowedAt = &now
	entity.ReviewedAt = &now
	entity.ReviewedByAccountID = &reviewer
	entity.EndedAt = nil
	entity.EndReason = nil
	entity.EndedByAccountID = nil
	if err := i.store.DB.WithContext(ctx).Save(&entity).Error; err != nil {
		return nil, err
	}
	return &entity, nil
}

func (i *instance) rejectFollowRequest(ctx context.Context, requestID, reviewer uuid.UUID, reason *string) (*store.PublisherSubscriptionEntity, error) {
	var entity store.PublisherSubscriptionEntity
	if err := i.store.DB.WithContext(ctx).First(&entity, "id = ?", requestID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("Follow request not found")
		}
		return nil, err
	}
	if entity.State != subscriptionPending {
		return nil, errors.New("Request is not pending")
	}
	now := time.Now().UTC()
	entity.State = subscriptionRejected
	entity.ReviewedAt = &now
	entity.ReviewedByAccountID = &reviewer
	entity.RejectReason = reason
	if err := i.store.DB.WithContext(ctx).Save(&entity).Error; err != nil {
		return nil, err
	}
	return &entity, nil
}

func (i *instance) pendingFollowRequests(ctx context.Context, publisherID uuid.UUID) ([]store.PublisherSubscriptionEntity, error) {
	var rows []store.PublisherSubscriptionEntity
	err := i.store.DB.WithContext(ctx).
		Where("publisher_id = ? AND state = ?", publisherID, subscriptionPending).
		Order("created_at ASC").
		Find(&rows).Error
	return rows, err
}

func (i *instance) cleanupExpiredFollowRequests(ctx context.Context) error {
	rows, err := i.store.ExpiredPendingFollowRequests(ctx, time.Now().UTC().Add(-7*24*time.Hour))
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return i.store.DB.WithContext(ctx).Delete(&store.PublisherSubscriptionEntity{}, "id IN ?", ids).Error
}

// followerPublishers loads the actor snapshot for subscription rows.
func (i *instance) followerPublishers(ctx context.Context, rows []store.PublisherSubscriptionEntity) map[string]*store.PublisherEntity {
	ids := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		if row.FollowerPublisherID != nil {
			ids = append(ids, *row.FollowerPublisherID)
		}
	}
	out := map[string]*store.PublisherEntity{}
	if len(ids) == 0 {
		return out
	}
	var entities []store.PublisherEntity
	if err := i.store.DB.WithContext(ctx).Where("id IN ?", ids).Find(&entities).Error; err != nil {
		return out
	}
	for index := range entities {
		out[entities[index].ID.String()] = &entities[index]
	}
	return out
}

// ---------------------------------------------------------------------------
// Rating
// ---------------------------------------------------------------------------

func (i *instance) addRatingRecord(ctx context.Context, reasonType, reason string, delta float64, publisherID uuid.UUID) (*store.PublisherRatingRecordEntity, error) {
	entity := &store.PublisherRatingRecordEntity{
		ID:          uuid.New(),
		ReasonType:  reasonType,
		Reason:      reason,
		Delta:       delta,
		PublisherID: publisherID,
	}
	if err := i.store.DB.WithContext(ctx).Create(entity).Error; err != nil {
		return nil, err
	}
	total, err := i.store.PublisherRatingTotal(ctx, publisherID)
	if err != nil {
		return nil, err
	}
	if err := i.store.SetPublisherRating(ctx, publisherID, total+baseRating); err != nil {
		return nil, err
	}
	if i.core != nil && i.core.Redis != nil {
		_ = i.core.Redis.Remove(ctx, "publisher_leaderboard")
		_ = i.core.Redis.Remove(ctx, fmt.Sprintf("publisher_rating:%s", publisherID))
	}
	return entity, nil
}

func (i *instance) rating(ctx context.Context, publisherID uuid.UUID) (float64, error) {
	total, err := i.store.PublisherRatingTotal(ctx, publisherID)
	if err != nil {
		return 0, err
	}
	return total + baseRating, nil
}

// ---------------------------------------------------------------------------
// Features
// ---------------------------------------------------------------------------

func (i *instance) hasFeature(ctx context.Context, publisherID uuid.UUID, flag string) bool {
	var count int64
	err := i.store.DB.WithContext(ctx).Model(&store.PublisherFeatureEntity{}).
		Where("publisher_id = ? AND flag = ?", publisherID, flag).
		Where("expired_at IS NULL OR expired_at > ?", time.Now().UTC()).
		Count(&count).Error
	return err == nil && count > 0
}

func (i *instance) setFeatureFlag(ctx context.Context, publisherID uuid.UUID, flag string) error {
	var entity store.PublisherFeatureEntity
	err := i.store.DB.WithContext(ctx).
		Where("publisher_id = ? AND flag = ?", publisherID, flag).
		First(&entity).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		entity = store.PublisherFeatureEntity{
			ID:          uuid.New(),
			PublisherID: publisherID,
			Flag:        flag,
		}
		return i.store.DB.WithContext(ctx).Create(&entity).Error
	}
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	entity.ExpiredAt = &now
	return i.store.DB.WithContext(ctx).Save(&entity).Error
}

func (i *instance) hasFollowRequiresApproval(ctx context.Context, publisher *store.PublisherEntity) bool {
	return publisher.ModerateSubscription != nil && *publisher.ModerateSubscription
}

func (i *instance) hasPostsRequireFollow(ctx context.Context, publisher *store.PublisherEntity) bool {
	return publisher.GatekeptFollows != nil && *publisher.GatekeptFollows
}

// ---------------------------------------------------------------------------
// Stats
// ---------------------------------------------------------------------------

func (i *instance) publisherStats(ctx context.Context, name string) (*model.PublisherStats, bool, error) {
	publisher, err := i.publisherByName(ctx, name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	counts, err := i.store.PublisherCountsFor(ctx, publisher.ID)
	if err != nil {
		return nil, false, err
	}
	return &model.PublisherStats{
		PostsCreated:        counts.Posts,
		StickerPacksCreated: counts.StickerPacks,
		StickersCreated:     counts.Stickers,
		UpvoteReceived:      counts.Upvotes,
		DownvoteReceived:    counts.Downvotes,
		SubscribersCount:    counts.Subscribers,
	}, true, nil
}

func (i *instance) publisherContentStats(ctx context.Context, name string) (*model.PublisherContentStats, bool, error) {
	publisher, err := i.publisherByName(ctx, name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	row, err := i.store.PublisherContentStats(ctx, publisher.ID)
	if err != nil {
		return nil, false, err
	}
	return &model.PublisherContentStats{
		PostsCount:        row.PostsCount,
		WordsCount:        row.WordsCount,
		AttachmentsCount:  int(row.AttachmentsCount),
		DaysPostedCount:   row.DaysPostedCount,
		LongestStreakDays: row.LongestStreakDays,
		FirstPostedAt:     store.TimePtr(row.FirstPostedAt),
		LastPostedAt:      store.TimePtr(row.LastPostedAt),
	}, true, nil
}

func (i *instance) publisherHeatmap(ctx context.Context, name string) (*activityHeatmap, bool, error) {
	publisher, err := i.publisherByName(ctx, name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	now := time.Now().UTC()
	start := now.Add(-365 * 24 * time.Hour)
	rows, err := i.store.PublisherHeatmap(ctx, publisher.ID, start, now)
	if err != nil {
		return nil, false, err
	}
	items := make([]activityHeatmapItem, 0, len(rows))
	for _, row := range rows {
		day := time.Date(row.Day.Year(), row.Day.Month(), row.Day.Day(), 0, 0, 0, 0, time.UTC)
		items = append(items, activityHeatmapItem{Date: model.NewTime(day), Count: row.Count})
	}
	return &activityHeatmap{
		Unit:        "posts",
		PeriodStart: model.NewTime(start),
		PeriodEnd:   model.NewTime(now),
		Items:       items,
	}, true, nil
}

// ---------------------------------------------------------------------------
// Quota
// ---------------------------------------------------------------------------

func publisherQuotaTotal(level, perkLevel int) int {
	base := 2
	if level >= 30 {
		base = 3
	}
	return base + 2*perkLevel
}

func (i *instance) quota(ctx context.Context, account *gen.DyAccount) (publisherQuotaResponse, error) {
	response := publisherQuotaResponse{Records: []publisherQuotaRecord{}}
	if account == nil {
		return response, nil
	}
	accountID, err := uuid.Parse(account.GetId())
	if err != nil {
		return response, err
	}
	profile := account.GetProfile()
	if profile == nil && i.accounts != nil {
		if hydrated, err := i.accounts.GetAccount(ctx, account.GetId()); err == nil && hydrated != nil {
			account = hydrated
			profile = hydrated.GetProfile()
		}
	}
	level := 0
	if profile != nil {
		level = int(profile.GetLevel())
	}
	perkLevel := int(account.GetPerkLevel())

	var direct []store.PublisherEntity
	if err := i.store.DB.WithContext(ctx).Where("account_id = ?", accountID).Find(&direct).Error; err != nil {
		return response, err
	}
	owned := direct

	var organizational []store.PublisherEntity
	if err := i.store.DB.WithContext(ctx).
		Where("account_id IS NULL AND realm_id IS NOT NULL").
		Find(&organizational).Error; err != nil {
		return response, err
	}
	if len(organizational) > 0 && i.core != nil && i.core.Clients != nil && i.core.Clients.Realm != nil {
		realmIDs := make([]string, 0, len(organizational))
		for _, publisher := range organizational {
			if publisher.RealmID != nil {
				realmIDs = append(realmIDs, publisher.RealmID.String())
			}
		}
		realms := i.realmBatch(ctx, realmIDs)
		for _, publisher := range organizational {
			if publisher.RealmID == nil {
				continue
			}
			if realm, ok := realms[publisher.RealmID.String()]; ok && realm.GetAccountId() == account.GetId() {
				owned = append(owned, publisher)
			}
		}
	}

	sort.SliceStable(owned, func(a, b int) bool {
		return owned[a].CreatedAt.Before(owned[b].CreatedAt)
	})

	total := publisherQuotaTotal(level, perkLevel)
	used := len(owned)
	response.Total = total
	response.Used = used
	response.Remaining = total - used
	if response.Remaining < 0 {
		response.Remaining = 0
	}
	response.Level = level
	response.PerkLevel = perkLevel
	for _, publisher := range owned {
		response.Records = append(response.Records, publisherQuotaRecord{
			ID:      publisher.ID.String(),
			Name:    publisher.Name,
			Nick:    publisher.Nick,
			Type:    publisher.Type,
			RealmID: store.UUIDPtrString(publisher.RealmID),
		})
	}
	return response, nil
}

// ---------------------------------------------------------------------------
// Rewards
// ---------------------------------------------------------------------------

func (i *instance) rewardWindow() (time.Time, time.Time) {
	now := time.Now().UTC()
	yesterday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1)
	return yesterday, yesterday.Add(24*time.Hour - time.Millisecond)
}

type rewardStat struct {
	PostCount  int
	Upvotes    int
	Downvotes  int
	AwardScore float64
}

func (i *instance) statsForWindow(ctx context.Context, from, to time.Time, publisherID *uuid.UUID) (map[uuid.UUID]rewardStat, error) {
	posts, err := i.store.PostsCreatedBetween(ctx, from, to, publisherID)
	if err != nil {
		return nil, err
	}
	stats := map[uuid.UUID]rewardStat{}
	postIDs := make([]uuid.UUID, 0, len(posts))
	postPublisher := map[uuid.UUID]uuid.UUID{}
	for _, post := range posts {
		postIDs = append(postIDs, post.ID)
		postPublisher[post.ID] = post.PublisherID
		stat := stats[post.PublisherID]
		stat.PostCount++
		stat.AwardScore += post.AwardedScore
		stats[post.PublisherID] = stat
	}
	reactions, err := i.store.ReactionCountsForPosts(ctx, postIDs)
	if err != nil {
		return nil, err
	}
	for _, reaction := range reactions {
		publisherID, ok := postPublisher[reaction.PostID]
		if !ok {
			continue
		}
		stat := stats[publisherID]
		switch reaction.Attitude {
		case 1:
			stat.Upvotes += reaction.Total
		case 2:
			stat.Downvotes += reaction.Total
		}
		stats[publisherID] = stat
	}
	return stats, nil
}

func (i *instance) expectedReward(ctx context.Context, publisherID uuid.UUID) (rewardPreview, error) {
	from, to := i.rewardWindow()
	stats, err := i.statsForWindow(ctx, from, to, &publisherID)
	if err != nil {
		return rewardPreview{}, err
	}
	stat, ok := stats[publisherID]
	if !ok {
		return rewardPreview{}, nil
	}
	points := float64(stat.Upvotes-stat.Downvotes) + stat.AwardScore*0.1
	return rewardPreview{
		Experience:    stat.PostCount*100 + int(points*10),
		SocialCredits: int(points * 10),
	}, nil
}

func (i *instance) settlePublisherRewards(ctx context.Context) error {
	from, to := i.rewardWindow()
	stats, err := i.statsForWindow(ctx, from, to, nil)
	if err != nil {
		return err
	}
	for publisherID, stat := range stats {
		points := float64(stat.Upvotes-stat.Downvotes) + stat.AwardScore*0.1
		ratingDelta := int(points)
		if ratingDelta == 0 {
			continue
		}
		publisher, err := i.store.GetPublisher(ctx, publisherID)
		if err != nil {
			continue
		}
		reason := fmt.Sprintf("Publishing reward for @%s on %s", publisher.Name, time.Now().UTC().Format("2006-01-02"))
		if _, err := i.addRatingRecord(ctx, "publishers.rewards", reason, float64(ratingDelta), publisherID); err != nil {
			i.log.Warn("publisher reward rating update failed", "publisher", publisherID, "error", err)
		}
		// Experience and social-credit grants live in Leveling; no port exposes
		// them yet, so only the rating leg is applied.
		i.log.Debug("publisher reward experience skipped", "publisher", publisherID, "experience", stat.PostCount*100+int(points*10))
	}
	return nil
}

func (i *instance) settlePostAwards(ctx context.Context) (int, error) {
	awards, err := i.store.UnsettledPositiveAwards(ctx)
	if err != nil {
		return 0, err
	}
	if len(awards) == 0 {
		return 0, nil
	}
	var settlementErr error
	settled := make([]uuid.UUID, 0, len(awards))
	for _, award := range awards {
		if err := i.settleAward(ctx, award); err != nil {
			if settlementErr == nil {
				settlementErr = err
			}
			continue
		}
		settled = append(settled, award.ID)
	}
	if len(settled) > 0 {
		if err := i.store.MarkAwardsSettled(ctx, settled, time.Now().UTC()); err != nil {
			return len(settled), err
		}
	}
	return len(settled), settlementErr
}

func (i *instance) settleAward(ctx context.Context, award store.PostAwardWithPublisher) error {
	if i.core != nil && i.core.Clients != nil && i.core.Clients.Merch != nil {
		awardID := award.ID.String()
		_, err := i.core.Clients.Merch.CreateMerchantSettlement(ctx, &gen.DyCreateMerchantSettlementRequest{
			PublisherId: award.PublisherID.String(),
			AwardId:     &awardID,
			Currency:    "points",
			Amount:      fmt.Sprintf("%v", award.Amount),
		})
		return err
	}
	if i.payments != nil {
		_, err := i.payments.CreateOrder(ctx, award.PublisherID.String(), "publisher.award.settlement",
			fmt.Sprintf("%v", award.Amount), "points", map[string]any{
				"publisher_id": award.PublisherID.String(),
				"award_id":     award.ID.String(),
			})
		return err
	}
	return ports.ErrUnavailable
}

func (i *instance) aggressiveResettle(ctx context.Context, from, to time.Time, publisherID *uuid.UUID) (map[uuid.UUID]float64, error) {
	totals := map[uuid.UUID]rewardStat{}
	current := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, time.UTC)
	end := time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, time.UTC)
	for !current.After(end) {
		dayStats, err := i.statsForWindow(ctx, current, current.Add(24*time.Hour-time.Millisecond), publisherID)
		if err != nil {
			return nil, err
		}
		for id, stat := range dayStats {
			existing := totals[id]
			existing.PostCount += stat.PostCount
			existing.Upvotes += stat.Upvotes
			existing.Downvotes += stat.Downvotes
			existing.AwardScore += stat.AwardScore
			totals[id] = existing
		}
		current = current.AddDate(0, 0, 1)
	}
	results := map[uuid.UUID]float64{}
	for id, stat := range totals {
		points := float64(stat.Upvotes-stat.Downvotes) + stat.AwardScore*0.1
		delta := int(points)
		if delta == 0 {
			continue
		}
		reason := fmt.Sprintf("Resettle %s to %s", from.UTC().Format("2006-01-02"), to.UTC().Format("2006-01-02"))
		if _, err := i.addRatingRecord(ctx, "publishers.resettle", reason, float64(delta), id); err != nil {
			return nil, err
		}
		results[id] = float64(delta)
	}
	return results, nil
}

// ---------------------------------------------------------------------------
// Fediverse actor
// ---------------------------------------------------------------------------

func (i *instance) activityDomain() string {
	if i.core != nil && i.core.Config != nil && strings.TrimSpace(i.core.Config.ActivityPub.Domain) != "" {
		return i.core.Config.ActivityPub.Domain
	}
	return "localhost"
}

func (i *instance) localActor(ctx context.Context, publisherID uuid.UUID) *store.PublisherEntity {
	var entity store.PublisherEntity
	err := i.store.DB.WithContext(ctx).
		Where("id = ? AND uri IS NOT NULL", publisherID).
		First(&entity).Error
	if err != nil {
		return nil
	}
	return &entity
}

func (i *instance) fediverseStatus(ctx context.Context, publisher *store.PublisherEntity) (fediverseStatus, error) {
	status := fediverseStatus{}
	actor := i.localActor(ctx, publisher.ID)
	if actor != nil {
		mapped := store.PublisherToModel(actor)
		status.Enabled = true
		status.Actor = &mapped
		status.ActorURI = actor.URI
	}
	var count int64
	if err := i.store.DB.WithContext(ctx).Model(&store.PublisherSubscriptionEntity{}).
		Where("publisher_id = ? AND state = ? AND ended_at IS NULL AND is_blocking = ?",
			publisher.ID, subscriptionAccepted, false).
		Count(&count).Error; err != nil {
		return status, err
	}
	status.FollowerCount = int(count)
	return status, nil
}

func (i *instance) enableFediverse(ctx context.Context, publisher *store.PublisherEntity) error {
	if publisher.URI != nil {
		return errors.New("Fediverse actor already exists for this publisher")
	}
	domain := i.activityDomain()
	var instance store.FediverseInstanceEntity
	err := i.store.DB.WithContext(ctx).Where("domain = ?", domain).First(&instance).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		name := domain
		instance = store.FediverseInstanceEntity{ID: uuid.New(), Domain: domain, Name: &name}
		if err := i.store.DB.WithContext(ctx).Create(&instance).Error; err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	privateKey, publicKey, err := generateRSAKeyPair()
	if err != nil {
		return err
	}
	actorURL := fmt.Sprintf("https://%s/activitypub/actors/%s", domain, publisher.Name)
	now := time.Now().UTC()
	publisher.URI = &actorURL
	actorType := "Person"
	publisher.ActorType = &actorType
	username := publisher.Name
	publisher.Username = &username
	publisher.InstanceID = &instance.ID
	publisher.InstanceDomain = &domain
	inbox := actorURL + "/inbox"
	outbox := actorURL + "/outbox"
	followers := actorURL + "/followers"
	following := actorURL + "/following"
	featured := actorURL + "/featured"
	keyID := actorURL + "#main-key"
	publisher.InboxURI = &inbox
	publisher.OutboxURI = &outbox
	publisher.FollowersURI = &followers
	publisher.FollowingURI = &following
	publisher.FeaturedURI = &featured
	publisher.PublicKeyID = &keyID
	publisher.PublicKey = &publicKey
	publisher.LastActivityAt = &now
	if err := i.store.DB.WithContext(ctx).Save(publisher).Error; err != nil {
		return err
	}
	key := store.FediverseKeyEntity{
		ID:            uuid.New(),
		KeyID:         keyID,
		KeyPEM:        publicKey,
		PrivateKeyPEM: &privateKey,
		Algorithm:     "rsa-sha256",
		PublisherID:   &publisher.ID,
	}
	return i.store.DB.WithContext(ctx).Create(&key).Error
}

func (i *instance) disableFediverse(ctx context.Context, publisher *store.PublisherEntity) error {
	if publisher.URI == nil {
		return nil
	}
	publisher.URI = nil
	publisher.ActorType = nil
	publisher.Username = nil
	publisher.InstanceID = nil
	publisher.InstanceDomain = nil
	publisher.InboxURI = nil
	publisher.OutboxURI = nil
	publisher.FollowersURI = nil
	publisher.FollowingURI = nil
	publisher.FeaturedURI = nil
	publisher.PublicKeyID = nil
	publisher.PublicKey = nil
	return i.store.DB.WithContext(ctx).Save(publisher).Error
}

func (i *instance) updateFediverseActor(ctx context.Context, publisher *store.PublisherEntity, request updateFediverseActorRequest) error {
	if publisher.URI == nil {
		return errors.New("Fediverse is not enabled for this publisher")
	}
	if request.ActorType != nil {
		switch *request.ActorType {
		case "Person", "Service", "Group", "Organization", "Application":
			actorType := *request.ActorType
			publisher.ActorType = &actorType
		default:
			return fmt.Errorf("Unsupported actor type '%s'. Supported: Person, Service, Group, Organization, Application", *request.ActorType)
		}
	}
	if request.IsLocked != nil {
		publisher.IsLocked = *request.IsLocked
	}
	if request.IsDiscoverable != nil {
		publisher.IsDiscoverable = *request.IsDiscoverable
	}
	now := time.Now().UTC()
	publisher.LastActivityAt = &now
	return i.store.DB.WithContext(ctx).Save(publisher).Error
}

// verificationJSON encodes a verification mark.
func verificationJSON(mark *model.VerificationMark) datatypes.JSON {
	if mark == nil {
		return nil
	}
	return store.ToJSON(mark)
}

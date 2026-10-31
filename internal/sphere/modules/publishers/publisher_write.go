package publishers

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/lighthouse/shared/pkg/errs"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"

	"src.solsynth.dev/sosys/lighthouse/shared/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/httpx"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

// ensureQuota mirrors the quota gate on publisher creation.
func (i *instance) ensureQuota(c *gin.Context, accountID uuid.UUID) bool {
	user := i.getAccount(c.Request.Context(), accountID.String())
	if user == nil {
		user = authn.CurrentUser(c)
	}
	quota, err := i.quota(c.Request.Context(), user)
	if err != nil {
		i.internal(c)
		return false
	}
	if quota.Used >= quota.Total {
		i.forbidden(c, "Publisher quota exceeded ("+itoa(quota.Used)+"/"+itoa(quota.Total)+").")
		return false
	}
	return true
}

func (i *instance) handleCreateIndividual(c *gin.Context) {
	var request publisherRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	if !i.validatePublisherPayload(c, request, true) {
		return
	}
	accountID, ok := i.accountID(c)
	if !ok {
		return
	}
	if !i.ensureQuota(c, accountID) {
		return
	}
	name := strings.TrimSpace(*request.Name)
	taken, err := i.store.PublisherNamesTaken(c.Request.Context(), name, uuid.Nil)
	if err != nil {
		i.internal(c)
		return
	}
	if taken {
		i.badRequest(c, "PUBLISHER_NAME_TAKEN",
			"The name you requested has already be taken, if it is your account name, "+
				"you can request a taken down to the publisher which created with your name firstly to get your name back.")
		return
	}
	picture, ok := i.fileReference(c, request.PictureID)
	if !ok {
		return
	}
	background, ok := i.fileReference(c, request.BackgroundID)
	if !ok {
		return
	}

	user := i.getAccount(c.Request.Context(), accountID.String())
	if user == nil {
		user = authn.CurrentUser(c)
	}
	if picture == nil {
		picture = i.accountProfilePicture(user)
	}
	if background == nil {
		background = i.accountProfileBackground(user)
	}
	bio := request.Bio
	if bio == nil {
		bio = i.accountProfileBio(user)
	}

	entity := store.PublisherEntity{
		ID:         uuid.New(),
		Type:       publisherIndividual,
		Name:       name,
		Nick:       strings.TrimSpace(*request.Nick),
		Bio:        bio,
		Picture:    picture,
		Background: background,
		AccountID:  &accountID,
	}
	if err := i.store.DB.WithContext(c.Request.Context()).Create(&entity).Error; err != nil {
		i.internal(c)
		return
	}
	now := time.Now().UTC()
	if err := i.addMember(c.Request.Context(), entity.ID, accountID, roleOwner, &now); err != nil {
		i.log.Warn("publisher owner membership insert failed", "publisher", entity.ID, "error", err)
	}
	httpx.OK(c, i.publisherModelFor(c.Request.Context(), &entity, false))
}

func (i *instance) handleCreateOrganization(c *gin.Context) {
	var request publisherRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	if !i.validatePublisherPayload(c, request, true) {
		return
	}
	accountID, ok := i.accountID(c)
	if !ok {
		return
	}
	if !i.ensureQuota(c, accountID) {
		return
	}
	realm := i.realmBySlug(c.Request.Context(), c.Param("realmSlug"))
	if realm == nil {
		httpx.Fail(c, errs.New("REALM_NOT_FOUND", "Realm not found", 404))
		return
	}
	if i.core.Clients == nil || i.core.Clients.Realm == nil {
		i.unavailable(c)
		return
	}
	member, err := i.core.Clients.Realm.IsMemberWithRole(c.Request.Context(), &gen.DyIsMemberWithRoleRequest{
		RealmId:       realm.GetId(),
		AccountId:     accountID.String(),
		RequiredRoles: []int32{50},
	})
	if err != nil || !member.GetValue() {
		i.forbidden(c, "You need to be a moderator of the realm to create an organization publisher")
		return
	}
	name := strings.TrimSpace(*request.Name)
	taken, err := i.store.PublisherNamesTaken(c.Request.Context(), name, uuid.Nil)
	if err != nil {
		i.internal(c)
		return
	}
	if taken {
		i.badRequest(c, "PUBLISHER_NAME_TAKEN", "The name you requested has already been taken")
		return
	}
	picture, ok := i.fileReference(c, request.PictureID)
	if !ok {
		return
	}
	background, ok := i.fileReference(c, request.BackgroundID)
	if !ok {
		return
	}
	user := i.getAccount(c.Request.Context(), accountID.String())
	if user == nil {
		user = authn.CurrentUser(c)
	}
	if picture == nil {
		picture = i.accountProfilePicture(user)
	}
	if background == nil {
		background = i.accountProfileBackground(user)
	}
	bio := request.Bio
	if bio == nil && realm.GetDescription() != "" {
		description := realm.GetDescription()
		bio = &description
	}

	realmID, err := uuid.Parse(realm.GetId())
	if err != nil {
		i.internal(c)
		return
	}
	entity := store.PublisherEntity{
		ID:         uuid.New(),
		Type:       publisherOrganizational,
		Name:       name,
		Nick:       strings.TrimSpace(*request.Nick),
		Bio:        bio,
		Picture:    picture,
		Background: background,
		RealmID:    &realmID,
	}
	if err := i.store.DB.WithContext(c.Request.Context()).Create(&entity).Error; err != nil {
		i.internal(c)
		return
	}
	now := time.Now().UTC()
	if err := i.addMember(c.Request.Context(), entity.ID, accountID, roleOwner, &now); err != nil {
		i.log.Warn("publisher owner membership insert failed", "publisher", entity.ID, "error", err)
	}
	mapped := i.publisherModelFor(c.Request.Context(), &entity, false)
	mapped.Realm = i.realmRef(realm)
	httpx.OK(c, mapped)
}

func (i *instance) handleUpdatePublisher(c *gin.Context) {
	accountID, ok := i.accountID(c)
	if !ok {
		return
	}
	var request publisherRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	if !i.validatePublisherPayload(c, request, false) {
		return
	}
	publisher, ok := i.publisherOr404(c, c.Param("name"))
	if !ok {
		return
	}
	role, member := i.memberRole(c.Request.Context(), publisher.ID, accountID)
	if !member {
		i.forbidden(c, "You are not even a member of the targeted publisher.")
		return
	}
	if role < roleManager {
		i.forbidden(c, "You need at least be the manager to update the publisher profile.")
		return
	}
	if request.Nick != nil {
		publisher.Nick = strings.TrimSpace(*request.Nick)
	}
	if request.Bio != nil {
		publisher.Bio = request.Bio
	}
	picture, ok := i.fileReference(c, request.PictureID)
	if !ok {
		return
	}
	if picture != nil {
		publisher.Picture = picture
	}
	background, ok := i.fileReference(c, request.BackgroundID)
	if !ok {
		return
	}
	if background != nil {
		publisher.Background = background
	}
	if request.DefaultPostTags != nil || request.DefaultPostCategories != nil {
		meta := map[string]any{}
		if len(publisher.Meta) > 0 {
			_ = json.Unmarshal(publisher.Meta, &meta)
		}
		if request.DefaultPostTags != nil {
			meta["default_post_tags"] = normalizeTagList(request.DefaultPostTags)
		}
		if request.DefaultPostCategories != nil {
			meta["default_post_categories"] = normalizeTagList(request.DefaultPostCategories)
		}
		publisher.Meta = store.ToJSON(meta)
	}
	if request.PayoutWalletID != nil && role >= roleOwner {
		walletID, err := uuid.Parse(*request.PayoutWalletID)
		if err != nil {
			httpx.Fail(c, validationError("payout_wallet_id", "The value is not a valid UUID."))
			return
		}
		publisher.PayoutWalletID = &walletID
	}
	if err := i.store.DB.WithContext(c.Request.Context()).Save(publisher).Error; err != nil {
		i.internal(c)
		return
	}
	if request.PayoutWalletID != nil && i.core.Clients != nil && i.core.Clients.Merch != nil {
		upsert := &gen.DyUpsertMerchantRequest{PublisherId: publisher.ID.String()}
		if publisher.PayoutWalletID != nil {
			wallet := publisher.PayoutWalletID.String()
			upsert.PaymentWalletId = &wallet
		}
		name := publisher.Name
		upsert.Name = &name
		if _, err := i.core.Clients.Merch.UpsertMerchant(c.Request.Context(), upsert); err != nil {
			i.log.Warn("publisher merchant upsert failed", "publisher", publisher.ID, "error", err)
		}
	}
	httpx.OK(c, i.publisherModelFor(c.Request.Context(), publisher, false))
}

func (i *instance) handleDeletePublisher(c *gin.Context) {
	accountID, ok := i.accountID(c)
	if !ok {
		return
	}
	publisher, ok := i.publisherOr404(c, c.Param("name"))
	if !ok {
		return
	}
	role, member := i.memberRole(c.Request.Context(), publisher.ID, accountID)
	if !member {
		i.forbidden(c, "You are not even a member of the targeted publisher.")
		return
	}
	if role < roleOwner {
		i.forbidden(c, "You need to be the owner to delete the publisher.")
		return
	}
	if err := i.store.DB.WithContext(c.Request.Context()).Delete(publisher).Error; err != nil && !errors.Is(err, store.ErrNotFound) {
		i.internal(c)
		return
	}
	httpx.NoContent(c)
}

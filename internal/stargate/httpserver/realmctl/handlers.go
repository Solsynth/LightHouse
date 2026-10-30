package realmctl

import (
	"errors"
	"net/http"
	"sort"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/go/pkg/errs"
	"src.solsynth.dev/sosys/stargate/middleware"
	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/realm"
)

// ─────────────────────────── realms ───────────────────────────

// listCommunityRealms mirrors RealmPublicController.ListCommunityRealms:
// community realms ordered by member count, with an ILIKE name/description
// filter and the X-Total header.
func (d Deps) listCommunityRealms(c *gin.Context) {
	query := c.Query("query")
	take := parseInt(c.DefaultQuery("take", "10"), 10)
	offset := parseInt(c.DefaultQuery("offset", "0"), 0)
	entities, total, err := d.Store.ListCommunityRealms(c.Request.Context(), query, offset, take)
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	if err := d.Service.RefreshBoosts(c.Request.Context(), entities); err != nil {
		internalError(c, d.Log, err)
		return
	}
	setTotal(c, total)
	c.JSON(http.StatusOK, realmProjection(entities))
}

// getQuota mirrors GET /api/realms/quota.
func (d Deps) getQuota(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	id, ok := accountIDOf(account)
	if !ok {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized("Authentication is required."))
		return
	}
	account = d.callerWithProfile(c, account)
	quota, err := d.Service.Quota(c.Request.Context(), id, profileLevel(account), account.PerkLevel)
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	c.JSON(http.StatusOK, quota)
}

// getRealm mirrors GET /:slug (anonymous).
func (d Deps) getRealm(c *gin.Context) {
	entity := d.loadRealm(c)
	if entity == nil {
		return
	}
	c.JSON(http.StatusOK, realm.RealmFromEntity(entity))
}

// loadRealm is the shared :slug resolution that refreshes boost state.
func (d Deps) loadRealm(c *gin.Context) *realm.RealmEntity {
	return loadRealm(c, d)
}

// listJoinedRealms mirrors GET /api/realms: the realms the caller belongs to.
func (d Deps) listJoinedRealms(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	id, ok := accountIDOf(account)
	if !ok {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized("Authentication is required."))
		return
	}
	entities, err := d.Store.JoinedRealms(c.Request.Context(), id)
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	if err := d.Service.RefreshBoosts(c.Request.Context(), entities); err != nil {
		internalError(c, d.Log, err)
		return
	}
	c.JSON(http.StatusOK, realmProjection(entities))
}

// listInvites mirrors GET /api/realms/invites: pending memberships for the
// caller, hydrated with account payloads.
func (d Deps) listInvites(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	id, ok := accountIDOf(account)
	if !ok {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized("Authentication is required."))
		return
	}
	rows, err := d.Store.PendingInvites(c.Request.Context(), id)
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	members, err := memberPayload(c.Request.Context(), d, rows)
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	// The invite listing embeds the realm, which the C# Include(e => e.Realm)
	// populated; resolve them in one read.
	realms, err := d.Store.GetRealmsByIDs(c.Request.Context(), realmIDsOf(rows))
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	byID := map[uuid.UUID]*realm.RealmEntity{}
	for i := range realms {
		byID[realms[i].ID] = &realms[i]
	}
	for _, member := range members {
		if entity, ok := byID[member.RealmId]; ok {
			member.Realm = realm.RealmFromEntity(entity)
		}
	}
	c.JSON(http.StatusOK, members)
}

// createRealm mirrors POST /api/realms: quota check, slug conflict, owner
// membership, optional picture/background resolution.
func (d Deps) createRealm(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	accountID, ok := accountIDOf(account)
	if !ok {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized("Authentication is required."))
		return
	}
	var request realmRequest
	if !bindJSON(c, &request) {
		return
	}
	if isBlank(request.Name) {
		badRequest(c, "REALM_NAME_REQUIRED", "You cannot create a realm without a name.")
		return
	}
	if isBlank(request.Slug) {
		badRequest(c, "REALM_SLUG_REQUIRED", "You cannot create a realm without a slug.")
		return
	}
	account = d.callerWithProfile(c, account)
	quota, err := d.Service.Quota(c.Request.Context(), accountID, profileLevel(account), account.PerkLevel)
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	if quota.Used >= quota.Total {
		forbidden(c, "Realm quota exceeded ("+itoa(quota.Used)+"/"+itoa(quota.Total)+").")
		return
	}
	taken, err := d.Store.RealmSlugTaken(c.Request.Context(), derefString(request.Slug), nil)
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	if taken {
		badRequest(c, "REALM_SLUG_CONFLICT", "Realm with this slug already exists.")
		return
	}

	entity := &realm.RealmEntity{
		ID: uuid.New(), Slug: derefString(request.Slug), Name: derefString(request.Name),
		Description: derefString(request.Description), AccountID: accountID,
		IsCommunity: boolValue(request.IsCommunity), IsPublic: boolValue(request.IsPublic),
	}
	if !d.resolveArtwork(c, request.PictureId, &entity.Picture, "REALM_PICTURE_NOT_FOUND",
		"Invalid picture id, unable to find the file on cloud.") {
		return
	}
	if !d.resolveArtwork(c, request.BackgroundId, &entity.Background, "REALM_BACKGROUND_NOT_FOUND",
		"Invalid background id, unable to find the file on cloud.") {
		return
	}
	if err := d.Store.CreateRealmWithOwner(c.Request.Context(), entity, nowUTC()); err != nil {
		internalError(c, d.Log, err)
		return
	}
	d.logAction(c, account.Id, model.ActionLogRealmCreate, map[string]any{
		"realm_id": entity.ID.String(), "name": entity.Name, "slug": entity.Slug,
		"is_community": entity.IsCommunity, "is_public": entity.IsPublic,
	})
	c.JSON(http.StatusOK, realm.RealmFromEntity(entity))
}

// updateRealm mirrors PATCH /:slug: moderator-only, slug uniqueness, optional
// artwork replacement.
func (d Deps) updateRealm(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	entity := d.loadRealm(c)
	if entity == nil {
		return
	}
	accountID, _ := accountIDOf(account)
	role, member, err := d.Store.MemberRole(c.Request.Context(), entity.ID, accountID)
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	if !member || role < realm.RoleModerator {
		forbidden(c, "You do not have permission to update this realm.")
		return
	}
	var request realmRequest
	if !bindJSON(c, &request) {
		return
	}
	if request.Slug != nil && *request.Slug != entity.Slug {
		taken, err := d.Store.RealmSlugTaken(c.Request.Context(), *request.Slug, &entity.ID)
		if err != nil {
			internalError(c, d.Log, err)
			return
		}
		if taken {
			badRequest(c, "REALM_SLUG_CONFLICT", "Realm with this slug already exists.")
			return
		}
		entity.Slug = *request.Slug
	}
	if request.Name != nil {
		entity.Name = *request.Name
	}
	if request.Description != nil {
		entity.Description = *request.Description
	}
	if request.IsCommunity != nil {
		entity.IsCommunity = *request.IsCommunity
	}
	if request.IsPublic != nil {
		entity.IsPublic = *request.IsPublic
	}
	if !d.resolveArtwork(c, request.PictureId, &entity.Picture, "REALM_PICTURE_NOT_FOUND",
		"Invalid picture id, unable to find the file on cloud.") {
		return
	}
	if !d.resolveArtwork(c, request.BackgroundId, &entity.Background, "REALM_BACKGROUND_NOT_FOUND",
		"Invalid background id, unable to find the file on cloud.") {
		return
	}
	if err := d.Store.SaveRealm(c.Request.Context(), entity); err != nil {
		internalError(c, d.Log, err)
		return
	}
	d.logAction(c, account.Id, model.ActionLogRealmUpdate, map[string]any{
		"realm_id":     entity.ID.String(),
		"name_updated": request.Name != nil, "slug_updated": request.Slug != nil,
		"description_updated": request.Description != nil,
		"picture_updated":     request.PictureId != nil, "background_updated": request.BackgroundId != nil,
		"is_community_updated": request.IsCommunity != nil, "is_public_updated": request.IsPublic != nil,
	})
	c.JSON(http.StatusOK, realm.RealmFromEntity(entity))
}

// deleteRealm mirrors DELETE /:slug: owner only, cascades the memberships.
func (d Deps) deleteRealm(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	entity := d.loadRealm(c)
	if entity == nil {
		return
	}
	accountID, _ := accountIDOf(account)
	ok, err := d.Service.IsMemberWithRole(c.Request.Context(), entity.ID, accountID, realm.RoleOwner)
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	if !ok {
		forbidden(c, "Only the owner can delete this realm.")
		return
	}
	if err := d.Store.DeleteRealm(c.Request.Context(), entity.ID, nowUTC()); err != nil {
		internalError(c, d.Log, err)
		return
	}
	d.logAction(c, account.Id, model.ActionLogRealmDelete, map[string]any{
		"realm_id": entity.ID.String(), "realm_name": entity.Name, "realm_slug": entity.Slug,
	})
	c.Status(http.StatusNoContent)
}

// ─────────────────────────── members ───────────────────────────

// listMembers mirrors GET /:slug/members. A private realm requires a joined
// member; withStatus sorts by live presence and paginates after sorting.
func (d Deps) listMembers(c *gin.Context) {
	entity := d.loadRealm(c)
	if entity == nil {
		return
	}
	viewer := currentViewer(c)
	if !entity.IsPublic {
		if viewer == nil {
			c.JSON(http.StatusUnauthorized, errs.Unauthorized("Authentication is required."))
			return
		}
		viewerID, _ := accountIDOf(viewer)
		ok, err := d.Service.IsMemberWithRole(c.Request.Context(), entity.ID, viewerID, realm.RoleNormal)
		if err != nil {
			internalError(c, d.Log, err)
			return
		}
		if !ok {
			forbidden(c, "You must be a member to view this realm's members.")
			return
		}
	}
	filter := realm.MemberFilter{
		RealmID: entity.ID, JoinedOnly: true,
		Offset: parseInt(c.DefaultQuery("offset", "0"), 0),
		Take:   parseInt(c.DefaultQuery("take", "20"), 20),
	}
	if raw := c.Query("labelId"); raw != "" {
		if labelID, err := uuid.Parse(raw); err == nil {
			filter.LabelID = &labelID
		}
	}
	if name := c.Query("accountName"); name != "" {
		accounts, err := d.Accounts.SearchAccounts(c.Request.Context(), name, 100)
		if err != nil {
			internalError(c, d.Log, err)
			return
		}
		if len(accounts) == 0 {
			setTotal(c, 0)
			c.JSON(http.StatusOK, []*realm.Member{})
			return
		}
		filter.AccountIDs = make([]uuid.UUID, 0, len(accounts))
		for _, account := range accounts {
			if id, err := uuid.Parse(account.Id); err == nil {
				filter.AccountIDs = append(filter.AccountIDs, id)
			}
		}
	}

	if !queryBool(c, "withStatus") {
		rows, total, err := d.Store.ListMembers(c.Request.Context(), filter)
		if err != nil {
			internalError(c, d.Log, err)
			return
		}
		members, err := memberPayload(c.Request.Context(), d, rows)
		if err != nil {
			internalError(c, d.Log, err)
			return
		}
		setTotal(c, total)
		c.JSON(http.StatusOK, members)
		return
	}

	// withStatus=true: the C# path loaded every matching member, attached
	// statuses, sorted by presence then level, and paginated the result.
	all := filter
	all.Offset, all.Take = 0, 200
	rows, _, err := d.Store.ListMembers(c.Request.Context(), all)
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	members, err := memberPayload(c.Request.Context(), d, rows)
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	d.attachStatuses(c, members, viewer)
	sortMembersByPresence(members)
	setTotal(c, int64(len(members)))
	start := minInt(filter.Offset, len(members))
	end := minInt(start+filter.Take, len(members))
	c.JSON(http.StatusOK, members[start:end])
}

// currentMembership mirrors GET /:slug/members/me.
func (d Deps) currentMembership(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	entity := d.loadRealm(c)
	if entity == nil {
		return
	}
	accountID, _ := accountIDOf(account)
	member, err := d.Store.GetActiveMember(c.Request.Context(), entity.ID, accountID)
	if errors.Is(err, realm.ErrNotFound) {
		memberNotFound(c)
		return
	}
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	payload, err := memberPayload(c.Request.Context(), d, []realm.RealmMemberEntity{*member})
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	if len(payload) == 0 {
		memberNotFound(c)
		return
	}
	payload[0].Realm = realm.RealmFromEntity(entity)
	c.JSON(http.StatusOK, payload[0])
}

// updateMembershipProfile mirrors PATCH /:slug/members/me/profile: perk level
// 2 is required to customise the realm identity.
func (d Deps) updateMembershipProfile(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	if account.PerkLevel < 2 {
		forbidden(c, "Perk level 2 is required to customize realm profile.")
		return
	}
	entity := d.loadRealm(c)
	if entity == nil {
		return
	}
	var request memberProfileRequest
	if !bindJSON(c, &request) {
		return
	}
	accountID, _ := accountIDOf(account)
	member, err := d.Store.GetActiveMember(c.Request.Context(), entity.ID, accountID)
	if errors.Is(err, realm.ErrNotFound) {
		memberNotFound(c)
		return
	}
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	if request.Nick != nil {
		if exceeded(*request.Nick, 1024) {
			validation(c, "nick")
			return
		}
	}
	if request.Bio != nil {
		if exceeded(*request.Bio, 4096) {
			validation(c, "bio")
			return
		}
	}
	member.Nick = request.Nick
	member.Bio = request.Bio
	if err := d.Store.SaveMember(c.Request.Context(), member); err != nil {
		internalError(c, d.Log, err)
		return
	}
	d.respondMember(c, entity, member)
}

// resetMembershipProfile mirrors DELETE /:slug/members/me/profile.
func (d Deps) resetMembershipProfile(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	entity := d.loadRealm(c)
	if entity == nil {
		return
	}
	accountID, _ := accountIDOf(account)
	member, err := d.Store.GetActiveMember(c.Request.Context(), entity.ID, accountID)
	if errors.Is(err, realm.ErrNotFound) {
		memberNotFound(c)
		return
	}
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	member.Nick = nil
	member.Bio = nil
	if err := d.Store.SaveMember(c.Request.Context(), member); err != nil {
		internalError(c, d.Log, err)
		return
	}
	d.respondMember(c, entity, member)
}

// respondMember re-reads the membership after a write and emits it with the
// realm and account navigations the C# LoadMemberAccount filled.
func (d Deps) respondMember(c *gin.Context, entity *realm.RealmEntity, member *realm.RealmMemberEntity) {
	payload, err := memberPayload(c.Request.Context(), d, []realm.RealmMemberEntity{*member})
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	if len(payload) == 0 {
		memberNotFound(c)
		return
	}
	payload[0].Realm = realm.RealmFromEntity(entity)
	c.JSON(http.StatusOK, payload[0])
}

// inviteMember mirrors POST /invites/:slug.
func (d Deps) inviteMember(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	var request memberRequest
	if !bindJSON(c, &request) {
		return
	}
	if request.RelatedUserId == uuid.Nil {
		validation(c, "related_user_id")
		return
	}
	related, err := d.Accounts.GetAccountByID(c.Request.Context(), request.RelatedUserId)
	if err != nil {
		badRequest(c, "REALM_USER_NOT_FOUND", "Related user was not found")
		return
	}
	inviterID, _ := accountIDOf(account)
	if d.Relationships != nil {
		blocked, err := d.Relationships.HasStatus(c.Request.Context(), inviterID, request.RelatedUserId, model.RelationshipBlocked)
		if err != nil {
			internalError(c, d.Log, err)
			return
		}
		if blocked {
			forbidden(c, "You cannot invite a user that blocked you.")
			return
		}
	}
	entity := d.loadRealm(c)
	if entity == nil {
		return
	}
	if request.Role > realm.RoleNormal && realm.BoostLevel(entity) < 2 {
		forbidden(c, "Realm boost level 2 is required to invite promoted members.")
		return
	}
	ok, err := d.Service.IsMemberWithRole(c.Request.Context(), entity.ID, inviterID, request.Role)
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	if !ok {
		forbidden(c, "You cannot invite member has higher permission than yours.")
		return
	}

	existing, err := d.Store.GetMember(c.Request.Context(), entity.ID, idOfAccount(related))
	if err != nil && !errors.Is(err, realm.ErrNotFound) {
		internalError(c, d.Log, err)
		return
	}
	if existing != nil {
		if existing.LeaveAt == nil {
			badRequest(c, "REALM_MEMBER_EXISTS", "This user already in the realm cannot be invited again.")
			return
		}
		existing.LeaveAt = nil
		existing.JoinedAt = nil
		existing.Role = request.Role
		if err := d.Store.SaveMember(c.Request.Context(), existing); err != nil {
			internalError(c, d.Log, err)
			return
		}
		d.logAction(c, account.Id, model.ActionLogRealmInvite, map[string]any{
			"realm_id": entity.ID.String(), "account_id": existing.AccountID.String(), "role": request.Role,
		})
		d.respondMember(c, entity, existing)
		return
	}

	member := &realm.RealmMemberEntity{
		RealmID: entity.ID, AccountID: idOfAccount(related), Role: request.Role,
	}
	if err := d.Store.CreateMember(c.Request.Context(), member); err != nil {
		internalError(c, d.Log, err)
		return
	}
	d.logAction(c, account.Id, model.ActionLogRealmInvite, map[string]any{
		"realm_id": entity.ID.String(), "account_id": member.AccountID.String(), "role": request.Role,
	})
	d.respondMember(c, entity, member)
}

// acceptInvite mirrors POST /invites/:slug/accept.
func (d Deps) acceptInvite(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	accountID, _ := accountIDOf(account)
	entity, err := d.Store.GetRealmBySlug(c.Request.Context(), c.Param("slug"))
	if errors.Is(err, realm.ErrNotFound) {
		realmNotFound(c)
		return
	}
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	member, err := d.Store.GetMember(c.Request.Context(), entity.ID, accountID)
	if errors.Is(err, realm.ErrNotFound) {
		memberNotFound(c)
		return
	}
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	if member.JoinedAt != nil {
		memberNotFound(c)
		return
	}
	member.JoinedAt = new(nowUTC())
	if err := d.Store.SaveMember(c.Request.Context(), member); err != nil {
		internalError(c, d.Log, err)
		return
	}
	d.logAction(c, account.Id, model.ActionLogRealmJoin, map[string]any{
		"realm_id": entity.ID.String(), "account_id": accountID.String(),
	})
	d.respondMember(c, entity, member)
}

// declineInvite mirrors POST /invites/:slug/decline.
func (d Deps) declineInvite(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	accountID, _ := accountIDOf(account)
	entity, err := d.Store.GetRealmBySlug(c.Request.Context(), c.Param("slug"))
	if errors.Is(err, realm.ErrNotFound) {
		realmNotFound(c)
		return
	}
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	member, err := d.Store.GetMember(c.Request.Context(), entity.ID, accountID)
	if errors.Is(err, realm.ErrNotFound) {
		memberNotFound(c)
		return
	}
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	if member.JoinedAt != nil {
		memberNotFound(c)
		return
	}
	member.LeaveAt = new(nowUTC())
	if err := d.Store.SaveMember(c.Request.Context(), member); err != nil {
		internalError(c, d.Log, err)
		return
	}
	d.logAction(c, account.Id, "realms.members.decline_invite", map[string]any{
		"realm_id": entity.ID.String(), "account_id": accountID.String(), "decliner_id": account.Id,
	})
	c.Status(http.StatusNoContent)
}

// joinRealm mirrors POST /:slug/members/me: community realms only.
func (d Deps) joinRealm(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	entity := d.loadRealm(c)
	if entity == nil {
		return
	}
	if !entity.IsCommunity {
		forbidden(c, "Only community realms can be joined without invitation.")
		return
	}
	accountID, _ := accountIDOf(account)
	existing, err := d.Store.GetMember(c.Request.Context(), entity.ID, accountID)
	if err != nil && !errors.Is(err, realm.ErrNotFound) {
		internalError(c, d.Log, err)
		return
	}
	if existing != nil {
		if existing.LeaveAt == nil {
			badRequest(c, "REALM_MEMBER_EXISTS", "You are already a member of this realm.")
			return
		}
		existing.LeaveAt = nil
		existing.JoinedAt = new(nowUTC())
		if err := d.Store.SaveMember(c.Request.Context(), existing); err != nil {
			internalError(c, d.Log, err)
			return
		}
		d.logAction(c, account.Id, model.ActionLogRealmJoin, map[string]any{
			"realm_id": entity.ID.String(), "account_id": account.Id, "is_community": entity.IsCommunity,
		})
		d.respondMember(c, entity, existing)
		return
	}
	member := &realm.RealmMemberEntity{
		RealmID: entity.ID, AccountID: accountID, Role: realm.RoleNormal, JoinedAt: new(nowUTC()),
	}
	if err := d.Store.CreateMember(c.Request.Context(), member); err != nil {
		internalError(c, d.Log, err)
		return
	}
	d.logAction(c, account.Id, model.ActionLogRealmJoin, map[string]any{
		"realm_id": entity.ID.String(), "account_id": account.Id, "is_community": entity.IsCommunity,
	})
	d.respondMember(c, entity, member)
}

// leaveRealm mirrors DELETE /:slug/members/me: the owner cannot leave.
func (d Deps) leaveRealm(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	entity := d.loadRealm(c)
	if entity == nil {
		return
	}
	accountID, _ := accountIDOf(account)
	member, err := d.Store.GetActiveMember(c.Request.Context(), entity.ID, accountID)
	if errors.Is(err, realm.ErrNotFound) {
		memberNotFound(c)
		return
	}
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	if member.Role == realm.RoleOwner {
		forbidden(c, "Owner cannot leave their own realm.")
		return
	}
	member.LeaveAt = new(nowUTC())
	if err := d.Store.SaveMember(c.Request.Context(), member); err != nil {
		internalError(c, d.Log, err)
		return
	}
	d.logAction(c, account.Id, model.ActionLogRealmLeave, map[string]any{
		"realm_id": entity.ID.String(), "account_id": accountID.String(), "leaver_id": account.Id,
	})
	c.Status(http.StatusNoContent)
}

// removeMember mirrors DELETE /:slug/members/:memberId: the actor must outrank
// (or match) the target.
func (d Deps) removeMember(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	entity := d.loadRealm(c)
	if entity == nil {
		return
	}
	memberID, err := uuid.Parse(c.Param("memberId"))
	if err != nil {
		memberNotFound(c)
		return
	}
	member, err := d.Store.GetActiveMember(c.Request.Context(), entity.ID, memberID)
	if errors.Is(err, realm.ErrNotFound) {
		memberNotFound(c)
		return
	}
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	actorID, _ := accountIDOf(account)
	ok, err := d.Service.IsMemberWithRole(c.Request.Context(), entity.ID, actorID, realm.RoleModerator, member.Role)
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	if !ok {
		forbidden(c, "You do not have permission to remove members from this realm.")
		return
	}
	member.LeaveAt = new(nowUTC())
	if err := d.Store.SaveMember(c.Request.Context(), member); err != nil {
		internalError(c, d.Log, err)
		return
	}
	d.logAction(c, account.Id, model.ActionLogRealmKick, map[string]any{
		"realm_id": entity.ID.String(), "account_id": memberID.String(), "kicker_id": account.Id,
	})
	c.Status(http.StatusNoContent)
}

// updateMemberRole mirrors PATCH /:slug/members/:memberId/role. The body is a
// bare integer (the C# action bound [FromBody] int newRole).
func (d Deps) updateMemberRole(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	var newRole int
	if !bindJSON(c, &newRole) {
		return
	}
	if newRole >= realm.RoleOwner {
		badRequest(c, "REALM_ROLE_INVALID", "Unable to set realm member to owner or greater role.")
		return
	}
	entity := d.loadRealm(c)
	if entity == nil {
		return
	}
	if newRole > realm.RoleNormal && realm.BoostLevel(entity) < 2 {
		forbidden(c, "Realm boost level 2 is required to promote members.")
		return
	}
	memberID, err := uuid.Parse(c.Param("memberId"))
	if err != nil {
		memberNotFound(c)
		return
	}
	member, err := d.Store.GetActiveMember(c.Request.Context(), entity.ID, memberID)
	if errors.Is(err, realm.ErrNotFound) {
		memberNotFound(c)
		return
	}
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	actorID, _ := accountIDOf(account)
	ok, err := d.Service.IsMemberWithRole(c.Request.Context(), entity.ID, actorID,
		realm.RoleModerator, member.Role, newRole)
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	if !ok {
		forbidden(c, "You do not have permission to update member roles in this realm.")
		return
	}
	member.Role = newRole
	if err := d.Store.SaveMember(c.Request.Context(), member); err != nil {
		internalError(c, d.Log, err)
		return
	}
	d.logAction(c, account.Id, model.ActionLogRealmAdjustRole, map[string]any{
		"realm_id": entity.ID.String(), "account_id": memberID.String(),
		"new_role": newRole, "updater_id": account.Id,
	})
	c.JSON(http.StatusOK, realm.MemberFromEntity(member))
}

// updateMemberLabel mirrors PATCH /:slug/members/:memberId/label.
func (d Deps) updateMemberLabel(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	entity := d.loadRealm(c)
	if entity == nil {
		return
	}
	actorID, _ := accountIDOf(account)
	ok, err := d.Service.IsMemberWithRole(c.Request.Context(), entity.ID, actorID, realm.RoleModerator)
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	if !ok {
		forbidden(c, "You do not have permission to manage labels.")
		return
	}
	if realm.BoostLevel(entity) < 1 {
		forbidden(c, "Realm boost level 1 is required to manage labels.")
		return
	}
	var request labelAssignmentRequest
	if !bindJSON(c, &request) {
		return
	}
	memberID, err := uuid.Parse(c.Param("memberId"))
	if err != nil {
		memberNotFound(c)
		return
	}
	member, err := d.Store.GetActiveMember(c.Request.Context(), entity.ID, memberID)
	if errors.Is(err, realm.ErrNotFound) {
		memberNotFound(c)
		return
	}
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	if request.LabelId != nil {
		label, err := d.Store.GetLabel(c.Request.Context(), entity.ID, *request.LabelId)
		if errors.Is(err, realm.ErrNotFound) {
			badRequest(c, "REALM_LABEL_NOT_BELONG", "Label does not belong to this realm.")
			return
		}
		if err != nil {
			internalError(c, d.Log, err)
			return
		}
		member.LabelID = &label.ID
	} else {
		member.LabelID = nil
	}
	if err := d.Store.SaveMember(c.Request.Context(), member); err != nil {
		internalError(c, d.Log, err)
		return
	}
	d.respondMember(c, entity, member)
}

// memberExperience mirrors GET /:slug/members/:memberId/experience.
func (d Deps) memberExperience(c *gin.Context) {
	entity := d.loadRealm(c)
	if entity == nil {
		return
	}
	if !entity.IsPublic {
		viewer := currentViewer(c)
		if viewer == nil {
			c.JSON(http.StatusUnauthorized, errs.Unauthorized("Authentication is required."))
			return
		}
		viewerID, _ := accountIDOf(viewer)
		ok, err := d.Service.IsMemberWithRole(c.Request.Context(), entity.ID, viewerID, realm.RoleNormal)
		if err != nil {
			internalError(c, d.Log, err)
			return
		}
		if !ok {
			forbidden(c, "You must be a member to view this realm.")
			return
		}
	}
	memberID, err := uuid.Parse(c.Param("memberId"))
	if err != nil {
		memberNotFound(c)
		return
	}
	records, err := d.Store.RecentExperience(c.Request.Context(), entity.ID, memberID, parseInt(c.DefaultQuery("take", "50"), 50))
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	out := make([]*realm.ExperienceRecord, 0, len(records))
	for i := range records {
		out = append(out, realm.ExperienceFromEntity(&records[i]))
	}
	c.JSON(http.StatusOK, out)
}

// ─────────────────────────── labels ───────────────────────────

// listLabels mirrors GET /:slug/labels.
func (d Deps) listLabels(c *gin.Context) {
	entity := d.loadRealm(c)
	if entity == nil {
		return
	}
	if !entity.IsPublic {
		viewer := currentViewer(c)
		if viewer == nil {
			c.JSON(http.StatusUnauthorized, errs.Unauthorized("Authentication is required."))
			return
		}
		viewerID, _ := accountIDOf(viewer)
		ok, err := d.Service.IsMemberWithRole(c.Request.Context(), entity.ID, viewerID, realm.RoleNormal)
		if err != nil {
			internalError(c, d.Log, err)
			return
		}
		if !ok {
			forbidden(c, "You must be a member to view this realm.")
			return
		}
	}
	labels, err := d.Store.ListLabels(c.Request.Context(), entity.ID)
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	out := make([]*realm.Label, 0, len(labels))
	for i := range labels {
		out = append(out, realm.LabelFromEntity(&labels[i]))
	}
	c.JSON(http.StatusOK, out)
}

// createLabel mirrors POST /:slug/labels: moderator, boost level 1, and the
// per-level label cap.
func (d Deps) createLabel(c *gin.Context) {
	entity, actor := d.labelGuard(c)
	if entity == nil || actor == nil {
		return
	}
	var request labelRequest
	if !bindJSON(c, &request) {
		return
	}
	if isBlank(request.Name) {
		validation(c, "name")
		return
	}
	if exceeded(derefString(request.Name), 1024) {
		validation(c, "name")
		return
	}
	cap := realm.LabelCap(realm.BoostLevel(entity))
	count, err := d.Store.CountLabels(c.Request.Context(), entity.ID)
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	if count >= int64(cap) {
		badRequest(c, "REALM_LABEL_LIMIT_REACHED", "Realm label limit reached for current boost level.")
		return
	}
	label := &realm.RealmLabelEntity{
		ID: uuid.New(), RealmID: entity.ID, Name: derefString(request.Name),
		Description: request.Description, Color: request.Color, Icon: request.Icon,
		CreatedByAccountID: actorID(actor),
	}
	if err := d.Store.CreateLabel(c.Request.Context(), label); err != nil {
		internalError(c, d.Log, err)
		return
	}
	c.JSON(http.StatusOK, realm.LabelFromEntity(label))
}

// updateLabel mirrors PATCH /:slug/labels/:labelId.
func (d Deps) updateLabel(c *gin.Context) {
	entity, _ := d.labelGuard(c)
	if entity == nil {
		return
	}
	labelID, err := uuid.Parse(c.Param("labelId"))
	if err != nil {
		labelNotFound(c)
		return
	}
	label, err := d.Store.GetLabel(c.Request.Context(), entity.ID, labelID)
	if errors.Is(err, realm.ErrNotFound) {
		labelNotFound(c)
		return
	}
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	var request labelRequest
	if !bindJSON(c, &request) {
		return
	}
	if isBlank(request.Name) {
		validation(c, "name")
		return
	}
	label.Name = derefString(request.Name)
	label.Description = request.Description
	label.Color = request.Color
	label.Icon = request.Icon
	if err := d.Store.SaveLabel(c.Request.Context(), label); err != nil {
		internalError(c, d.Log, err)
		return
	}
	c.JSON(http.StatusOK, realm.LabelFromEntity(label))
}

// deleteLabel mirrors DELETE /:slug/labels/:labelId.
func (d Deps) deleteLabel(c *gin.Context) {
	entity, _ := d.labelGuard(c)
	if entity == nil {
		return
	}
	labelID, err := uuid.Parse(c.Param("labelId"))
	if err != nil {
		labelNotFound(c)
		return
	}
	if _, err := d.Store.GetLabel(c.Request.Context(), entity.ID, labelID); errors.Is(err, realm.ErrNotFound) {
		labelNotFound(c)
		return
	} else if err != nil {
		internalError(c, d.Log, err)
		return
	}
	if err := d.Store.DeleteLabel(c.Request.Context(), entity.ID, labelID); err != nil {
		internalError(c, d.Log, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// labelGuard resolves the realm and enforces the label-management rules the
// three label write routes share: moderator role and boost level 1.
func (d Deps) labelGuard(c *gin.Context) (*realm.RealmEntity, *model.Account) {
	account := currentAccount(c)
	if account == nil {
		return nil, nil
	}
	entity := d.loadRealm(c)
	if entity == nil {
		return nil, nil
	}
	actorID, _ := accountIDOf(account)
	ok, err := d.Service.IsMemberWithRole(c.Request.Context(), entity.ID, actorID, realm.RoleModerator)
	if err != nil {
		internalError(c, d.Log, err)
		return nil, nil
	}
	if !ok {
		forbidden(c, "You do not have permission to manage labels.")
		return nil, nil
	}
	if realm.BoostLevel(entity) < 1 {
		forbidden(c, "Realm boost level 1 is required to manage labels.")
		return nil, nil
	}
	return entity, account
}

// ─────────────────────────── boosts ───────────────────────────

// boostRealm mirrors POST /:slug/boosts: validates the currency, opens the
// order for realms.boost, and lets the payment consumer apply it.
func (d Deps) boostRealm(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	entity := d.loadRealm(c)
	if entity == nil {
		return
	}
	accountID, _ := accountIDOf(account)
	ok, err := d.Service.IsMemberWithRole(c.Request.Context(), entity.ID, accountID, realm.RoleNormal)
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	if !ok {
		forbidden(c, "You must be a member to boost this realm.")
		return
	}
	var request boostRequest
	if !bindJSON(c, &request) {
		return
	}
	if request.Shares < 1 || request.Shares > 1000000 {
		validation(c, "shares")
		return
	}
	currency, err := realm.NormalizeCurrency(derefString(request.Currency))
	if err != nil {
		badRequest(c, "REALM_BAD_BOOST_CURRENCY", "Unsupported boost currency. Use golds or points.")
		return
	}
	amount, err := realm.AmountForShares(currency, request.Shares)
	if err != nil {
		badRequest(c, "REALM_BAD_BOOST_CURRENCY", "Unsupported boost currency. Use golds or points.")
		return
	}
	if d.Payments == nil {
		c.JSON(http.StatusServiceUnavailable, errs.New("SERVICE_UNAVAILABLE",
			"Payments are not configured; realm boosts are unavailable.", http.StatusServiceUnavailable))
		return
	}
	order, err := d.Payments.CreateOrder(c.Request.Context(), realm.OrderInput{
		AccountID: account.Id, Currency: currency, Amount: amount,
		ProductIdentifier: "realms.boost",
		AppIdentifier:     "internal",
		Remarks:           "Boost realm " + entity.Name,
		Meta: map[string]any{
			"realm_id":   entity.ID.String(),
			"account_id": account.Id,
			"shares":     request.Shares,
			"currency":   currency,
			"amount":     amount.String(),
		},
	})
	if err != nil {
		if errors.Is(err, realm.ErrUnavailable) {
			c.JSON(http.StatusServiceUnavailable, errs.New("SERVICE_UNAVAILABLE",
				"Payments are not configured; realm boosts are unavailable.", http.StatusServiceUnavailable))
			return
		}
		internalError(c, d.Log, err)
		return
	}
	orderID, err := uuid.Parse(order.ID)
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	c.JSON(http.StatusOK, realm.BoostResponse{
		OrderId: orderID, Shares: request.Shares, Currency: currency, Amount: amount,
	})
}

// boostStatus mirrors GET /:slug/boosts.
func (d Deps) boostStatus(c *gin.Context) {
	entity := d.loadRealm(c)
	if entity == nil {
		return
	}
	level := realm.BoostLevel(entity)
	c.JSON(http.StatusOK, realm.BoostStatus{
		BoostPoints: entity.BoostPoints, BoostLevel: level,
		LabelCap: realm.LabelCap(level), ExpiresAfterDays: realm.ExpirationDays,
		SupportedCurrencies: []string{realm.GoldsCurrency, realm.PointsCurrency},
		DefaultCurrency:     realm.DefaultCurrency,
	})
}

// boostLeaderboard mirrors GET /:slug/boosts/leaderboard.
func (d Deps) boostLeaderboard(c *gin.Context) {
	entity := d.loadRealm(c)
	if entity == nil {
		return
	}
	rows, err := d.Store.BoostLeaderboard(c.Request.Context(), entity.ID, realm.BoostCutoff(nowUTC()), parseInt(c.DefaultQuery("take", "20"), 20))
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	accounts, err := d.accountsByIDs(c, rows)
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	out := make([]realm.LeaderboardRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, realm.LeaderboardRow{
			AccountId: row.AccountID, Account: accounts[row.AccountID],
			AmountGolds: row.AmountGolds, AmountPoints: row.AmountPoints,
			Shares: row.Shares, Boosts: row.Boosts,
			LastBoostedAt: model.NewTime(row.LastBoostedAt),
		})
	}
	c.JSON(http.StatusOK, out)
}

// ─────────────────────────── permissions ───────────────────────────

// listRolePermissions mirrors GET /:slug/permissions/roles.
func (d Deps) listRolePermissions(c *gin.Context) {
	entity, actor := d.permissionGuard(c)
	if entity == nil {
		return
	}
	rows, err := d.Store.ListRolePermissions(c.Request.Context(), entity.ID)
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	_ = actor
	out := make([]*realm.RolePermission, 0, len(rows))
	for i := range rows {
		out = append(out, realm.RolePermissionFromEntity(&rows[i]))
	}
	c.JSON(http.StatusOK, out)
}

// updateRolePermissions mirrors POST /:slug/permissions/roles.
func (d Deps) updateRolePermissions(c *gin.Context) {
	entity, actor := d.permissionGuard(c)
	if entity == nil {
		return
	}
	var request rolePermissionRequest
	if !bindJSON(c, &request) {
		return
	}
	if request.RoleLevel < 0 || request.RoleLevel > realm.RoleOwner {
		validation(c, "role_level")
		return
	}
	saved, err := d.Store.SaveRolePermission(c.Request.Context(), &realm.RolePermissionEntity{
		ID: uuid.New(), RealmID: entity.ID, RoleLevel: request.RoleLevel,
		CanChat: request.CanChat, CanPost: request.CanPost, CanComment: request.CanComment,
		CanUploadMedia: request.CanUploadMedia, CanModeratePosts: request.CanModeratePosts,
		CanModerateChat: request.CanModerateChat, CanManageMembers: request.CanManageMembers,
		CanManageRealm: request.CanManageRealm,
	})
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	d.logAction(c, actor.Id, "realms.permissions.role.update", map[string]any{
		"realm_id": entity.ID.String(), "role_level": request.RoleLevel,
	})
	c.JSON(http.StatusOK, realm.RolePermissionFromEntity(saved))
}

// getUserPermissions mirrors GET /:slug/permissions/users/:accountId. A missing
// override returns null, as the C# FirstOrDefault did.
func (d Deps) getUserPermissions(c *gin.Context) {
	entity, _ := d.permissionGuard(c)
	if entity == nil {
		return
	}
	accountID, err := uuid.Parse(c.Param("accountId"))
	if err != nil {
		validation(c, "accountId")
		return
	}
	override, err := d.Store.GetUserPermission(c.Request.Context(), entity.ID, accountID)
	if errors.Is(err, realm.ErrNotFound) {
		c.JSON(http.StatusOK, nil)
		return
	}
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	c.JSON(http.StatusOK, realm.UserPermissionFromEntity(override))
}

// updateUserPermissions mirrors POST /:slug/permissions/users.
func (d Deps) updateUserPermissions(c *gin.Context) {
	entity, actor := d.permissionGuard(c)
	if entity == nil {
		return
	}
	var request userPermissionRequest
	if !bindJSON(c, &request) {
		return
	}
	if request.AccountId == uuid.Nil {
		validation(c, "account_id")
		return
	}
	saved, err := d.Store.GetUserPermission(c.Request.Context(), entity.ID, request.AccountId)
	if errors.Is(err, realm.ErrNotFound) {
		saved = &realm.UserPermissionEntity{ID: uuid.New(), RealmID: entity.ID, AccountID: request.AccountId}
	} else if err != nil {
		internalError(c, d.Log, err)
		return
	}
	saved.CanChat = request.CanChat
	saved.CanPost = request.CanPost
	saved.CanComment = request.CanComment
	saved.CanUploadMedia = request.CanUploadMedia
	saved.CanModeratePosts = request.CanModeratePosts
	saved.CanModerateChat = request.CanModerateChat
	saved.CanManageMembers = request.CanManageMembers
	saved.CanManageRealm = request.CanManageRealm
	if err := d.Store.SaveUserPermission(c.Request.Context(), saved); err != nil {
		internalError(c, d.Log, err)
		return
	}
	d.logAction(c, actor.Id, "realms.permissions.user.update", map[string]any{
		"realm_id": entity.ID.String(), "account_id": request.AccountId.String(),
	})
	c.JSON(http.StatusOK, realm.UserPermissionFromEntity(saved))
}

// permissionGuard resolves the realm and requires the realm.manage
// permission, which the per-user override, the role row or the defaults
// answer.
func (d Deps) permissionGuard(c *gin.Context) (*realm.RealmEntity, *model.Account) {
	account := currentAccount(c)
	if account == nil {
		return nil, nil
	}
	entity := d.loadRealm(c)
	if entity == nil {
		return nil, nil
	}
	actorID, _ := accountIDOf(account)
	ok, err := d.Service.HasPermission(c.Request.Context(), entity.ID, actorID, realm.PermRealmManage)
	if err != nil {
		internalError(c, d.Log, err)
		return nil, nil
	}
	if !ok {
		forbidden(c, "You do not have permission to manage realm permissions.")
		return nil, nil
	}
	return entity, account
}

// moderationLogs mirrors GET /:slug/posts/moderation-logs.
func (d Deps) moderationLogs(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	entity := d.loadRealm(c)
	if entity == nil {
		return
	}
	actorID, _ := accountIDOf(account)
	ok, err := d.Service.HasPermission(c.Request.Context(), entity.ID, actorID, realm.PermPostModerate)
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	if !ok {
		forbidden(c, "You do not have permission to view moderation logs.")
		return
	}
	rows, total, err := d.Store.ModerationLogs(c.Request.Context(), entity.ID,
		parseInt(c.DefaultQuery("offset", "0"), 0), parseInt(c.DefaultQuery("take", "20"), 20))
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	setTotal(c, total)
	out := make([]realm.ModerationLog, 0, len(rows))
	for i := range rows {
		out = append(out, realm.ModerationLogFromEntity(&rows[i]))
	}
	c.JSON(http.StatusOK, out)
}

// ─────────────────────────── shared helpers ───────────────────────────

// currentViewer returns the authenticated account, or nil for an anonymous
// request (the permissive routes use it for visibility checks).
func currentViewer(c *gin.Context) *model.Account {
	return middleware.CurrentUser(c.Request.Context())
}

func (d Deps) attachStatuses(c *gin.Context, members []*realm.Member, viewer *model.Account) {
	if d.Statuses == nil || len(members) == 0 {
		return
	}
	ids := make([]uuid.UUID, 0, len(members))
	for _, member := range members {
		ids = append(ids, member.AccountId)
	}
	var viewerID *uuid.UUID
	if viewer != nil {
		if id, ok := accountIDOf(viewer); ok {
			viewerID = &id
		}
	}
	statuses, err := d.Statuses.Statuses(c.Request.Context(), ids, viewerID)
	if err != nil {
		if d.Log != nil {
			d.Log.Warn("realm member statuses failed", "error", err)
		}
		return
	}
	for _, member := range members {
		member.Status = statuses[member.AccountId]
	}
}

func (d Deps) accountsByIDs(c *gin.Context, rows []realm.BoostLeaderboardEntry) (map[uuid.UUID]*model.Account, error) {
	out := make(map[uuid.UUID]*model.Account, len(rows))
	if len(rows) == 0 {
		return out, nil
	}
	ids := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.AccountID)
	}
	accounts, err := d.Accounts.GetAccountsByIDs(c.Request.Context(), ids)
	if err != nil {
		return nil, err
	}
	for i := range accounts {
		id, err := uuid.Parse(accounts[i].Id)
		if err != nil {
			continue
		}
		out[id] = &accounts[i]
	}
	return out, nil
}

// resolveArtwork resolves an optional file id into a jsonb reference, writing
// the C# error when the Drive lookup finds nothing.
func (d Deps) resolveArtwork(c *gin.Context, fileID *string, target **datatypesJSON, code, message string) bool {
	if fileID == nil || *fileID == "" {
		return true
	}
	if d.Files == nil {
		badRequest(c, code, message)
		return false
	}
	ref, err := d.Files.Resolve(c.Request.Context(), *fileID)
	if err != nil || ref == nil {
		badRequest(c, code, message)
		return false
	}
	raw, err := jsonMarshal(ref)
	if err != nil {
		internalError(c, d.Log, err)
		return false
	}
	value := datatypesJSON(raw)
	*target = &value
	return true
}

// realmProjection maps storage rows to the wire shape.
func realmProjection(entities []realm.RealmEntity) []*realm.Realm {
	out := make([]*realm.Realm, 0, len(entities))
	for i := range entities {
		out = append(out, realm.RealmFromEntity(&entities[i]))
	}
	return out
}

func realmIDsOf(rows []realm.RealmMemberEntity) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(rows))
	seen := make(map[uuid.UUID]struct{}, len(rows))
	for _, row := range rows {
		if _, ok := seen[row.RealmID]; ok {
			continue
		}
		seen[row.RealmID] = struct{}{}
		ids = append(ids, row.RealmID)
	}
	return ids
}

// sortMembersByPresence applies the C# withStatus ordering: online first, then
// level, then experience.
func sortMembersByPresence(members []*realm.Member) {
	sort.SliceStable(members, func(i, j int) bool {
		a, b := members[i], members[j]
		aOnline := a.Status != nil && a.Status.IsOnline
		bOnline := b.Status != nil && b.Status.IsOnline
		if aOnline != bOnline {
			return aOnline
		}
		if a.Level != b.Level {
			return a.Level > b.Level
		}
		return a.Experience > b.Experience
	})
}

// profileLevel reads the computed account level, 0 without a profile row.
func profileLevel(account *model.Account) int {
	if account == nil || account.Profile == nil {
		return 0
	}
	return account.Profile.Level
}

func actorID(account *model.Account) uuid.UUID {
	id, _ := uuid.Parse(account.Id)
	return id
}

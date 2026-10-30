package realmctl

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/datatypes"

	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/realm"
)

// adminListRealms mirrors GET /api/admin/realms.
func (d Deps) adminListRealms(c *gin.Context) {
	filter := realm.RealmFilter{
		Query:  c.Query("query"),
		Offset: parseInt(c.DefaultQuery("offset", "0"), 0),
		Take:   parseInt(c.DefaultQuery("take", "50"), 50),
	}
	if raw := c.Query("isPublic"); raw != "" {
		value := queryBool(c, "isPublic")
		filter.IsPublic = &value
	}
	if raw := c.Query("isCommunity"); raw != "" {
		value := queryBool(c, "isCommunity")
		filter.IsCommunity = &value
	}
	if raw := c.Query("verified"); raw != "" {
		value := queryBool(c, "verified")
		filter.Verified = &value
	}
	if raw := c.Query("accountId"); raw != "" {
		if id, err := uuid.Parse(raw); err == nil {
			filter.AccountID = &id
		}
	}
	entities, total, err := d.Store.ListRealms(c.Request.Context(), filter)
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

// adminGetRealm mirrors GET /api/admin/realms/:slug: the realm plus its four
// counters.
func (d Deps) adminGetRealm(c *gin.Context) {
	entity := d.loadRealm(c)
	if entity == nil {
		return
	}
	counts, err := d.Store.CountRealmDetail(c.Request.Context(), entity.ID, realm.BoostCutoff(nowUTC()))
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	c.JSON(http.StatusOK, realm.AdminRealmDetail{
		Realm: realm.RealmFromEntity(entity), MemberCount: counts.Members,
		PendingInviteCount: counts.PendingInvite, LabelCount: counts.Labels,
		ActiveBoostContributionCount: counts.ActiveBoosts,
	})
}

// adminUpdateRealm mirrors PATCH /api/admin/realms/:slug. Unlike the member
// route, an empty slug/name is a validation error and ownership may be
// transferred.
func (d Deps) adminUpdateRealm(c *gin.Context) {
	var request adminRealmRequest
	if !bindJSON(c, &request) {
		return
	}
	entity, err := d.Store.GetRealmBySlug(c.Request.Context(), c.Param("slug"))
	if errors.Is(err, realm.ErrNotFound) {
		realmNotFound(c)
		return
	}
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	if request.Slug != nil {
		normalized := trimSpace(*request.Slug)
		if normalized == "" {
			badRequest(c, "PASSPORT_REALM_SLUG_REQUIRED", "Slug cannot be empty.")
			return
		}
		if !equalFold(normalized, entity.Slug) {
			taken, err := d.Store.RealmSlugTaken(c.Request.Context(), normalized, &entity.ID)
			if err != nil {
				internalError(c, d.Log, err)
				return
			}
			if taken {
				badRequest(c, "PASSPORT_REALM_SLUG_EXISTS", "A realm with this slug already exists.")
				return
			}
			entity.Slug = normalized
		}
	}
	if request.Name != nil {
		trimmed := trimSpace(*request.Name)
		if trimmed == "" {
			badRequest(c, "PASSPORT_REALM_NAME_REQUIRED", "Name cannot be empty.")
			return
		}
		entity.Name = trimmed
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
	if request.AccountId != nil {
		entity.AccountID = *request.AccountId
	}
	if err := d.Store.SaveRealm(c.Request.Context(), entity); err != nil {
		internalError(c, d.Log, err)
		return
	}
	if _, err := d.Service.RefreshBoost(c.Request.Context(), entity); err != nil {
		internalError(c, d.Log, err)
		return
	}
	actor := currentViewer(c)
	d.logAction(c, actor.Id, model.ActionLogRealmUpdate, map[string]any{
		"realm_id": entity.ID.String(), "operation": "admin_update", "slug": entity.Slug,
	})
	c.JSON(http.StatusOK, realm.RealmFromEntity(entity))
}

// adminSetVerification mirrors POST /api/admin/realms/:slug/verification.
func (d Deps) adminSetVerification(c *gin.Context) {
	var request verificationRequest
	if !bindJSON(c, &request) {
		return
	}
	entity, err := d.Store.GetRealmBySlug(c.Request.Context(), c.Param("slug"))
	if errors.Is(err, realm.ErrNotFound) {
		realmNotFound(c)
		return
	}
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	if request.Title != nil && exceeded(*request.Title, 1024) {
		validation(c, "title")
		return
	}
	if request.Description != nil && exceeded(*request.Description, 8192) {
		validation(c, "description")
		return
	}
	mark := model.SnVerificationMark{
		Type: request.Type, Title: request.Title,
		Description: request.Description, VerifiedBy: request.VerifiedBy,
	}
	raw, err := jsonMarshal(mark)
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	value := datatypes.JSON(raw)
	entity.Verification = &value
	if err := d.Store.SaveRealm(c.Request.Context(), entity); err != nil {
		internalError(c, d.Log, err)
		return
	}
	actor := currentViewer(c)
	d.logAction(c, actor.Id, model.ActionLogRealmModerate, map[string]any{
		"realm_id": entity.ID.String(), "operation": "set_verification",
		"verification_type": request.Type,
	})
	c.JSON(http.StatusOK, realm.RealmFromEntity(entity))
}

// adminClearVerification mirrors DELETE /api/admin/realms/:slug/verification.
func (d Deps) adminClearVerification(c *gin.Context) {
	entity, err := d.Store.GetRealmBySlug(c.Request.Context(), c.Param("slug"))
	if errors.Is(err, realm.ErrNotFound) {
		realmNotFound(c)
		return
	}
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	entity.Verification = nil
	if err := d.Store.SaveRealm(c.Request.Context(), entity); err != nil {
		internalError(c, d.Log, err)
		return
	}
	actor := currentViewer(c)
	d.logAction(c, actor.Id, model.ActionLogRealmModerate, map[string]any{
		"realm_id": entity.ID.String(), "operation": "clear_verification",
	})
	c.JSON(http.StatusOK, realm.RealmFromEntity(entity))
}

// adminListMembers mirrors GET /api/admin/realms/:slug/members.
func (d Deps) adminListMembers(c *gin.Context) {
	entity, err := d.Store.GetRealmBySlug(c.Request.Context(), c.Param("slug"))
	if errors.Is(err, realm.ErrNotFound) {
		realmNotFound(c)
		return
	}
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	filter := realm.MemberFilter{
		RealmID: entity.ID, PendingOnly: queryBool(c, "pendingOnly"),
		Offset: parseInt(c.DefaultQuery("offset", "0"), 0),
		Take:   parseInt(c.DefaultQuery("take", "50"), 50),
	}
	if !filter.PendingOnly {
		filter.JoinedOnly = true
	}
	if raw := c.Query("role"); raw != "" {
		role := parseInt(raw, -1)
		if role >= 0 {
			filter.Role = &role
		}
	}
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
}

// adminUpdateMemberRole mirrors PATCH /api/admin/realms/:slug/members/:memberId/role.
// The console may promote freely, including transfer-level ownership.
func (d Deps) adminUpdateMemberRole(c *gin.Context) {
	var request memberRoleRequest
	if !bindJSON(c, &request) {
		return
	}
	entity, err := d.Store.GetRealmBySlug(c.Request.Context(), c.Param("slug"))
	if errors.Is(err, realm.ErrNotFound) {
		realmNotFound(c)
		return
	}
	if err != nil {
		internalError(c, d.Log, err)
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
	member.Role = request.Role
	if err := d.Store.SaveMember(c.Request.Context(), member); err != nil {
		internalError(c, d.Log, err)
		return
	}
	actor := currentViewer(c)
	d.logAction(c, actor.Id, model.ActionLogRealmModerate, map[string]any{
		"realm_id": entity.ID.String(), "operation": "set_member_role",
		"account_id": memberID.String(), "role": request.Role,
	})
	d.respondMember(c, entity, member)
}

// adminRemoveMember mirrors DELETE /api/admin/realms/:slug/members/:memberId.
func (d Deps) adminRemoveMember(c *gin.Context) {
	entity, err := d.Store.GetRealmBySlug(c.Request.Context(), c.Param("slug"))
	if errors.Is(err, realm.ErrNotFound) {
		realmNotFound(c)
		return
	}
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	memberID, err := uuid.Parse(c.Param("memberId"))
	if err != nil {
		memberNotFound(c)
		return
	}
	member, err := d.Store.GetMember(c.Request.Context(), entity.ID, memberID)
	if errors.Is(err, realm.ErrNotFound) {
		memberNotFound(c)
		return
	}
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	member.LeaveAt = new(nowUTC())
	if err := d.Store.SaveMember(c.Request.Context(), member); err != nil {
		internalError(c, d.Log, err)
		return
	}
	actor := currentViewer(c)
	d.logAction(c, actor.Id, model.ActionLogRealmKick, map[string]any{
		"realm_id": entity.ID.String(), "operation": "admin_kick", "account_id": memberID.String(),
	})
	c.Status(http.StatusNoContent)
}

// adminDeleteRealm mirrors DELETE /api/admin/realms/:slug.
func (d Deps) adminDeleteRealm(c *gin.Context) {
	entity, err := d.Store.GetRealmBySlug(c.Request.Context(), c.Param("slug"))
	if errors.Is(err, realm.ErrNotFound) {
		realmNotFound(c)
		return
	}
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	if err := d.Store.DeleteRealm(c.Request.Context(), entity.ID, nowUTC()); err != nil {
		internalError(c, d.Log, err)
		return
	}
	actor := currentViewer(c)
	d.logAction(c, actor.Id, model.ActionLogRealmDelete, map[string]any{
		"realm_id": entity.ID.String(), "operation": "admin_delete",
		"realm_name": entity.Name, "realm_slug": entity.Slug,
	})
	c.Status(http.StatusNoContent)
}

// ─────────────────────────── admin request bodies ───────────────────────────

type adminRealmRequest struct {
	Slug        *string    `json:"slug"`
	Name        *string    `json:"name"`
	Description *string    `json:"description"`
	IsCommunity *bool      `json:"is_community"`
	IsPublic    *bool      `json:"is_public"`
	AccountId   *uuid.UUID `json:"account_id"`
}

type verificationRequest struct {
	Type        int     `json:"type"`
	Title       *string `json:"title"`
	Description *string `json:"description"`
	VerifiedBy  *string `json:"verified_by"`
}

type memberRoleRequest struct {
	Role int `json:"role"`
}

package profilectl

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/go/pkg/errs"
	"src.solsynth.dev/sosys/stargate/internal/middleware"
	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/permission"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// Relationship routes from Passport's RelationshipController, backed by the
// RelationshipService semantics (bidirectional rows, soft delete, timed
// block/mute with degrade, 1h Redis caches under the C# key names).

const (
	relCacheFriends = "accounts:friends:"
	relCacheBlocked = "accounts:blocked:"
	relCacheMuted   = "accounts:muted:"
	relCacheClose   = "accounts:close_friends:"
	relCacheTTL     = time.Hour

	maxCloseFriends  = 200
	friendRequestTTL = 7 * 24 * time.Hour
)

func registerRelationships(api *gin.RouterGroup, d Deps) {
	r := api.Group("/relationships")
	r.Use(middleware.RequireAuth())
	r.GET("", d.listRelationships)
	r.GET("/requests", d.listRelationshipRequests)
	r.GET("/close-friends", d.listCloseFriends)
	r.GET("/inspect/:accountId", middleware.AskPermission(d.Perm, permission.RelationshipsInspect), d.inspectRelationship)
	r.POST("/sync", middleware.AskPermission(d.Perm, permission.RelationshipsSync), d.syncRelationships)
	r.GET("/:accountId", d.getRelationship)
	r.POST("/:accountId", middleware.AskPermission(d.Perm, permission.RelationshipsCreate), d.createRelationship)
	r.PATCH("/:accountId", middleware.AskPermission(d.Perm, permission.RelationshipsUpdate), d.updateRelationship)
	r.DELETE("/:accountId", middleware.AskPermission(d.Perm, permission.RelationshipsDelete), d.deleteRelationship)
	r.POST("/:accountId/friends", middleware.AskPermission(d.Perm, permission.RelationshipsFriendsManage), d.sendFriendRequest)
	r.DELETE("/:accountId/friends", middleware.AskPermission(d.Perm, permission.RelationshipsFriendsManage), d.deleteFriendRequest)
	r.POST("/:accountId/friends/accept", middleware.AskPermission(d.Perm, permission.RelationshipsFriendsManage), d.acceptFriendRequest)
	r.POST("/:accountId/friends/decline", middleware.AskPermission(d.Perm, permission.RelationshipsFriendsManage), d.declineFriendRequest)
	r.POST("/:accountId/block", middleware.AskPermission(d.Perm, permission.RelationshipsBlockManage), d.blockUser)
	r.DELETE("/:accountId/block", middleware.AskPermission(d.Perm, permission.RelationshipsBlockManage), d.unblockUser)
	r.POST("/:accountId/mute", middleware.AskPermission(d.Perm, permission.RelationshipsMuteManage), d.muteUser)
	r.DELETE("/:accountId/mute", middleware.AskPermission(d.Perm, permission.RelationshipsMuteManage), d.unmuteUser)
	r.POST("/:accountId/close-friend", middleware.AskPermission(d.Perm, permission.RelationshipsCloseFriendsManage), d.addCloseFriend)
	r.DELETE("/:accountId/close-friend", middleware.AskPermission(d.Perm, permission.RelationshipsCloseFriendsManage), d.removeCloseFriend)
	r.PATCH("/:accountId/alias", middleware.AskPermission(d.Perm, permission.RelationshipsAliasManage), d.updateAlias)
	r.GET("/:accountId/mutual-friends", d.getMutualFriends)
}

// ─────────────────────────── DTOs ───────────────────────────

type relationshipRequest struct {
	Status *model.RelationshipStatus `json:"status"`
}

type relationshipActionRequest struct {
	ExpiresIn *string                   `json:"expires_in"`
	DegradeTo *model.RelationshipStatus `json:"degrade_to"`
}

type aliasRequest struct {
	Alias *string `json:"alias"`
}

type syncRequest struct {
	LastSyncTimestamp int64 `json:"last_sync_timestamp"`
}

type relationshipSyncResponse struct {
	Added           []model.Relationship `json:"added"`
	Updated         []model.Relationship `json:"updated"`
	Removed         []string             `json:"removed"`
	ServerTimestamp *model.Time          `json:"server_timestamp"`
}

type inspectRelationshipResponse struct {
	Friends      []model.Account `json:"friends"`
	Blocked      []model.Account `json:"blocked"`
	Muted        []model.Account `json:"muted"`
	Pending      []model.Account `json:"pending"`
	CloseFriends []model.Account `json:"close_friends"`
}

// ─────────────────────────── helpers ───────────────────────────

// relErr mirrors the C# controller error mapping: an ApiError-shaped error
// carrying the exact code/message/status from the ported controller.
type relErr struct {
	code    string
	message string
	status  int
}

func (e *relErr) Error() string { return e.message }

func relErrNew(code, message string, status int) error {
	return &relErr{code: code, message: message, status: status}
}

func writeRelError(c *gin.Context, err error) {
	var re *relErr
	if errors.As(err, &re) {
		c.JSON(re.status, errs.New(re.code, re.message, re.status))
		return
	}
	internalError(c, err)
}

// requireTarget parses the :accountId route param, writing a 404 for invalid
// GUIDs (mirroring the {accountId:guid} route constraint).
func requireTarget(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("accountId"))
	if err != nil {
		c.JSON(http.StatusNotFound, notFound(c.Param("accountId")))
		return uuid.Nil, false
	}
	return id, true
}

func relStatusPtr(s model.RelationshipStatus) *model.RelationshipStatus { return &s }

// relationshipStatusName mirrors C# enum ToString() (PascalCase).
func relationshipStatusName(s model.RelationshipStatus) string {
	switch s {
	case model.RelationshipPending:
		return "Pending"
	case model.RelationshipFriends:
		return "Friends"
	case model.RelationshipMuted:
		return "Muted"
	case model.RelationshipBlocked:
		return "Blocked"
	case model.RelationshipCloseFriend:
		return "CloseFriend"
	}
	return fmt.Sprintf("%d", s)
}

func relationshipStatusNameLower(s model.RelationshipStatus) string {
	return strings.ToLower(relationshipStatusName(s))
}

// parseExpiresIn mirrors ParseExpiresIn: "30m" | "1h" | "24h" | "7d" | "30d".
func parseExpiresIn(expiresIn string) (time.Duration, error) {
	trimmed := strings.ToLower(strings.TrimSpace(expiresIn))
	var amount int
	var unit string
	switch {
	case strings.HasSuffix(trimmed, "d"):
		unit = "d"
	case strings.HasSuffix(trimmed, "h"):
		unit = "h"
	case strings.HasSuffix(trimmed, "m"):
		unit = "m"
	}
	if unit != "" {
		if _, err := fmt.Sscanf(trimmed[:len(trimmed)-1], "%d", &amount); err == nil {
			switch unit {
			case "d":
				return time.Duration(amount) * 24 * time.Hour, nil
			case "h":
				return time.Duration(amount) * time.Hour, nil
			case "m":
				return time.Duration(amount) * time.Minute, nil
			}
		}
	}
	return 0, fmt.Errorf("Invalid ExpiresIn format: '%s'. Use '1h', '24h', '7d', '30d', etc.", expiresIn)
}

// hydrateRelationships attaches Account/Related to each row (the C# uses
// AccountService.GetAccount which also attaches the profile; a dangling
// target attaches nil and logs instead of throwing).
func (d Deps) hydrateRelationships(ctx context.Context, rels []model.Relationship) {
	for i := range rels {
		if acc, err := d.Store.GetAccountWithProfile(ctx, mustUUID(rels[i].AccountId)); err == nil {
			rels[i].Account = acc
		}
		if acc, err := d.Store.GetAccountWithProfile(ctx, mustUUID(rels[i].RelatedId)); err == nil {
			rels[i].Related = acc
		}
	}
}

func mustUUID(s string) uuid.UUID {
	id, _ := uuid.Parse(s)
	return id
}

// ─────────────────────── Redis caches (C# key names) ───────────────────────

// cachedRelatedIDs mirrors GetCachedRelationships: read the 1h cache entry
// (dyson:-envelope via the shared cache service, so sibling services see the
// same keys), else query and populate.
func (d Deps) cachedRelatedIDs(ctx context.Context, accountID uuid.UUID, status model.RelationshipStatus, isRelated bool) ([]string, error) {
	var prefix string
	switch status {
	case model.RelationshipBlocked:
		prefix = relCacheBlocked
	case model.RelationshipMuted:
		prefix = relCacheMuted
	case model.RelationshipCloseFriend:
		prefix = relCacheClose
	default:
		prefix = relCacheFriends
	}
	suffix := "False"
	if isRelated {
		suffix = "True"
	}
	key := prefix + accountID.String() + ":" + suffix

	if d.Redis != nil && d.Redis.Cache != nil {
		var cached []string
		if found, err := d.Redis.Cache.Get(ctx, key, &cached); err == nil && found {
			return cached, nil
		}
	}
	ids, err := d.Store.ListRelatedAccountIDs(ctx, accountID, status, isRelated)
	if err != nil {
		return nil, err
	}
	if d.Redis != nil && d.Redis.Cache != nil {
		if err := d.Redis.Cache.Set(ctx, key, ids, relCacheTTL); err != nil {
			d.Log.Warn("set relationship cache", "key", key, "error", err)
		}
	}
	return ids, nil
}

// purgeRelationshipCache mirrors PurgeRelationshipCache (superset: the C#
// purges keys without the :False/:True suffix for friends/blocked, we purge
// every variant so sibling reads never go stale).
func (d Deps) purgeRelationshipCache(ctx context.Context, accountID, relatedID uuid.UUID, statuses ...model.RelationshipStatus) {
	if d.Redis == nil || d.Redis.Cache == nil {
		return
	}
	has := func(s model.RelationshipStatus) bool {
		for _, st := range statuses {
			if st == s {
				return true
			}
		}
		return false
	}
	a, b := accountID.String(), relatedID.String()
	keys := map[string]bool{}
	add := func(k string) { keys[k] = true }
	for _, id := range []string{a, b} {
		if has(model.RelationshipFriends) || has(model.RelationshipPending) {
			add(relCacheFriends + id)
			add(relCacheFriends + id + ":False")
			add(relCacheFriends + id + ":True")
		}
		if has(model.RelationshipBlocked) {
			add(relCacheBlocked + id)
			add(relCacheBlocked + id + ":False")
			add(relCacheBlocked + id + ":True")
			add(relCacheBlocked + "all:" + id)
		}
		if has(model.RelationshipMuted) {
			add(relCacheMuted + id + ":False")
		}
		if has(model.RelationshipCloseFriend) {
			add(relCacheClose + id + ":False")
		}
	}
	if has(model.RelationshipBlocked) {
		smaller, larger := a, b
		if a > b {
			smaller, larger = b, a
		}
		add(relCacheBlocked + "either:" + smaller + ":" + larger)
	}
	for key := range keys {
		if err := d.Redis.Cache.Remove(ctx, key); err != nil {
			d.Log.Warn("purge relationship cache", "key", key, "error", err)
		}
	}
}

// ─────────────────────── list / get / inspect / sync ───────────────────────

func (d Deps) listRelationships(c *gin.Context) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	offset, take := parsePagination(c)
	ctx := c.Request.Context()
	rels, total, err := d.Store.ListRelationshipsPage(ctx, accountIDOf(user), offset, take)
	if err != nil {
		internalError(c, err)
		return
	}
	d.hydrateRelationships(ctx, rels)
	c.Header("X-Total", fmt.Sprintf("%d", total))
	if rels == nil {
		rels = []model.Relationship{}
	}
	c.JSON(http.StatusOK, rels)
}

func (d Deps) listRelationshipRequests(c *gin.Context) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	ctx := c.Request.Context()
	rels, err := d.Store.ListRelationshipRequests(ctx, accountIDOf(user))
	if err != nil {
		internalError(c, err)
		return
	}
	d.hydrateRelationships(ctx, rels)
	if rels == nil {
		rels = []model.Relationship{}
	}
	c.JSON(http.StatusOK, rels)
}

func (d Deps) getRelationship(c *gin.Context) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	targetID, ok := requireTarget(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	rel, err := d.Store.GetRelationship(ctx, accountIDOf(user), targetID, nil, false, false)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			c.JSON(http.StatusNotFound, errs.New("PASSPORT_RELATIONSHIP_NOT_FOUND", "Relationship not found.", http.StatusNotFound))
			return
		}
		internalError(c, err)
		return
	}
	d.hydrateRelationships(ctx, []model.Relationship{*rel})
	c.JSON(http.StatusOK, rel)
}

func (d Deps) inspectRelationship(c *gin.Context) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	targetID, ok := requireTarget(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	rels, err := d.Store.ListOutgoingRelationships(ctx, targetID)
	if err != nil {
		internalError(c, err)
		return
	}
	d.hydrateRelationships(ctx, rels)
	response := &inspectRelationshipResponse{
		Friends:      []model.Account{},
		Blocked:      []model.Account{},
		Muted:        []model.Account{},
		Pending:      []model.Account{},
		CloseFriends: []model.Account{},
	}
	for _, rel := range rels {
		if rel.Related == nil {
			continue
		}
		switch rel.Status {
		case model.RelationshipFriends:
			response.Friends = append(response.Friends, *rel.Related)
		case model.RelationshipBlocked:
			response.Blocked = append(response.Blocked, *rel.Related)
		case model.RelationshipMuted:
			response.Muted = append(response.Muted, *rel.Related)
		case model.RelationshipPending:
			response.Pending = append(response.Pending, *rel.Related)
		case model.RelationshipCloseFriend:
			response.CloseFriends = append(response.CloseFriends, *rel.Related)
		}
	}
	c.JSON(http.StatusOK, response)
}

func (d Deps) syncRelationships(c *gin.Context) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	var req syncRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{"body": {"Invalid request body."}}))
		return
	}
	ctx := c.Request.Context()
	since := time.UnixMilli(req.LastSyncTimestamp)
	delta, err := d.Store.GetRelationshipDelta(ctx, accountIDOf(user), since)
	if err != nil {
		internalError(c, err)
		return
	}
	d.hydrateRelationships(ctx, delta.Added)
	d.hydrateRelationships(ctx, delta.Updated)
	if delta.Added == nil {
		delta.Added = []model.Relationship{}
	}
	if delta.Updated == nil {
		delta.Updated = []model.Relationship{}
	}
	if delta.Removed == nil {
		delta.Removed = []string{}
	}
	c.JSON(http.StatusOK, relationshipSyncResponse{
		Added:           delta.Added,
		Updated:         delta.Updated,
		Removed:         delta.Removed,
		ServerTimestamp: model.NewTime(delta.ServerTimestamp),
	})
}

// ─────────────────── create / update / delete ───────────────────

func (d Deps) createRelationship(c *gin.Context) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	targetID, ok := requireTarget(c)
	if !ok {
		return
	}
	var req relationshipRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{"body": {"Invalid request body."}}))
		return
	}
	if req.Status == nil {
		c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{"status": {"The Status field is required."}}))
		return
	}
	ctx := c.Request.Context()
	if !d.targetExists(ctx, c, targetID) {
		return
	}
	rel, err := d.opCreateRelationship(ctx, accountIDOf(user), targetID, *req.Status)
	if err != nil {
		writeRelError(c, err)
		return
	}
	d.logAction(c, user.Id, "relationships.create", map[string]any{
		"related_account_id": targetID.String(),
		"status":             relationshipStatusName(*req.Status),
	})
	c.JSON(http.StatusOK, rel)
}

func (d Deps) updateRelationship(c *gin.Context) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	targetID, ok := requireTarget(c)
	if !ok {
		return
	}
	var req relationshipRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{"body": {"Invalid request body."}}))
		return
	}
	if req.Status == nil {
		c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{"status": {"The Status field is required."}}))
		return
	}
	ctx := c.Request.Context()
	rel, err := d.opUpdateRelationship(ctx, accountIDOf(user), targetID, *req.Status)
	if err != nil {
		writeRelError(c, err)
		return
	}
	d.hydrateRelationships(ctx, []model.Relationship{*rel})
	d.logAction(c, user.Id, "relationships.update", map[string]any{
		"related_account_id": targetID.String(),
		"new_status":         relationshipStatusName(*req.Status),
	})
	c.JSON(http.StatusOK, rel)
}

func (d Deps) deleteRelationship(c *gin.Context) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	targetID, ok := requireTarget(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	rel, err := d.opDeleteRelationship(ctx, accountIDOf(user), targetID)
	if err != nil {
		writeRelError(c, err)
		return
	}
	d.hydrateRelationships(ctx, []model.Relationship{*rel})
	d.logAction(c, user.Id, "relationships.delete", map[string]any{
		"related_account_id": targetID.String(),
	})
	c.JSON(http.StatusOK, rel)
}

// opCreateRelationship mirrors RelationshipService.CreateRelationship.
func (d Deps) opCreateRelationship(ctx context.Context, senderID, targetID uuid.UUID, status model.RelationshipStatus) (*model.Relationship, error) {
	if status == model.RelationshipPending {
		return nil, relErrNew("PASSPORT_RELATIONSHIP_CREATE_FAILED",
			"Cannot create relationship with pending status, use SendFriendRequest instead.", http.StatusBadRequest)
	}
	exists, err := d.Store.HasExistingRelationship(ctx, senderID, targetID)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, relErrNew("PASSPORT_RELATIONSHIP_CREATE_FAILED",
			"Found existing relationship between you and target user.", http.StatusBadRequest)
	}
	rel, err := d.Store.GetRelationship(ctx, senderID, targetID, nil, true, true)
	if err == nil {
		restoreRelationship(rel, status, nil, nil)
		if err := d.Store.SaveRelationship(ctx, rel); err != nil {
			return nil, err
		}
	} else if errors.Is(err, store.ErrNotFound) {
		rel = &model.Relationship{AccountId: senderID.String(), RelatedId: targetID.String(), Status: status}
		if err := d.Store.InsertRelationship(ctx, rel); err != nil {
			return nil, err
		}
	} else {
		return nil, err
	}
	d.purgeRelationshipCache(ctx, senderID, targetID, status)
	return rel, nil
}

// opUpdateRelationship mirrors RelationshipService.UpdateRelationship.
func (d Deps) opUpdateRelationship(ctx context.Context, accountID, relatedID uuid.UUID, status model.RelationshipStatus) (*model.Relationship, error) {
	rel, err := d.Store.GetRelationship(ctx, accountID, relatedID, nil, false, false)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, relErrNew("PASSPORT_RELATIONSHIP_NOT_FOUND",
				"There is no relationship between you and the user.", http.StatusNotFound)
		}
		return nil, err
	}
	if rel.Status == status {
		return rel, nil
	}
	oldStatus := rel.Status
	rel.Status = status
	if err := d.Store.SaveRelationship(ctx, rel); err != nil {
		return nil, err
	}
	d.purgeRelationshipCache(ctx, accountID, relatedID, oldStatus, status)
	return rel, nil
}

// opDeleteRelationship mirrors RelationshipService.DeleteRelationship (EF
// Remove = soft delete).
func (d Deps) opDeleteRelationship(ctx context.Context, accountID, relatedID uuid.UUID) (*model.Relationship, error) {
	rel, err := d.Store.GetRelationship(ctx, accountID, relatedID, nil, false, false)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, relErrNew("PASSPORT_RELATIONSHIP_DELETE_FAILED",
				"There is no relationship between you and the user.", http.StatusBadRequest)
		}
		return nil, err
	}
	status := rel.Status
	rel.DeletedAt = model.NewTime(time.Now())
	if err := d.Store.SaveRelationship(ctx, rel); err != nil {
		return nil, err
	}
	d.purgeRelationshipCache(ctx, accountID, relatedID, status)
	return rel, nil
}

// targetExists writes the 404 the C# returns when the related account is
// missing.
func (d Deps) targetExists(ctx context.Context, c *gin.Context, targetID uuid.UUID) bool {
	exists, err := d.Store.AccountExists(ctx, targetID.String())
	if err != nil {
		internalError(c, err)
		return false
	}
	if !exists {
		c.JSON(http.StatusNotFound, errs.New("PASSPORT_RELATED_ACCOUNT_NOT_FOUND", "Account was not found.", http.StatusNotFound))
		return false
	}
	return true
}

// ─────────────────────── friends ───────────────────────

func (d Deps) sendFriendRequest(c *gin.Context) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	targetID, ok := requireTarget(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if !d.targetExists(ctx, c, targetID) {
		return
	}
	exists, err := d.Store.HasExistingRelationship(ctx, accountIDOf(user), targetID)
	if err != nil {
		internalError(c, err)
		return
	}
	if exists {
		c.JSON(http.StatusBadRequest, errs.New("PASSPORT_RELATIONSHIP_ALREADY_EXISTS", "Relationship already exists.", http.StatusBadRequest))
		return
	}
	rel, err := d.opSendFriendRequest(ctx, user, targetID)
	if err != nil {
		writeRelError(c, err)
		return
	}
	c.JSON(http.StatusOK, rel)
}

func (d Deps) deleteFriendRequest(c *gin.Context) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	targetID, ok := requireTarget(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	affected, err := d.Store.HardDeleteRelationship(ctx, accountIDOf(user), targetID, relStatusPtr(model.RelationshipPending))
	if err != nil {
		internalError(c, err)
		return
	}
	if affected == 0 {
		c.JSON(http.StatusNotFound, errs.New("PASSPORT_FRIEND_REQUEST_NOT_FOUND", "Friend request was not found.", http.StatusNotFound))
		return
	}
	d.purgeRelationshipCache(ctx, accountIDOf(user), targetID, model.RelationshipPending)
	c.Status(http.StatusNoContent)
}

func (d Deps) acceptFriendRequest(c *gin.Context) {
	d.respondFriendDecision(c, model.RelationshipFriends)
}

func (d Deps) declineFriendRequest(c *gin.Context) {
	d.respondFriendDecision(c, model.RelationshipBlocked)
}

// respondFriendDecision ports AcceptFriendRequest / DeclineFriendRequest.
func (d Deps) respondFriendDecision(c *gin.Context, status model.RelationshipStatus) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	targetID, ok := requireTarget(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	failCode := "PASSPORT_FRIEND_ACCEPT_FAILED"
	if status == model.RelationshipBlocked {
		failCode = "PASSPORT_FRIEND_DECLINE_FAILED"
	}
	rel, err := d.Store.GetRelationship(ctx, targetID, accountIDOf(user), relStatusPtr(model.RelationshipPending), false, false)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			c.JSON(http.StatusNotFound, errs.New("PASSPORT_FRIEND_REQUEST_NOT_FOUND", "Friend request was not found.", http.StatusNotFound))
			return
		}
		internalError(c, err)
		return
	}
	result, err := d.opAcceptFriendRelationship(ctx, rel, status)
	if err != nil {
		var re *relErr
		if errors.As(err, &re) && re.status == http.StatusBadRequest {
			c.JSON(http.StatusBadRequest, errs.New(failCode, re.message, http.StatusBadRequest))
			return
		}
		writeRelError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

// opSendFriendRequest mirrors RelationshipService.SendFriendRequest (plus
// the Ring push notification).
func (d Deps) opSendFriendRequest(ctx context.Context, sender *model.Account, targetID uuid.UUID) (*model.Relationship, error) {
	senderID := accountIDOf(sender)
	expiredAt := model.NewTime(time.Now().Add(friendRequestTTL))
	rel, err := d.Store.GetRelationship(ctx, senderID, targetID, nil, true, true)
	if err == nil {
		restoreRelationship(rel, model.RelationshipPending, expiredAt, nil)
		if err := d.Store.SaveRelationship(ctx, rel); err != nil {
			return nil, err
		}
	} else if errors.Is(err, store.ErrNotFound) {
		rel = &model.Relationship{
			AccountId: senderID.String(),
			RelatedId: targetID.String(),
			Status:    model.RelationshipPending,
			ExpiredAt: expiredAt,
		}
		if err := d.Store.InsertRelationship(ctx, rel); err != nil {
			return nil, err
		}
	} else {
		return nil, err
	}
	d.logActionService(ctx, senderID.String(), model.ActionLogRelationshipFriendRequest,
		map[string]any{"related_account_id": targetID.String()})
	d.pushFriendRequest(ctx, sender, targetID.String())
	d.purgeRelationshipCache(ctx, senderID, targetID, model.RelationshipPending)
	return rel, nil
}

// opAcceptFriendRelationship mirrors RelationshipService.AcceptFriendRelationship.
func (d Deps) opAcceptFriendRelationship(ctx context.Context, relationship *model.Relationship, status model.RelationshipStatus) (*model.Relationship, error) {
	if relationship.Status != model.RelationshipPending {
		return nil, errors.New("Cannot accept friend request that not in pending status.")
	}
	if status == model.RelationshipPending {
		return nil, errors.New("Cannot accept friend request by setting the new status to pending.")
	}
	relationship.Status = model.RelationshipFriends
	relationship.ExpiredAt = nil
	relationship.DegradeToStatus = nil
	if err := d.Store.SaveRelationship(ctx, relationship); err != nil {
		return nil, err
	}

	accountUUID := mustUUID(relationship.AccountId)
	relatedUUID := mustUUID(relationship.RelatedId)
	backward, err := d.Store.GetRelationship(ctx, relatedUUID, accountUUID, nil, true, true)
	if err == nil {
		restoreRelationship(backward, status, nil, nil)
		if err := d.Store.SaveRelationship(ctx, backward); err != nil {
			return nil, err
		}
	} else if errors.Is(err, store.ErrNotFound) {
		backward = &model.Relationship{
			AccountId: relationship.RelatedId,
			RelatedId: relationship.AccountId,
			Status:    status,
		}
		if err := d.Store.InsertRelationship(ctx, backward); err != nil {
			return nil, err
		}
	} else {
		return nil, err
	}

	d.logActionService(ctx, relationship.RelatedId, model.ActionLogRelationshipFriendAccept,
		map[string]any{"status": relationshipStatusNameLower(status), "related_account_id": relationship.AccountId})
	d.logActionService(ctx, relationship.AccountId, model.ActionLogRelationshipFriendEstablished,
		map[string]any{"related_account_id": relationship.RelatedId})
	d.logActionService(ctx, relationship.RelatedId, model.ActionLogRelationshipFriendEstablished,
		map[string]any{"related_account_id": relationship.AccountId})
	d.purgeRelationshipCache(ctx, accountUUID, relatedUUID, model.RelationshipFriends, status)
	return backward, nil
}

// pushFriendRequest mirrors the SendFriendRequest Ring push with the
// English localization strings.
func (d Deps) pushFriendRequest(ctx context.Context, sender *model.Account, targetID string) {
	if d.Clients == nil || d.Clients.Ring == nil {
		return
	}
	actionURI := "/account/relationships"
	title := strings.ReplaceAll("{sender} requested to be your friend", "{sender}", sender.Nick)
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	_, err := d.Clients.Ring.SendPushNotificationToUser(ctx, &gen.DySendPushNotificationToUserRequest{
		UserId: targetID,
		Notification: &gen.DyPushNotification{
			Topic:     "relationships.friends.request",
			Title:     title,
			Body:      "You can go to relationships page and decide accept their request or not.",
			ActionUri: &actionURI,
			IsSavable: true,
		},
	})
	if err != nil {
		d.Log.Warn("push friend request notification", "target", targetID, "error", err)
	}
}

// ─────────────────────── block / mute ───────────────────────

func (d Deps) blockUser(c *gin.Context) {
	d.respondBlockMute(c, true)
}

func (d Deps) unblockUser(c *gin.Context) {
	d.respondUnblockUnmute(c, true)
}

func (d Deps) muteUser(c *gin.Context) {
	d.respondBlockMute(c, false)
}

func (d Deps) unmuteUser(c *gin.Context) {
	d.respondUnblockUnmute(c, false)
}

// respondBlockMute ports BlockUser / MuteUser (both take RelationshipActionRequest).
func (d Deps) respondBlockMute(c *gin.Context, isBlock bool) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	targetID, ok := requireTarget(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if !d.targetExists(ctx, c, targetID) {
		return
	}
	var req relationshipActionRequest
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{"body": {"Invalid request body."}}))
			return
		}
	}
	var expiresIn *time.Duration
	if req.ExpiresIn != nil {
		duration, err := parseExpiresIn(*req.ExpiresIn)
		if err != nil {
			c.JSON(http.StatusBadRequest, errs.New(relErrCode(isBlock, "PASSPORT_BLOCK_FAILED", "PASSPORT_MUTE_FAILED"), err.Error(), http.StatusBadRequest))
			return
		}
		expiresIn = &duration
	}
	var (
		rel *model.Relationship
		err error
	)
	if isBlock {
		rel, err = d.opBlock(ctx, accountIDOf(user), targetID, expiresIn, req.DegradeTo)
	} else {
		rel, err = d.opMute(ctx, accountIDOf(user), targetID, expiresIn, req.DegradeTo)
	}
	if err != nil {
		writeRelError(c, err)
		return
	}
	if isBlock {
		d.logAction(c, user.Id, model.ActionLogRelationshipBlock,
			map[string]any{"status": "blocked", "related_account_id": targetID.String()})
	} else {
		d.logAction(c, user.Id, model.ActionLogRelationshipMute,
			map[string]any{"related_account_id": targetID.String()})
	}
	c.JSON(http.StatusOK, rel)
}

// respondUnblockUnmute ports UnblockUser / UnmuteUser.
func (d Deps) respondUnblockUnmute(c *gin.Context, isBlock bool) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	targetID, ok := requireTarget(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if !d.targetExists(ctx, c, targetID) {
		return
	}
	var (
		rel *model.Relationship
		err error
	)
	if isBlock {
		rel, err = d.opUnblock(ctx, accountIDOf(user), targetID)
	} else {
		rel, err = d.opUnmute(ctx, accountIDOf(user), targetID)
	}
	if err != nil {
		writeRelError(c, err)
		return
	}
	if isBlock {
		d.logAction(c, user.Id, model.ActionLogRelationshipUnblock,
			map[string]any{"related_account_id": targetID.String()})
	} else {
		d.logAction(c, user.Id, model.ActionLogRelationshipUnmute,
			map[string]any{"related_account_id": targetID.String()})
	}
	c.JSON(http.StatusOK, rel)
}

func relErrCode(isBlock bool, blockCode, muteCode string) string {
	if isBlock {
		return blockCode
	}
	return muteCode
}

// opBlock mirrors RelationshipService.BlockAccount.
func (d Deps) opBlock(ctx context.Context, senderID, targetID uuid.UUID, expiresIn *time.Duration, degradeTo *model.RelationshipStatus) (*model.Relationship, error) {
	var expiredAt *model.Time
	if expiresIn != nil {
		expiredAt = model.NewTime(time.Now().Add(*expiresIn))
	}
	var relationship *model.Relationship
	outgoing, err := d.Store.GetRelationship(ctx, senderID, targetID, nil, true, true)
	if err == nil {
		restoreRelationship(outgoing, model.RelationshipBlocked, expiredAt, degradeTo)
		if err := d.Store.SaveRelationship(ctx, outgoing); err != nil {
			return nil, err
		}
		relationship = outgoing
	} else if errors.Is(err, store.ErrNotFound) {
		incoming, err := d.Store.GetRelationship(ctx, targetID, senderID, nil, true, true)
		if err == nil && incoming.DeletedAt == nil {
			incoming.DeletedAt = model.NewTime(time.Now())
			if err := d.Store.SaveRelationship(ctx, incoming); err != nil {
				return nil, err
			}
		} else if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		relationship = &model.Relationship{
			AccountId:       senderID.String(),
			RelatedId:       targetID.String(),
			Status:          model.RelationshipBlocked,
			ExpiredAt:       expiredAt,
			DegradeToStatus: degradeTo,
		}
		if err := d.Store.InsertRelationship(ctx, relationship); err != nil {
			return nil, err
		}
	} else {
		return nil, err
	}
	d.purgeRelationshipCache(ctx, senderID, targetID, model.RelationshipBlocked)
	return relationship, nil
}

// opUnblock mirrors RelationshipService.UnblockAccount.
func (d Deps) opUnblock(ctx context.Context, senderID, targetID uuid.UUID) (*model.Relationship, error) {
	rel, err := d.Store.GetRelationship(ctx, senderID, targetID, relStatusPtr(model.RelationshipBlocked), false, false)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, relErrNew("PASSPORT_UNBLOCK_FAILED", "There is no relationship between you and the user.", http.StatusBadRequest)
		}
		return nil, err
	}
	rel.Status = model.RelationshipFriends
	rel.ExpiredAt = nil
	rel.DegradeToStatus = nil
	if err := d.Store.SaveRelationship(ctx, rel); err != nil {
		return nil, err
	}
	d.purgeRelationshipCache(ctx, senderID, targetID, model.RelationshipBlocked, model.RelationshipFriends)
	return rel, nil
}

// opMute mirrors RelationshipService.MuteAccount.
func (d Deps) opMute(ctx context.Context, senderID, targetID uuid.UUID, expiresIn *time.Duration, degradeTo *model.RelationshipStatus) (*model.Relationship, error) {
	var expiredAt *model.Time
	if expiresIn != nil {
		expiredAt = model.NewTime(time.Now().Add(*expiresIn))
	}
	rel, err := d.Store.GetRelationship(ctx, senderID, targetID, nil, true, true)
	if err == nil {
		if rel.DeletedAt == nil && rel.Status == model.RelationshipMuted {
			return nil, relErrNew("PASSPORT_MUTE_FAILED", "You have already muted this user.", http.StatusBadRequest)
		}
		restoreRelationship(rel, model.RelationshipMuted, expiredAt, degradeTo)
		if err := d.Store.SaveRelationship(ctx, rel); err != nil {
			return nil, err
		}
	} else if errors.Is(err, store.ErrNotFound) {
		rel = &model.Relationship{
			AccountId:       senderID.String(),
			RelatedId:       targetID.String(),
			Status:          model.RelationshipMuted,
			ExpiredAt:       expiredAt,
			DegradeToStatus: degradeTo,
		}
		if err := d.Store.InsertRelationship(ctx, rel); err != nil {
			return nil, err
		}
	} else {
		return nil, err
	}
	d.purgeRelationshipCache(ctx, senderID, targetID, model.RelationshipMuted)
	return rel, nil
}

// opUnmute mirrors RelationshipService.UnmuteAccount.
func (d Deps) opUnmute(ctx context.Context, senderID, targetID uuid.UUID) (*model.Relationship, error) {
	rel, err := d.Store.GetRelationship(ctx, senderID, targetID, relStatusPtr(model.RelationshipMuted), false, false)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, relErrNew("PASSPORT_UNMUTE_FAILED", "There is no mute relationship with this user.", http.StatusBadRequest)
		}
		return nil, err
	}
	rel.Status = model.RelationshipFriends
	rel.ExpiredAt = nil
	rel.DegradeToStatus = nil
	if err := d.Store.SaveRelationship(ctx, rel); err != nil {
		return nil, err
	}
	d.purgeRelationshipCache(ctx, senderID, targetID, model.RelationshipMuted, model.RelationshipFriends)
	return rel, nil
}

// ─────────────────────── close friends / alias / mutual ───────────────────────

func (d Deps) addCloseFriend(c *gin.Context) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	targetID, ok := requireTarget(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if !d.targetExists(ctx, c, targetID) {
		return
	}
	rel, err := d.opAddCloseFriend(ctx, accountIDOf(user), targetID)
	if err != nil {
		writeRelError(c, err)
		return
	}
	d.logAction(c, user.Id, model.ActionLogRelationshipCloseFriend,
		map[string]any{"related_account_id": targetID.String()})
	c.JSON(http.StatusOK, rel)
}

func (d Deps) removeCloseFriend(c *gin.Context) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	targetID, ok := requireTarget(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if !d.targetExists(ctx, c, targetID) {
		return
	}
	rel, err := d.opRemoveCloseFriend(ctx, accountIDOf(user), targetID)
	if err != nil {
		writeRelError(c, err)
		return
	}
	d.logAction(c, user.Id, model.ActionLogRelationshipUnCloseFriend,
		map[string]any{"related_account_id": targetID.String()})
	c.JSON(http.StatusOK, rel)
}

func (d Deps) listCloseFriends(c *gin.Context) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	ctx := c.Request.Context()
	ids, err := d.cachedRelatedIDs(ctx, accountIDOf(user), model.RelationshipCloseFriend, false)
	if err != nil {
		internalError(c, err)
		return
	}
	accounts := d.accountsForIDs(ctx, ids)
	if accounts == nil {
		accounts = []model.Account{}
	}
	c.JSON(http.StatusOK, accounts)
}

func (d Deps) updateAlias(c *gin.Context) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	targetID, ok := requireTarget(c)
	if !ok {
		return
	}
	var req aliasRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{"body": {"Invalid request body."}}))
		return
	}
	if req.Alias != nil && len(*req.Alias) > 128 {
		c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{"alias": {"Alias exceeds the maximum length of 128."}}))
		return
	}
	ctx := c.Request.Context()
	rel, err := d.opUpdateAlias(ctx, accountIDOf(user), targetID, req.Alias)
	if err != nil {
		writeRelError(c, err)
		return
	}
	d.hydrateRelationships(ctx, []model.Relationship{*rel})
	c.JSON(http.StatusOK, rel)
}

func (d Deps) getMutualFriends(c *gin.Context) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	targetID, ok := requireTarget(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	myFriends, err := d.cachedRelatedIDs(ctx, accountIDOf(user), model.RelationshipFriends, false)
	if err != nil {
		internalError(c, err)
		return
	}
	theirFriends, err := d.cachedRelatedIDs(ctx, targetID, model.RelationshipFriends, false)
	if err != nil {
		internalError(c, err)
		return
	}
	theirs := make(map[string]bool, len(theirFriends))
	for _, id := range theirFriends {
		theirs[id] = true
	}
	var mutual []string
	for _, id := range myFriends {
		if theirs[id] {
			mutual = append(mutual, id)
		}
	}
	accounts := d.accountsForIDs(ctx, mutual)
	if accounts == nil {
		accounts = []model.Account{}
	}
	c.JSON(http.StatusOK, accounts)
}

// accountsForIDs loads accounts for a list of ids with their profiles (the
// C# loads them one by one via GetAccount, which attaches the profile).
func (d Deps) accountsForIDs(ctx context.Context, ids []string) []model.Account {
	if len(ids) == 0 {
		return []model.Account{}
	}
	uuids := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if parsed, err := uuid.Parse(id); err == nil {
			uuids = append(uuids, parsed)
		}
	}
	accounts, err := d.Store.GetAccountsByIDs(ctx, uuids)
	if err != nil {
		d.Log.Warn("load accounts by ids", "error", err)
		return []model.Account{}
	}
	for i := range accounts {
		if profile, err := d.Store.GetOrCreateAccountProfile(ctx, mustUUID(accounts[i].Id)); err == nil {
			accounts[i].Profile = profile
		}
	}
	return accounts
}

// opAddCloseFriend mirrors RelationshipService.AddCloseFriend.
func (d Deps) opAddCloseFriend(ctx context.Context, senderID, targetID uuid.UUID) (*model.Relationship, error) {
	rel, err := d.Store.GetRelationship(ctx, senderID, targetID, nil, true, true)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	if rel == nil || rel.DeletedAt != nil ||
		(rel.Status != model.RelationshipFriends && rel.Status != model.RelationshipCloseFriend) {
		return nil, relErrNew("PASSPORT_CLOSE_FRIEND_ADD_FAILED",
			"Only friends can be added to your close friends list.", http.StatusBadRequest)
	}
	if rel.Status == model.RelationshipCloseFriend {
		return nil, relErrNew("PASSPORT_CLOSE_FRIEND_ADD_FAILED",
			"This user is already in your close friends list.", http.StatusBadRequest)
	}
	count, err := d.Store.CountRelationshipsByStatus(ctx, senderID, model.RelationshipCloseFriend)
	if err != nil {
		return nil, err
	}
	if count >= maxCloseFriends {
		return nil, relErrNew("PASSPORT_CLOSE_FRIEND_ADD_FAILED",
			fmt.Sprintf("You can have at most %d close friends.", maxCloseFriends), http.StatusBadRequest)
	}
	rel.Status = model.RelationshipCloseFriend
	rel.ExpiredAt = nil
	rel.DegradeToStatus = nil
	if err := d.Store.SaveRelationship(ctx, rel); err != nil {
		return nil, err
	}
	d.purgeRelationshipCache(ctx, senderID, targetID, model.RelationshipFriends, model.RelationshipCloseFriend)
	return rel, nil
}

// opRemoveCloseFriend mirrors RelationshipService.RemoveCloseFriend.
func (d Deps) opRemoveCloseFriend(ctx context.Context, senderID, targetID uuid.UUID) (*model.Relationship, error) {
	rel, err := d.Store.GetRelationship(ctx, senderID, targetID, relStatusPtr(model.RelationshipCloseFriend), false, false)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, relErrNew("PASSPORT_CLOSE_FRIEND_NOT_FOUND",
				"This user is not in your close friends list.", http.StatusNotFound)
		}
		return nil, err
	}
	rel.Status = model.RelationshipFriends
	rel.ExpiredAt = nil
	rel.DegradeToStatus = nil
	if err := d.Store.SaveRelationship(ctx, rel); err != nil {
		return nil, err
	}
	d.purgeRelationshipCache(ctx, senderID, targetID, model.RelationshipFriends, model.RelationshipCloseFriend)
	return rel, nil
}

// opUpdateAlias mirrors RelationshipService.UpdateAlias (no expiry filter).
func (d Deps) opUpdateAlias(ctx context.Context, accountID, relatedID uuid.UUID, alias *string) (*model.Relationship, error) {
	rel, err := d.Store.GetRelationship(ctx, accountID, relatedID, nil, true, false)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, relErrNew("PASSPORT_RELATIONSHIP_NOT_FOUND",
				"There is no relationship between you and the user.", http.StatusNotFound)
		}
		return nil, err
	}
	if alias == nil || strings.TrimSpace(*alias) == "" {
		rel.Alias = nil
	} else {
		trimmed := strings.TrimSpace(*alias)
		rel.Alias = &trimmed
	}
	if err := d.Store.SaveRelationship(ctx, rel); err != nil {
		return nil, err
	}
	return rel, nil
}

// restoreRelationship mirrors RelationshipService.RestoreRelationship.
func restoreRelationship(rel *model.Relationship, status model.RelationshipStatus, expiredAt *model.Time, degradeTo *model.RelationshipStatus) {
	rel.DeletedAt = nil
	rel.Status = status
	rel.ExpiredAt = expiredAt
	rel.DegradeToStatus = degradeTo
}

// logActionService mirrors RelationshipService.CreateActionLog, which reads
// the ambient HttpContext (absent here) for user-agent/ip and always attaches
// related_account_id via the meta the caller provides.
func (d Deps) logActionService(ctx context.Context, accountID string, action model.ActionLogType, meta map[string]any) {
	if d.Logs == nil {
		return
	}
	if meta == nil {
		meta = map[string]any{}
	}
	_ = d.Logs.Create(ctx, accountID, action, meta, "", "", nil, nil)
}

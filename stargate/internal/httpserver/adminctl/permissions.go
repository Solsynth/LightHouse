package adminctl

// Port of Padlock's PermissionAdminController (/api/admin/permissions):
// permission-group CRUD, group permission nodes, group memberships and actor
// permission inspection. Route paths, DTOs, error codes/messages and the
// dual [AskPermission] requirements match the C# file.

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/go/pkg/errs"
	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/permission"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// permissionGroupSummary mirrors PermissionGroupSummary.
type permissionGroupSummary struct {
	Id          string      `json:"id"`
	Key         string      `json:"key"`
	NodeCount   int         `json:"node_count"`
	MemberCount int         `json:"member_count"`
	CreatedAt   *model.Time `json:"created_at,omitempty"`
	UpdatedAt   *model.Time `json:"updated_at,omitempty"`
}

// permissionGroupDetailResponse mirrors PermissionGroupDetailResponse.
type permissionGroupDetailResponse struct {
	Group       store.PermissionGroup    `json:"group"`
	Nodes       []store.PermissionNode   `json:"nodes"`
	NodeTotal   int                      `json:"node_total"`
	Members     []store.PermissionMember `json:"members"`
	MemberTotal int                      `json:"member_total"`
}

// adminActorPermissionsResponse mirrors AdminActorPermissionsResponse.
type adminActorPermissionsResponse struct {
	Actor                string                            `json:"actor"`
	DirectPermissions    []store.PermissionNode            `json:"direct_permissions"`
	EffectivePermissions []store.PermissionNode            `json:"effective_permissions"`
	Groups               []store.PermissionMemberWithGroup `json:"groups"`
}

type createPermissionGroupRequest struct {
	Key string `json:"key"`
}

type updatePermissionGroupRequest struct {
	Key string `json:"key"`
}

type upsertGroupPermissionRequest struct {
	Value      json.RawMessage `json:"value"`
	ExpiredAt  *model.Time     `json:"expired_at"`
	AffectedAt *model.Time     `json:"affected_at"`
}

type groupMembershipRequest struct {
	ExpiredAt  *model.Time `json:"expired_at"`
	AffectedAt *model.Time `json:"affected_at"`
}

// permissionNodeActorType mirrors PermissionNodeActorType (Group = 1).
const permissionNodeActorTypeGroup = 1

func registerPermissionAdmin(g *gin.RouterGroup, d Deps) {
	g.GET("groups", requirePerm(d, permission.PermissionsGroupsCheck), listPermissionGroups(d))
	g.GET("groups/:groupId", requirePerm(d, permission.PermissionsGroupsCheck), getPermissionGroup(d))
	g.POST("groups", requirePerm(d, permission.PermissionsGroupsManage), createPermissionGroup(d))
	g.PATCH("groups/:groupId", requirePerm(d, permission.PermissionsGroupsManage), updatePermissionGroup(d))
	g.DELETE("groups/:groupId", requirePerm(d, permission.PermissionsGroupsManage), deletePermissionGroup(d))
	g.PUT("groups/:groupId/permissions/:key", requirePerm(d, permission.PermissionsManage, permission.PermissionsGroupsManage), upsertGroupPermission(d))
	g.DELETE("groups/:groupId/permissions/:key", requirePerm(d, permission.PermissionsManage, permission.PermissionsGroupsManage), deleteGroupPermission(d))
	g.PUT("groups/:groupId/members/:actor", requirePerm(d, permission.PermissionsGroupsManage), upsertGroupMember(d))
	g.DELETE("groups/:groupId/members/:actor", requirePerm(d, permission.PermissionsGroupsManage), deleteGroupMember(d))
	g.GET("actors/:actor", requirePerm(d, permission.PermissionsCheck, permission.PermissionsGroupsCheck), getActorPermissions(d))
}

func listPermissionGroups(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		take := queryTake(c, 50)
		offset := queryOffset(c)
		query := c.Query("query")
		groups, total, err := d.Store.PermissionGroupList(c.Request.Context(), query, take, offset)
		if err != nil {
			serverError(c, err, d)
			return
		}
		response := make([]permissionGroupSummary, 0, len(groups))
		for i := range groups {
			response = append(response, permissionGroupSummary{
				Id:          groups[i].Id,
				Key:         groups[i].Key,
				NodeCount:   groups[i].NodeCount,
				MemberCount: groups[i].MemberCount,
				CreatedAt:   groups[i].CreatedAt,
				UpdatedAt:   groups[i].UpdatedAt,
			})
		}
		setTotal(c, total)
		c.JSON(http.StatusOK, response)
	}
}

func getPermissionGroup(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		groupID, err := uuid.Parse(c.Param("groupId"))
		if err != nil {
			permissionGroupNotFound(c)
			return
		}
		group, err := d.Store.PermissionGroupGet(c.Request.Context(), groupID)
		if err != nil {
			if err == store.ErrNotFound {
				permissionGroupNotFound(c)
				return
			}
			serverError(c, err, d)
			return
		}
		nodesTake, nodesOffset := queryTakeZero(c, "nodesTake", "nodesOffset", 50)
		membersTake, membersOffset := queryTakeZero(c, "membersTake", "membersOffset", 50)

		nodes, nodeTotal, err := d.Store.PermissionNodeList(c.Request.Context(), groupID, nodesTake, nodesOffset)
		if err != nil {
			serverError(c, err, d)
			return
		}
		members, memberTotal, err := d.Store.PermissionMemberList(c.Request.Context(), groupID, membersTake, membersOffset)
		if err != nil {
			serverError(c, err, d)
			return
		}
		c.JSON(http.StatusOK, permissionGroupDetailResponse{
			Group:       *group,
			Nodes:       nodes,
			NodeTotal:   nodeTotal,
			Members:     members,
			MemberTotal: memberTotal,
		})
	}
}

func createPermissionGroup(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request createPermissionGroupRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, errs.New("PERMISSION_GROUP_KEY_EMPTY", "Group key cannot be empty.", http.StatusBadRequest))
			return
		}
		key := strings.TrimSpace(request.Key)
		if key == "" {
			c.JSON(http.StatusBadRequest, errs.New("PERMISSION_GROUP_KEY_EMPTY", "Group key cannot be empty.", http.StatusBadRequest))
			return
		}
		exists, err := d.Store.PermissionGroupKeyExists(c.Request.Context(), key, nil)
		if err != nil {
			serverError(c, err, d)
			return
		}
		if exists {
			c.JSON(http.StatusConflict, errs.New("PERMISSION_GROUP_KEY_CONFLICT", "A permission group with this key already exists.", http.StatusConflict))
			return
		}
		group, err := d.Store.PermissionGroupCreate(c.Request.Context(), key)
		if err != nil {
			serverError(c, err, d)
			return
		}
		c.Header("Location", "/api/admin/permissions/groups/"+group.Id)
		c.JSON(http.StatusCreated, group)
	}
}

func updatePermissionGroup(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		groupID, err := uuid.Parse(c.Param("groupId"))
		if err != nil {
			permissionGroupNotFound(c)
			return
		}
		var request updatePermissionGroupRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, errs.New("PERMISSION_GROUP_KEY_EMPTY", "Group key cannot be empty.", http.StatusBadRequest))
			return
		}
		key := strings.TrimSpace(request.Key)
		if key == "" {
			c.JSON(http.StatusBadRequest, errs.New("PERMISSION_GROUP_KEY_EMPTY", "Group key cannot be empty.", http.StatusBadRequest))
			return
		}
		group, err := d.Store.PermissionGroupGet(c.Request.Context(), groupID)
		if err != nil {
			if err == store.ErrNotFound {
				permissionGroupNotFound(c)
				return
			}
			serverError(c, err, d)
			return
		}
		if group.Key == "default" && key != "default" {
			c.JSON(http.StatusBadRequest, errs.New("PERMISSION_GROUP_DEFAULT_RENAME", "The default permission group cannot be renamed.", http.StatusBadRequest))
			return
		}
		exists, err := d.Store.PermissionGroupKeyExists(c.Request.Context(), key, &groupID)
		if err != nil {
			serverError(c, err, d)
			return
		}
		if exists {
			c.JSON(http.StatusConflict, errs.New("PERMISSION_GROUP_KEY_CONFLICT", "A permission group with this key already exists.", http.StatusConflict))
			return
		}
		updated, err := d.Store.PermissionGroupUpdateKey(c.Request.Context(), groupID, key)
		if err != nil {
			serverError(c, err, d)
			return
		}
		clearGroupMemberCaches(c, d, groupID)
		c.JSON(http.StatusOK, updated)
	}
}

func deletePermissionGroup(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		groupID, err := uuid.Parse(c.Param("groupId"))
		if err != nil {
			permissionGroupNotFound(c)
			return
		}
		group, err := d.Store.PermissionGroupGet(c.Request.Context(), groupID)
		if err != nil {
			if err == store.ErrNotFound {
				permissionGroupNotFound(c)
				return
			}
			serverError(c, err, d)
			return
		}
		if group.Key == "default" {
			c.JSON(http.StatusBadRequest, errs.New("PERMISSION_GROUP_DEFAULT_DELETE", "The default permission group cannot be deleted.", http.StatusBadRequest))
			return
		}
		actors, err := d.Store.PermissionGroupDelete(c.Request.Context(), groupID)
		if err != nil {
			serverError(c, err, d)
			return
		}
		clearActorCaches(c, d, actors)
		c.Status(http.StatusNoContent)
	}
}

func upsertGroupPermission(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		groupID, err := uuid.Parse(c.Param("groupId"))
		if err != nil {
			permissionGroupNotFound(c)
			return
		}
		key := c.Param("key")
		group, err := d.Store.PermissionGroupGet(c.Request.Context(), groupID)
		if err != nil {
			if err == store.ErrNotFound {
				permissionGroupNotFound(c)
				return
			}
			serverError(c, err, d)
			return
		}
		if !isValidPermissionPattern(key) {
			c.JSON(http.StatusBadRequest, errs.New("PERMISSION_KEY_INVALID_PATTERN", "Permission key contains invalid characters or wildcards.", http.StatusBadRequest))
			return
		}
		var request upsertGroupPermissionRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, errs.New("PERMISSION_KEY_INVALID_PATTERN", "Permission key contains invalid characters or wildcards.", http.StatusBadRequest))
			return
		}
		value := []byte("true")
		if len(request.Value) > 0 && string(request.Value) != "null" {
			value = request.Value
		}
		node, err := d.Store.PermissionNodeUpsert(c.Request.Context(), groupID, key, value, "group:"+group.Key, permissionNodeActorTypeGroup, request.ExpiredAt, request.AffectedAt)
		if err != nil {
			serverError(c, err, d)
			return
		}
		clearGroupMemberCaches(c, d, groupID)
		c.JSON(http.StatusOK, node)
	}
}

func deleteGroupPermission(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		groupID, err := uuid.Parse(c.Param("groupId"))
		if err != nil {
			permissionNodeNotFound(c)
			return
		}
		key := c.Param("key")
		if err := d.Store.PermissionNodeDelete(c.Request.Context(), groupID, key); err != nil {
			if err == store.ErrNotFound {
				permissionNodeNotFound(c)
				return
			}
			serverError(c, err, d)
			return
		}
		clearGroupMemberCaches(c, d, groupID)
		c.Status(http.StatusNoContent)
	}
}

func upsertGroupMember(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		groupID, err := uuid.Parse(c.Param("groupId"))
		if err != nil {
			permissionGroupNotFound(c)
			return
		}
		groupExists, err := d.Store.PermissionGroupGet(c.Request.Context(), groupID)
		if err != nil {
			if err == store.ErrNotFound {
				permissionGroupNotFound(c)
				return
			}
			serverError(c, err, d)
			return
		}
		_ = groupExists
		actor := c.Param("actor")
		if strings.TrimSpace(actor) == "" {
			c.JSON(http.StatusBadRequest, errs.New("PERMISSION_ACTOR_EMPTY", "Actor cannot be empty.", http.StatusBadRequest))
			return
		}
		var request groupMembershipRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, errs.New("PERMISSION_ACTOR_EMPTY", "Actor cannot be empty.", http.StatusBadRequest))
			return
		}
		member, err := d.Store.PermissionMemberUpsert(c.Request.Context(), groupID, actor, request.ExpiredAt, request.AffectedAt)
		if err != nil {
			serverError(c, err, d)
			return
		}
		clearActorPermissionCache(d, c, actor)
		c.JSON(http.StatusOK, member)
	}
}

func deleteGroupMember(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		groupID, err := uuid.Parse(c.Param("groupId"))
		if err != nil {
			c.JSON(http.StatusNotFound, errs.New("PERMISSION_ACTOR_NOT_IN_GROUP", "Actor is not in this group.", http.StatusNotFound))
			return
		}
		actor := c.Param("actor")
		if err := d.Store.PermissionMemberDelete(c.Request.Context(), groupID, actor); err != nil {
			if err == store.ErrNotFound {
				c.JSON(http.StatusNotFound, errs.New("PERMISSION_ACTOR_NOT_IN_GROUP", "Actor is not in this group.", http.StatusNotFound))
				return
			}
			serverError(c, err, d)
			return
		}
		clearActorPermissionCache(d, c, actor)
		c.Status(http.StatusNoContent)
	}
}

func getActorPermissions(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		actor := c.Param("actor")
		if strings.TrimSpace(actor) == "" {
			c.JSON(http.StatusBadRequest, errs.New("PERMISSION_ACTOR_EMPTY", "Actor cannot be empty.", http.StatusBadRequest))
			return
		}
		now := time.Now().UTC()
		direct, err := d.Store.PermissionDirectNodes(c.Request.Context(), actor)
		if err != nil {
			serverError(c, err, d)
			return
		}
		effective, err := d.Store.PermissionEffectiveNodes(c.Request.Context(), actor, now)
		if err != nil {
			serverError(c, err, d)
			return
		}
		groups, err := d.Store.PermissionMembersForActor(c.Request.Context(), actor)
		if err != nil {
			serverError(c, err, d)
			return
		}
		c.JSON(http.StatusOK, adminActorPermissionsResponse{
			Actor:                actor,
			DirectPermissions:    direct,
			EffectivePermissions: effective,
			Groups:               groups,
		})
	}
}

// ─────────────────────────── helpers ───────────────────────────

func permissionGroupNotFound(c *gin.Context) {
	c.JSON(http.StatusNotFound, errs.New("PERMISSION_GROUP_NOT_FOUND", "Permission group not found.", http.StatusNotFound))
}

func permissionNodeNotFound(c *gin.Context) {
	c.JSON(http.StatusNotFound, errs.New("PERMISSION_NODE_NOT_FOUND", "Permission node not found.", http.StatusNotFound))
}

// queryTakeZero reads a take/offset pair where take allows 0 (the C# group
// detail clamps nodes/members take to Math.Clamp(v, 0, 200)).
func queryTakeZero(c *gin.Context, takeName, offsetName string, def int) (int, int) {
	take := def
	if raw := c.Query(takeName); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			take = parsed
		}
	}
	if take < 0 {
		take = 0
	}
	if take > 200 {
		take = 200
	}
	offset := 0
	if raw := c.Query(offsetName); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			offset = parsed
		}
	}
	return take, offset
}

// isValidPermissionPattern mirrors PermissionService.IsValidPermissionPattern.
func isValidPermissionPattern(pattern string) bool {
	if strings.TrimSpace(pattern) == "" {
		return false
	}
	if strings.Contains(pattern, "**") || strings.HasPrefix(pattern, "*") || strings.HasSuffix(pattern, "*") {
		return false
	}
	for _, r := range pattern {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || r == '.' || r == '*' || r == ':') {
			return false
		}
	}
	return true
}

// clearGroupMemberCaches mirrors ClearGroupMemberCachesAsync.
func clearGroupMemberCaches(c *gin.Context, d Deps, groupID uuid.UUID) {
	actors, err := d.Store.PermissionGroupMemberActors(c.Request.Context(), groupID)
	if err != nil {
		return
	}
	clearActorCaches(c, d, actors)
}

// clearActorCaches mirrors ClearActorCachesAsync.
func clearActorCaches(c *gin.Context, d Deps, actors []string) {
	seen := map[string]struct{}{}
	for _, actor := range actors {
		if _, ok := seen[actor]; ok {
			continue
		}
		seen[actor] = struct{}{}
		clearActorPermissionCache(d, c, actor)
	}
}

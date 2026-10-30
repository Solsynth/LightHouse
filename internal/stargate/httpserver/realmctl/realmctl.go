// Package realmctl mounts Passport's realm HTTP surface: /api/realms (public
// and member routes), /api/realms/public and /api/admin/realms.
//
// Route paths, DTO field names, error codes and the [AskPermission] keys match
// RealmController / RealmPublicController / RealmAdminController.
package realmctl

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/go/pkg/errs"
	"src.solsynth.dev/sosys/stargate/middleware"
	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/permission"
	"src.solsynth.dev/sosys/stargate/realm"
)

// Deps is the dependency set the realm controllers need.
type Deps struct {
	Service *realm.Service
	Store   *realm.Store
	// Accounts hydrates member payloads from the core account tables.
	Accounts realm.AccountLookup
	// Profiles reloads the caller with its profile for the quota calculation;
	// the session account carries a perk level but no profile row.
	Profiles realm.ProfileLookup
	// Statuses assembles the read-time account status for
	// GET /{slug}/members?withStatus=true.
	Statuses StatusLoader
	// Files resolves picture/background ids through the Drive port.
	Files realm.FileResolver
	// Relationships answers the invite-block check (RelationshipService
	// .HasRelationshipWithStatus).
	Relationships BlockChecker
	// Payments opens the wallet order a boost purchase is charged on.
	Payments realm.Payments
	Perm     middleware.PermissionChecker
	Logs     ActionLogSink
	Log      *slog.Logger
}

// ActionLogSink records account action logs; it matches actionlog.Service.
type ActionLogSink interface {
	Create(ctx context.Context, accountID string, action model.ActionLogType, meta map[string]any, userAgent, ipAddress string, location *string, sessionID *string) error
}

// BlockChecker reports whether the account blocked the related account.
type BlockChecker interface {
	HasStatus(ctx context.Context, accountID, relatedID uuid.UUID, status model.RelationshipStatus) (bool, error)
}

// StatusLoader assembles account statuses for the member listing; nil leaves
// the status projection empty instead of failing the listing.
type StatusLoader interface {
	Statuses(ctx context.Context, ownerIDs []uuid.UUID, viewer *uuid.UUID) (map[uuid.UUID]*model.AccountStatus, error)
}

// Register mounts the public and member realm routes on the /api group.
func Register(api *gin.RouterGroup, d Deps) {
	realms := api.Group("/realms")
	realms.GET("/public", d.listCommunityRealms)
	realms.GET("/quota", middleware.RequireAuth(), d.getQuota)
	realms.GET("/invites", middleware.RequireAuth(), d.listInvites)
	realms.POST("/invites/:slug", middleware.RequireAuth(),
		middleware.AskPermission(d.Perm, permission.RealmsInvitesManage), d.inviteMember)
	realms.POST("/invites/:slug/accept", middleware.RequireAuth(),
		middleware.AskPermission(d.Perm, permission.RealmsInvitesManage), d.acceptInvite)
	realms.POST("/invites/:slug/decline", middleware.RequireAuth(),
		middleware.AskPermission(d.Perm, permission.RealmsInvitesManage), d.declineInvite)
	realms.GET("", middleware.RequireAuth(), d.listJoinedRealms)
	realms.POST("", middleware.RequireAuth(), d.createRealm)

	realms.GET("/:slug", d.getRealm)
	realms.PATCH("/:slug", middleware.RequireAuth(),
		middleware.AskPermission(d.Perm, permission.RealmsUpdate), d.updateRealm)
	realms.DELETE("/:slug", middleware.RequireAuth(),
		middleware.AskPermission(d.Perm, permission.RealmsDelete), d.deleteRealm)

	realms.GET("/:slug/members", d.listMembers)
	realms.POST("/:slug/members/me", middleware.RequireAuth(),
		middleware.AskPermission(d.Perm, permission.RealmsMembersManage), d.joinRealm)
	realms.GET("/:slug/members/me", middleware.RequireAuth(), d.currentMembership)
	realms.PATCH("/:slug/members/me/profile", middleware.RequireAuth(),
		middleware.AskPermission(d.Perm, permission.RealmsMembersManage), d.updateMembershipProfile)
	realms.DELETE("/:slug/members/me/profile", middleware.RequireAuth(),
		middleware.AskPermission(d.Perm, permission.RealmsMembersManage), d.resetMembershipProfile)
	realms.DELETE("/:slug/members/me", middleware.RequireAuth(),
		middleware.AskPermission(d.Perm, permission.RealmsMembersManage), d.leaveRealm)
	realms.DELETE("/:slug/members/:memberId", middleware.RequireAuth(),
		middleware.AskPermission(d.Perm, permission.RealmsMembersManage), d.removeMember)
	realms.PATCH("/:slug/members/:memberId/role", middleware.RequireAuth(),
		middleware.AskPermission(d.Perm, permission.RealmsMembersManage), d.updateMemberRole)
	realms.PATCH("/:slug/members/:memberId/label", middleware.RequireAuth(),
		middleware.AskPermission(d.Perm, permission.RealmsMembersManage), d.updateMemberLabel)
	realms.GET("/:slug/members/:memberId/experience", d.memberExperience)

	realms.GET("/:slug/labels", d.listLabels)
	realms.POST("/:slug/labels", middleware.RequireAuth(),
		middleware.AskPermission(d.Perm, permission.RealmsLabelsManage), d.createLabel)
	realms.PATCH("/:slug/labels/:labelId", middleware.RequireAuth(),
		middleware.AskPermission(d.Perm, permission.RealmsLabelsManage), d.updateLabel)
	realms.DELETE("/:slug/labels/:labelId", middleware.RequireAuth(),
		middleware.AskPermission(d.Perm, permission.RealmsLabelsManage), d.deleteLabel)

	realms.POST("/:slug/boosts", middleware.RequireAuth(),
		middleware.AskPermission(d.Perm, permission.RealmsBoostsManage), d.boostRealm)
	realms.GET("/:slug/boosts", d.boostStatus)
	realms.GET("/:slug/boosts/leaderboard", d.boostLeaderboard)

	realms.GET("/:slug/permissions/roles", middleware.RequireAuth(), d.listRolePermissions)
	realms.POST("/:slug/permissions/roles", middleware.RequireAuth(),
		middleware.AskPermission(d.Perm, permission.RealmsPermissionsManage), d.updateRolePermissions)
	realms.GET("/:slug/permissions/users/:accountId", middleware.RequireAuth(), d.getUserPermissions)
	realms.POST("/:slug/permissions/users", middleware.RequireAuth(),
		middleware.AskPermission(d.Perm, permission.RealmsPermissionsManage), d.updateUserPermissions)
	realms.GET("/:slug/posts/moderation-logs", middleware.RequireAuth(), d.moderationLogs)
}

// RegisterAdmin mounts the /api/admin/realms console routes.
func RegisterAdmin(api *gin.RouterGroup, d Deps) {
	admin := api.Group("/admin/realms")
	admin.GET("", middleware.AskPermission(d.Perm, permission.RealmsModerate), d.adminListRealms)
	admin.GET("/:slug", middleware.AskPermission(d.Perm, permission.RealmsModerate), d.adminGetRealm)
	admin.PATCH("/:slug", middleware.AskPermission(d.Perm, permission.RealmsUpdate), d.adminUpdateRealm)
	admin.POST("/:slug/verification", middleware.AskPermission(d.Perm, permission.RealmsModerate), d.adminSetVerification)
	admin.DELETE("/:slug/verification", middleware.AskPermission(d.Perm, permission.RealmsModerate), d.adminClearVerification)
	admin.GET("/:slug/members", middleware.AskPermission(d.Perm, permission.RealmsModerate), d.adminListMembers)
	admin.PATCH("/:slug/members/:memberId/role", middleware.AskPermission(d.Perm, permission.RealmsMembersManage), d.adminUpdateMemberRole)
	admin.DELETE("/:slug/members/:memberId", middleware.AskPermission(d.Perm, permission.RealmsMembersManage), d.adminRemoveMember)
	admin.DELETE("/:slug", middleware.AskPermission(d.Perm, permission.RealmsDelete), d.adminDeleteRealm)
}

// ─────────────────────────── helpers ───────────────────────────

var (
	errRealmNotFound  = errors.New("realm not found")
	errMemberNotFound = errors.New("realm member not found")
)

// currentAccount mirrors the C# CurrentUser check: 401 when absent.
func currentAccount(c *gin.Context) *model.Account {
	account := middleware.CurrentUser(c.Request.Context())
	if account == nil {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized("Authentication is required."))
		return nil
	}
	return account
}

func accountIDOf(account *model.Account) (uuid.UUID, bool) {
	id, err := uuid.Parse(account.Id)
	return id, err == nil
}

// forbidden is ApiError.Unauthorized(message, forbidden: true): code FORBIDDEN,
// status 403, and the caller's message.
func forbidden(c *gin.Context, message string) {
	c.JSON(http.StatusForbidden, &errs.ApiError{
		Code: "FORBIDDEN", Message: message, Status: http.StatusForbidden,
	})
}

func realmNotFound(c *gin.Context) {
	c.JSON(http.StatusNotFound, &errs.ApiError{
		Code: "PASSPORT_REALM_NOT_FOUND", Message: "Realm not found.", Status: http.StatusNotFound,
	})
}

func memberNotFound(c *gin.Context) {
	c.JSON(http.StatusNotFound, &errs.ApiError{
		Code: "PASSPORT_REALM_MEMBER_NOT_FOUND", Message: "Realm member not found.", Status: http.StatusNotFound,
	})
}

func labelNotFound(c *gin.Context) {
	c.JSON(http.StatusNotFound, &errs.ApiError{
		Code: "PASSPORT_REALM_LABEL_NOT_FOUND", Message: "Realm label not found.", Status: http.StatusNotFound,
	})
}

func badRequest(c *gin.Context, code, message string) {
	c.JSON(http.StatusBadRequest, errs.New(code, message, http.StatusBadRequest))
}

func internalError(c *gin.Context, log *slog.Logger, err error) {
	if log != nil {
		log.Error("realm request failed", "error", err)
	}
	c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Internal server error.", http.StatusInternalServerError))
}

// loadRealm resolves the :slug parameter, writing the 404 itself.
func loadRealm(c *gin.Context, d Deps) *realm.RealmEntity {
	entity, err := d.Store.GetRealmBySlug(c.Request.Context(), c.Param("slug"))
	if errors.Is(err, realm.ErrNotFound) {
		realmNotFound(c)
		return nil
	}
	if err != nil {
		internalError(c, d.Log, err)
		return nil
	}
	if _, err := d.Service.RefreshBoost(c.Request.Context(), entity); err != nil {
		internalError(c, d.Log, err)
		return nil
	}
	return entity
}

// memberPayload projects membership rows for the wire, hydrating the account
// navigation in one batch and dropping rows whose account is gone (the C#
// listing filtered on m.Account is not null).
func memberPayload(ctx context.Context, d Deps, rows []realm.RealmMemberEntity) ([]*realm.Member, error) {
	if err := realm.HydrateMembers(ctx, d.Accounts, rows); err != nil {
		return nil, err
	}
	out := make([]*realm.Member, 0, len(rows))
	for i := range rows {
		if rows[i].Account == nil {
			continue
		}
		out = append(out, realm.MemberFromEntity(&rows[i]))
	}
	return out, nil
}

// logAction writes an action log with the request's user-agent/ip/session.
func (d Deps) logAction(c *gin.Context, accountID string, action model.ActionLogType, meta map[string]any) {
	if d.Logs == nil || accountID == "" {
		return
	}
	var sessionID *string
	if session := middleware.CurrentSession(c.Request.Context()); session != nil {
		sessionID = &session.Id
	}
	_ = d.Logs.Create(c.Request.Context(), accountID, action, meta, c.Request.UserAgent(),
		middleware.ClientIP(c.Request), nil, sessionID)
}

func setTotal(c *gin.Context, total int64) {
	c.Header("X-Total", strconv.FormatInt(total, 10))
}

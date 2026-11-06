// Package progressionctl mounts Passport's progression surface:
// /api/accounts/me/progression (the account's own states), /api/admin/progression
// (definition CRUD and the seed sync) and /.well-known/badges (the badge
// manifest and icons).
package progressionctl

import (
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/lighthouse/shared/pkg/errs"
	"src.solsynth.dev/sosys/stargate/middleware"
	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/permission"
	"src.solsynth.dev/sosys/stargate/progression"
)

// Deps is the dependency set the progression controllers need.
type Deps struct {
	Service *progression.Service
	Seed    *progression.SeedService
	Store   *progression.Store
	// BadgeIconsPath is the directory /.well-known/badges/icons serves from,
	// the C# [Badges] iconsPath option. Empty serves the SVG set embedded with
	// the module.
	BadgeIconsPath string
	Perm           middleware.PermissionChecker
	Log            *slog.Logger
}

// Register mounts the account routes on the /api group.
func Register(api *gin.RouterGroup, d Deps) {
	me := api.Group("/accounts/me/progression")
	me.Use(middleware.RequireAuth())
	me.GET("/achievements", d.listAchievements)
	me.GET("/achievements/stats", d.achievementStats)
	me.GET("/quests", d.listQuests)
	me.GET("/grants", d.listGrants)
}

// RegisterAdmin mounts the console routes.
func RegisterAdmin(api *gin.RouterGroup, d Deps) {
	admin := api.Group("/admin/progression")
	admin.GET("/achievements", d.adminListAchievements)
	admin.POST("/achievements", middleware.AskPermission(d.Perm, permission.ProgressionAchievementsManage), d.createAchievement)
	admin.PUT("/achievements/:identifier", middleware.AskPermission(d.Perm, permission.ProgressionAchievementsManage), d.updateAchievement)
	admin.POST("/achievements/:identifier/enable", middleware.AskPermission(d.Perm, permission.ProgressionAchievementsManage), d.toggleAchievement)
	admin.GET("/quests", d.adminListQuests)
	admin.POST("/quests", middleware.AskPermission(d.Perm, permission.ProgressionQuestsManage), d.createQuest)
	admin.PUT("/quests/:identifier", middleware.AskPermission(d.Perm, permission.ProgressionQuestsManage), d.updateQuest)
	admin.POST("/quests/:identifier/enable", middleware.AskPermission(d.Perm, permission.ProgressionQuestsManage), d.toggleQuest)
	admin.POST("/sync", middleware.AskPermission(d.Perm, permission.ProgressionSync), d.syncSeeds)
	admin.POST("/test-ws-packet", d.testPacket)
}

// RegisterWellKnown mounts the badge discovery routes on the engine (they are
// not under /api).
func RegisterWellKnown(engine *gin.Engine, d Deps) {
	engine.GET("/.well-known/badges", d.badgeManifest)
	engine.GET("/.well-known/badges/icons/:iconName", d.badgeIcon)
}

// ─────────────────────────── account routes ───────────────────────────

func (d Deps) listAchievements(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	id, ok := accountID(account)
	if !ok {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized("Authentication is required."))
		return
	}
	query := strings.TrimSpace(c.Query("query"))
	var (
		states []progression.AchievementState
		err    error
	)
	if query == "" {
		states, err = d.Service.ListAchievements(c.Request.Context(), id)
	} else {
		states, err = d.Service.SearchAchievements(c.Request.Context(), id, query)
	}
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	c.JSON(http.StatusOK, states)
}

func (d Deps) achievementStats(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	id, ok := accountID(account)
	if !ok {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized("Authentication is required."))
		return
	}
	stats, err := d.Service.AchievementStatistics(c.Request.Context(), id)
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	c.JSON(http.StatusOK, stats)
}

func (d Deps) listQuests(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	id, ok := accountID(account)
	if !ok {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized("Authentication is required."))
		return
	}
	states, err := d.Service.ListQuests(c.Request.Context(), id)
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	c.JSON(http.StatusOK, states)
}

func (d Deps) listGrants(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	id, ok := accountID(account)
	if !ok {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized("Authentication is required."))
		return
	}
	take, _ := strconv.Atoi(c.DefaultQuery("take", "50"))
	grants, err := d.Service.ListGrants(c.Request.Context(), id, take)
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	out := make([]grantView, 0, len(grants))
	for i := range grants {
		out = append(out, grantFromEntity(&grants[i]))
	}
	c.JSON(http.StatusOK, out)
}

// ─────────────────────────── admin routes ───────────────────────────

func (d Deps) adminListAchievements(c *gin.Context) {
	if !d.isAdmin(c) {
		return
	}
	entities, err := d.Store.ListAchievements(c.Request.Context(), true)
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	out := make([]definitionView, 0, len(entities))
	for i := range entities {
		out = append(out, achievementView(&entities[i]))
	}
	c.JSON(http.StatusOK, out)
}

func (d Deps) createAchievement(c *gin.Context) {
	if !d.isAdmin(c) {
		return
	}
	var request progression.UpsertRequest
	if !bindJSON(c, &request) {
		return
	}
	if strings.TrimSpace(request.Identifier) == "" {
		validation(c, "identifier")
		return
	}
	entity := &progression.AchievementDefinitionEntity{ID: uuid.New()}
	// The C# request classes default IsEnabled/IsProgressEnabled to true; an
	// omitted JSON field is indistinguishable from false, so a create without
	// the flags means the same defaults.
	request.IsEnabled = true
	request.IsProgressEnabled = true
	if err := request.ApplyToAchievement(entity); err != nil {
		internalError(c, d.Log, err)
		return
	}
	entity.Normalize()
	if err := d.Store.SaveAchievement(c.Request.Context(), entity); err != nil {
		internalError(c, d.Log, err)
		return
	}
	c.JSON(http.StatusOK, achievementView(entity))
}

func (d Deps) updateAchievement(c *gin.Context) {
	if !d.isAdmin(c) {
		return
	}
	entity, err := d.Store.GetAchievement(c.Request.Context(), c.Param("identifier"))
	if errors.Is(err, progression.ErrNotFound) {
		notFound(c, c.Param("identifier"))
		return
	}
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	var request progression.UpsertRequest
	if !bindJSON(c, &request) {
		return
	}
	if strings.TrimSpace(request.Identifier) == "" {
		request.Identifier = entity.Identifier
	}
	if err := request.ApplyToAchievement(entity); err != nil {
		internalError(c, d.Log, err)
		return
	}
	entity.Normalize()
	if err := d.Store.SaveAchievement(c.Request.Context(), entity); err != nil {
		internalError(c, d.Log, err)
		return
	}
	c.JSON(http.StatusOK, achievementView(entity))
}

func (d Deps) toggleAchievement(c *gin.Context) {
	if !d.isAdmin(c) {
		return
	}
	entity, err := d.Store.ToggleAchievement(c.Request.Context(), c.Param("identifier"), enabledQuery(c))
	if errors.Is(err, progression.ErrNotFound) {
		notFound(c, c.Param("identifier"))
		return
	}
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	c.JSON(http.StatusOK, achievementView(entity))
}

func (d Deps) adminListQuests(c *gin.Context) {
	if !d.isAdmin(c) {
		return
	}
	entities, err := d.Store.ListQuests(c.Request.Context(), true)
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	out := make([]definitionView, 0, len(entities))
	for i := range entities {
		out = append(out, questView(&entities[i]))
	}
	c.JSON(http.StatusOK, out)
}

func (d Deps) createQuest(c *gin.Context) {
	if !d.isAdmin(c) {
		return
	}
	var request progression.UpsertRequest
	if !bindJSON(c, &request) {
		return
	}
	if strings.TrimSpace(request.Identifier) == "" {
		validation(c, "identifier")
		return
	}
	entity := &progression.QuestDefinitionEntity{ID: uuid.New()}
	request.IsEnabled = true
	request.IsProgressEnabled = true
	if err := request.ApplyToQuest(entity); err != nil {
		internalError(c, d.Log, err)
		return
	}
	entity.Normalize()
	if err := d.Store.SaveQuest(c.Request.Context(), entity); err != nil {
		internalError(c, d.Log, err)
		return
	}
	c.JSON(http.StatusOK, questView(entity))
}

func (d Deps) updateQuest(c *gin.Context) {
	if !d.isAdmin(c) {
		return
	}
	entity, err := d.Store.GetQuest(c.Request.Context(), c.Param("identifier"))
	if errors.Is(err, progression.ErrNotFound) {
		notFound(c, c.Param("identifier"))
		return
	}
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	var request progression.UpsertRequest
	if !bindJSON(c, &request) {
		return
	}
	if strings.TrimSpace(request.Identifier) == "" {
		request.Identifier = entity.Identifier
	}
	if err := request.ApplyToQuest(entity); err != nil {
		internalError(c, d.Log, err)
		return
	}
	entity.Normalize()
	if err := d.Store.SaveQuest(c.Request.Context(), entity); err != nil {
		internalError(c, d.Log, err)
		return
	}
	c.JSON(http.StatusOK, questView(entity))
}

func (d Deps) toggleQuest(c *gin.Context) {
	if !d.isAdmin(c) {
		return
	}
	entity, err := d.Store.ToggleQuest(c.Request.Context(), c.Param("identifier"), enabledQuery(c))
	if errors.Is(err, progression.ErrNotFound) {
		notFound(c, c.Param("identifier"))
		return
	}
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	c.JSON(http.StatusOK, questView(entity))
}

func (d Deps) syncSeeds(c *gin.Context) {
	if !d.isAdmin(c) {
		return
	}
	if err := d.Seed.EnsureSeeded(c.Request.Context()); err != nil {
		internalError(c, d.Log, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{})
}

// testPacket mirrors POST /api/admin/progression/test-ws-packet.
func (d Deps) testPacket(c *gin.Context) {
	if !d.isAdmin(c) {
		return
	}
	account := currentAccount(c)
	var targetID uuid.UUID
	if raw := c.Query("accountId"); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			validation(c, "accountId")
			return
		}
		targetID = parsed
	} else if account != nil {
		id, ok := accountID(account)
		if !ok {
			c.JSON(http.StatusUnauthorized, errs.Unauthorized("Authentication is required."))
			return
		}
		targetID = id
	}
	if targetID == uuid.Nil {
		c.JSON(http.StatusBadRequest, errs.New("PASSPORT_PROGRESSION_NO_ACCOUNT",
			"No account specified and no current user.", http.StatusBadRequest))
		return
	}
	kind := c.DefaultQuery("kind", "achievement")
	identifier := c.DefaultQuery("identifier", "test")
	title := c.DefaultQuery("title", "Test Packet")
	if err := d.Service.SendTestPacket(c.Request.Context(), targetID, kind, identifier, title); err != nil {
		if errors.Is(err, progression.ErrUnavailable) {
			c.JSON(http.StatusServiceUnavailable, errs.New("SERVICE_UNAVAILABLE",
				"The realtime transport is not configured.", http.StatusServiceUnavailable))
			return
		}
		internalError(c, d.Log, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{})
}

// ─────────────────────────── badge discovery ───────────────────────────

// badgeManifest mirrors GET /.well-known/badges.
func (d Deps) badgeManifest(c *gin.Context) {
	// The embedded set backs the manifest even when no iconsPath is configured,
	// so icon_url is always a working URL rather than null.
	hasIcons := true
	if d.BadgeIconsPath != "" {
		if info, err := os.Stat(d.BadgeIconsPath); err != nil || !info.IsDir() {
			hasIcons = false
		}
	}
	entries := make([]gin.H, 0, len(badgeEntries))
	for _, entry := range badgeEntries {
		var iconURL *string
		if hasIcons {
			url := "/.well-known/badges/icons/" + entry.Icon
			iconURL = &url
		}
		entries = append(entries, gin.H{
			"identifier": entry.Identifier, "achievement_identifier": entry.Achievement,
			"label": entry.Label, "caption": entry.Caption, "icon": entry.Icon,
			"color": entry.Color, "icon_url": iconURL, "localization_key": entry.LocalizationKey,
			"category": entry.Category, "series": entry.Series, "hidden": entry.Hidden,
		})
	}
	c.Header("Cache-Control", "public, max-age=3600")
	c.JSON(http.StatusOK, gin.H{"version": 1, "badges": entries})
}

// badgeIcon mirrors GET /.well-known/badges/icons/{iconName}, including the
// path containment check the C# controller performed.
func (d Deps) badgeIcon(c *gin.Context) {
	name := c.Param("iconName")
	if d.BadgeIconsPath == "" {
		// No configured directory: fall back to the SVG set embedded with the
		// module, which is the same set Passport shipped next to the service.
		if data, err := fs.ReadFile(progression.BadgeIcons(), name+".svg"); err == nil {
			c.Header("Cache-Control", "public, max-age=86400")
			c.Data(http.StatusOK, "image/svg+xml", data)
			return
		}
		badgeIconNotFound(c)
		return
	}
	root, err := filepath.Abs(d.BadgeIconsPath)
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	full, err := filepath.Abs(filepath.Join(root, c.Param("iconName")+".svg"))
	if err != nil {
		internalError(c, d.Log, err)
		return
	}
	if !strings.HasPrefix(full, root+string(filepath.Separator)) && full != root {
		c.JSON(http.StatusBadRequest, errs.New("PASSPORT_BADGE_ICON_INVALID_PATH",
			"Invalid icon path.", http.StatusBadRequest))
		return
	}
	data, err := os.ReadFile(full)
	if err != nil {
		badgeIconNotFound(c)
		return
	}
	c.Header("Cache-Control", "public, max-age=86400")
	c.Data(http.StatusOK, "image/svg+xml", data)
}

func badgeIconNotFound(c *gin.Context) {
	c.JSON(http.StatusNotFound, errs.New("PASSPORT_BADGE_ICON_NOT_FOUND",
		"Badge icon not found.", http.StatusNotFound))
}

// ─────────────────────────── helpers ───────────────────────────

func currentAccount(c *gin.Context) *model.Account {
	account := middleware.CurrentUser(c.Request.Context())
	if account == nil {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized("Authentication is required."))
		return nil
	}
	return account
}

func accountID(account *model.Account) (uuid.UUID, bool) {
	id, err := uuid.Parse(account.Id)
	return id, err == nil
}

// isAdmin mirrors ProgressionAdminController.IsAdminAsync: a superuser, or an
// account holding the progression.admin node.
func (d Deps) isAdmin(c *gin.Context) bool {
	account := middleware.CurrentUser(c.Request.Context())
	if account == nil {
		c.JSON(http.StatusForbidden, errs.Forbidden("You do not have permission to perform this action."))
		return false
	}
	// The token scope narrows; it never grants. An OAuth app token must cover
	// the key before the permission service decides on the account's node.
	if !middleware.ScopeAllows(c, "progression.admin") {
		c.JSON(http.StatusForbidden, errs.Forbidden("You do not have permission to perform this action."))
		return false
	}
	if account.IsSuperuser {
		return true
	}
	if d.Perm == nil {
		c.JSON(http.StatusForbidden, errs.Forbidden("You do not have permission to perform this action."))
		return false
	}
	id, ok := accountID(account)
	if !ok {
		c.JSON(http.StatusForbidden, errs.Forbidden("You do not have permission to perform this action."))
		return false
	}
	allowed, err := d.Perm.HasPermission(c.Request.Context(), id, "progression.admin")
	if err != nil {
		internalError(c, d.Log, err)
		return false
	}
	if !allowed {
		c.JSON(http.StatusForbidden, errs.Forbidden("You do not have permission to perform this action."))
		return false
	}
	return true
}

func enabledQuery(c *gin.Context) bool {
	raw := strings.TrimSpace(c.DefaultQuery("enabled", "true"))
	value, err := strconv.ParseBool(raw)
	return err == nil && value
}

func internalError(c *gin.Context, log *slog.Logger, err error) {
	if log != nil {
		log.Error("progression request failed", "error", err)
	}
	c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Internal server error.", http.StatusInternalServerError))
}

func notFound(c *gin.Context, identifier string) {
	c.JSON(http.StatusNotFound, &errs.ApiError{
		Code: "NOT_FOUND", Message: "The requested resource '" + identifier + "' was not found.",
		Status: http.StatusNotFound, Detail: &identifier,
	})
}

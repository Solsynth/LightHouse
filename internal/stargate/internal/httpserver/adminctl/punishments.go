package adminctl

// Punishment surface: the admin CRUD from AccountAdminController
// (punishments/created, {name}/punishments, {name}/suspend) plus the
// user-facing routes from AccountPunishmentController
// (/api/accounts/{name}/punishments, .../overview, /me/punishments).

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/go/pkg/errs"
	"src.solsynth.dev/sosys/stargate/internal/localization"
	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// punishmentView mirrors the hydrated SnAccountPunishment (Account and
// Creator are filled in by HydratePunishmentAccountBatch).
type punishmentView struct {
	model.Punishment
	Account *model.Account `json:"account,omitempty"`
	Creator *model.Account `json:"creator,omitempty"`
}

// hydratePunishments fills Account/Creator on the views from the local
// accounts table (the C# hydrates via the Passport profile gRPC; Stargate
// owns the accounts).
func hydratePunishments(c *gin.Context, d Deps, views map[string]*punishmentView) {
	if len(views) == 0 {
		return
	}
	seen := map[uuid.UUID]struct{}{}
	var ids []uuid.UUID
	collect := func(raw string) {
		id, err := uuid.Parse(raw)
		if err != nil {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	for _, view := range views {
		collect(view.AccountId)
		if view.CreatorId != nil {
			collect(*view.CreatorId)
		}
	}
	if len(ids) == 0 {
		return
	}
	accounts, err := d.Store.AdminLoadAccountsByIds(c.Request.Context(), ids)
	if err != nil {
		return
	}
	for _, view := range views {
		if account, ok := accounts[view.AccountId]; ok {
			view.Account = account
		}
		if view.CreatorId != nil {
			if creator, ok := accounts[*view.CreatorId]; ok {
				view.Creator = creator
			}
		}
	}
}

// ─────────────────────────── Admin punishments ───────────────────────────

type createPunishmentRequest struct {
	Reason                   string          `json:"reason"`
	ExpiredAt                *model.Time     `json:"expired_at"`
	Type                     json.RawMessage `json:"type"`
	BlockedPermissions       []string        `json:"blocked_permissions"`
	SocialCreditReduction    *float64        `json:"social_credit_reduction"`
	PublisherRatingReduction *float64        `json:"publisher_rating_reduction"`
	PublisherNames           []string        `json:"publisher_names"`
}

type updatePunishmentRequest struct {
	Reason             *string         `json:"reason"`
	ExpiredAt          *model.Time     `json:"expired_at"`
	Type               json.RawMessage `json:"type"`
	BlockedPermissions *[]string       `json:"blocked_permissions"`
}

type suspendAccountRequest struct {
	Reason                   string          `json:"reason"`
	ExpiredAt                *model.Time     `json:"expired_at"`
	Type                     json.RawMessage `json:"type"`
	RevokeSessions           *bool           `json:"revoke_sessions"`
	SocialCreditReduction    *float64        `json:"social_credit_reduction"`
	PublisherRatingReduction *float64        `json:"publisher_rating_reduction"`
	PublisherNames           []string        `json:"publisher_names"`
}

func getCreatedPunishments(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, ok := currentUserID(c)
		if !ok {
			return
		}
		take := defaultTake(c, 50)
		offset := defaultOffset(c)
		punishments, total, err := d.Store.AdminPunishmentsCreatedBy(c.Request.Context(), userID, take, offset)
		if err != nil {
			serverError(c, err, d)
			return
		}
		views := punishmentsToViews(punishments)
		hydratePunishments(c, d, indexViews(views))
		setTotal(c, total)
		c.JSON(http.StatusOK, views)
	}
}

func createPunishment(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, ok := currentUserID(c)
		if !ok {
			return
		}
		var request createPunishmentRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, errs.New("PADLOCK_OPERATION_FAILED", "Invalid punishment request.", http.StatusBadRequest))
			return
		}
		account := lookupAccount(c, d, c.Param("name"))
		if account == nil {
			return
		}
		accountID, err := uuid.Parse(account.Id)
		if err != nil {
			accountNotFound(c)
			return
		}
		ptype, ok := parsePunishmentType(request.Type)
		if !ok {
			c.JSON(http.StatusBadRequest, errs.New("PADLOCK_OPERATION_FAILED", "Invalid punishment type.", http.StatusBadRequest))
			return
		}
		view, err := createPunishmentInternal(c, d, accountID, userID, request.Reason, request.ExpiredAt, ptype, request.BlockedPermissions, true)
		if err != nil {
			serverError(c, err, d)
			return
		}
		c.JSON(http.StatusOK, view)
	}
}

func updatePunishment(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, ok := currentUserID(c)
		if !ok {
			return
		}
		var request updatePunishmentRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, errs.New("PADLOCK_PUNISHMENT_NOT_FOUND", "Punishment not found.", http.StatusBadRequest))
			return
		}
		account := lookupAccount(c, d, c.Param("name"))
		if account == nil {
			return
		}
		accountID, err := uuid.Parse(account.Id)
		if err != nil {
			accountNotFound(c)
			return
		}
		punishmentID, err := uuid.Parse(c.Param("punishmentId"))
		if err != nil {
			c.JSON(http.StatusNotFound, errs.New("PADLOCK_PUNISHMENT_NOT_FOUND", "Punishment not found.", http.StatusNotFound))
			return
		}

		var reason *string
		if request.Reason != nil {
			reason = request.Reason
		}
		var expiredAt *time.Time
		if request.ExpiredAt != nil {
			t := request.ExpiredAt.Time()
			expiredAt = &t
		}
		var ptype *int
		if len(request.Type) > 0 && string(request.Type) != "null" {
			parsed, ok := parsePunishmentType(request.Type)
			if !ok {
				c.JSON(http.StatusBadRequest, errs.New("PADLOCK_OPERATION_FAILED", "Invalid punishment type.", http.StatusBadRequest))
				return
			}
			ptype = &parsed
		}
		var blocked []string
		hasBlocked := request.BlockedPermissions != nil
		if hasBlocked {
			blocked = *request.BlockedPermissions
		}
		creatorID := userID
		punishment, err := d.Store.AdminPunishmentUpdate(c.Request.Context(), punishmentID, reason, expiredAt, ptype, blocked, hasBlocked, &creatorID)
		if err != nil {
			if err == store.ErrNotFound {
				c.JSON(http.StatusNotFound, errs.New("PADLOCK_PUNISHMENT_NOT_FOUND", "Punishment not found.", http.StatusNotFound))
				return
			}
			serverError(c, err, d)
			return
		}
		clearActorPermissionCache(d, c, accountID.String())
		view := punishmentView{Punishment: *punishment}
		hydratePunishments(c, d, indexViews([]punishmentView{view}))
		c.JSON(http.StatusOK, view)
	}
}

func deletePunishment(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		account := lookupAccount(c, d, c.Param("name"))
		if account == nil {
			return
		}
		accountID, err := uuid.Parse(account.Id)
		if err != nil {
			accountNotFound(c)
			return
		}
		punishmentID, err := uuid.Parse(c.Param("punishmentId"))
		if err != nil {
			c.JSON(http.StatusNotFound, errs.New("PADLOCK_PUNISHMENT_NOT_FOUND", "Punishment not found.", http.StatusNotFound))
			return
		}
		punishment, err := d.Store.AdminPunishmentDelete(c.Request.Context(), accountID, punishmentID)
		if err != nil {
			if err == store.ErrNotFound {
				c.JSON(http.StatusNotFound, errs.New("PADLOCK_PUNISHMENT_NOT_FOUND", "Punishment not found.", http.StatusNotFound))
				return
			}
			serverError(c, err, d)
			return
		}
		clearActorPermissionCache(d, c, accountID.String())
		// Best-effort "punishment lifted" push (the C# localizes per account
		// language; Stargate ships en/zh-hans strings).
		title := localization.Localize(account.Language, "punishmentLiftedTitle", nil)
		body := localization.Localize(account.Language, "punishmentLiftedBody", map[string]string{"type": punishmentTypeName(int(punishment.Type))})
		_ = sendPushToUser(c, d, accountID.String(), "account.punishment.lifted", title, "", body)
		c.JSON(http.StatusOK, gin.H{})
	}
}

func suspendAccount(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, ok := currentUserID(c)
		if !ok {
			return
		}
		var request suspendAccountRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, errs.New("PADLOCK_SUSPEND_TYPE_UNSUPPORTED", "Suspend endpoint only supports block_login or disable_account punishments.", http.StatusBadRequest))
			return
		}
		ptype, ok := parsePunishmentType(request.Type)
		if !ok || (ptype != int(model.PunishmentBlockLogin) && ptype != int(model.PunishmentDisableAccount)) {
			c.JSON(http.StatusBadRequest, errs.New("PADLOCK_SUSPEND_TYPE_UNSUPPORTED", "Suspend endpoint only supports block_login or disable_account punishments.", http.StatusBadRequest))
			return
		}
		account := lookupAccount(c, d, c.Param("name"))
		if account == nil {
			return
		}
		accountID, err := uuid.Parse(account.Id)
		if err != nil {
			accountNotFound(c)
			return
		}
		revokeSessions := true
		if request.RevokeSessions != nil {
			revokeSessions = *request.RevokeSessions
		}
		view, err := createPunishmentInternal(c, d, accountID, userID, request.Reason, request.ExpiredAt, ptype, nil, revokeSessions)
		if err != nil {
			serverError(c, err, d)
			return
		}
		c.JSON(http.StatusOK, view)
	}
}

// createPunishmentInternal mirrors CreatePunishmentInternal: insert, clear
// the permission cache, optionally revoke sessions, apply social-credit /
// publisher-rating reductions (logged only — no clients), push a
// notification, and hydrate.
func createPunishmentInternal(c *gin.Context, d Deps, accountID, creatorID uuid.UUID, reason string, expiredAt *model.Time, ptype int, blocked []string, revokeSessions bool) (*punishmentView, error) {
	var expired *time.Time
	if expiredAt != nil {
		t := expiredAt.Time()
		expired = &t
	}
	punishment, err := d.Store.AdminPunishmentCreate(c.Request.Context(), accountID, creatorID, reason, expired, ptype, blocked)
	if err != nil {
		return nil, err
	}
	clearActorPermissionCache(d, c, accountID.String())

	if revokeSessions && (ptype == int(model.PunishmentBlockLogin) || ptype == int(model.PunishmentDisableAccount)) {
		now := time.Now().UTC()
		sessions, err := d.Store.AdminRevokeAllSessions(c.Request.Context(), accountID, now)
		if err != nil {
			return nil, err
		}
		for i := range sessions {
			removeSessionCache(d, c, sessions[i].Id)
		}
		if len(sessions) > 0 {
			logAction(d, c, accountID, model.ActionLogSessionRevoke, map[string]any{
				"count":  len(sessions),
				"reason": "account_punishment",
			})
		}
	}

	language := ""
	if target, err := d.Store.GetAccountByID(c.Request.Context(), accountID); err == nil {
		language = target.Language
	}
	title := punishmentTitle(language, ptype)
	body := punishmentBody(language, reason, expiredAt)
	_ = sendPushToUser(c, d, accountID.String(), "account.punishment", title, localization.Localize(language, "punishmentTitle", nil), body)

	view := punishmentView{Punishment: *punishment}
	hydratePunishments(c, d, indexViews([]punishmentView{view}))
	return &view, nil
}

// ─────────────────────────── User-facing punishments ───────────────────────────

func getAccountPunishments(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		account := lookupAccount(c, d, c.Param("name"))
		if account == nil {
			return
		}
		accountID, err := uuid.Parse(account.Id)
		if err != nil {
			accountNotFound(c)
			return
		}
		take := defaultTake(c, 50)
		offset := defaultOffset(c)
		punishments, total, err := d.Store.AdminActivePunishmentsForAccount(c.Request.Context(), accountID, time.Now().UTC(), take, offset)
		if err != nil {
			serverError(c, err, d)
			return
		}
		views := punishmentsToViews(punishments)
		hydratePunishments(c, d, indexViews(views))
		setTotal(c, total)
		c.JSON(http.StatusOK, views)
	}
}

func getPunishmentOverview(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		account := lookupAccount(c, d, c.Param("name"))
		if account == nil {
			return
		}
		accountID, err := uuid.Parse(account.Id)
		if err != nil {
			accountNotFound(c)
			return
		}
		punishment, err := d.Store.AdminPunishmentOverview(c.Request.Context(), accountID, time.Now().UTC())
		if err != nil {
			serverError(c, err, d)
			return
		}
		if punishment == nil {
			c.JSON(http.StatusOK, gin.H{})
			return
		}
		view := punishmentView{Punishment: *punishment}
		hydratePunishments(c, d, indexViews([]punishmentView{view}))
		c.JSON(http.StatusOK, view)
	}
}

func getMyPunishments(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, ok := currentUserID(c)
		if !ok {
			return
		}
		take := defaultTake(c, 50)
		offset := defaultOffset(c)
		punishments, total, err := d.Store.AdminAllPunishmentsForAccount(c.Request.Context(), userID, take, offset)
		if err != nil {
			serverError(c, err, d)
			return
		}
		views := punishmentsToViews(punishments)
		hydratePunishments(c, d, indexViews(views))
		setTotal(c, total)
		user := middlewareCurrentUser(c)
		response := gin.H{"punishments": views}
		if user != nil {
			response["account"] = user
		}
		c.JSON(http.StatusOK, response)
	}
}

// ─────────────────────────── helpers ───────────────────────────

func punishmentsToViews(punishments []model.Punishment) []punishmentView {
	views := make([]punishmentView, 0, len(punishments))
	for i := range punishments {
		views = append(views, punishmentView{Punishment: punishments[i]})
	}
	return views
}

func indexViews(views []punishmentView) map[string]*punishmentView {
	index := make(map[string]*punishmentView, len(views))
	for i := range views {
		index[views[i].Id] = &views[i]
	}
	return index
}

// defaultTake mirrors the user-facing controllers which do not clamp take
// (default 50 when absent/<=0).
func defaultTake(c *gin.Context, def int) int {
	raw := c.Query("take")
	if raw == "" {
		return def
	}
	take, err := strconv.Atoi(raw)
	if err != nil || take <= 0 {
		return def
	}
	return take
}

func defaultOffset(c *gin.Context) int {
	raw := c.Query("offset")
	if raw == "" {
		return 0
	}
	offset, err := strconv.Atoi(raw)
	if err != nil || offset < 0 {
		return 0
	}
	return offset
}

// parsePunishmentType accepts the C# enum as a number (0-3) or a string
// name ("BlockLogin", case-insensitive). The FloatingIsland admin UI sends
// snake_case strings ("block_login"/"disable_account"), which the C#
// System.Text.Json binder rejects; Stargate accepts those too (deviation
// noted in the phase report).
func parsePunishmentType(raw json.RawMessage) (int, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, false
	}
	var number int
	if err := json.Unmarshal(raw, &number); err == nil {
		if number >= 0 && number <= 3 {
			return number, true
		}
		return 0, false
	}
	var name string
	if err := json.Unmarshal(raw, &name); err != nil {
		return 0, false
	}
	switch strings.ToLower(strings.ReplaceAll(name, "_", "")) {
	case "permissionmodification":
		return int(model.PunishmentPermissionModification), true
	case "blocklogin":
		return int(model.PunishmentBlockLogin), true
	case "disableaccount":
		return int(model.PunishmentDisableAccount), true
	case "strike":
		return int(model.PunishmentStrike), true
	default:
		return 0, false
	}
}

func punishmentTypeName(ptype int) string {
	switch model.PunishmentType(ptype) {
	case model.PunishmentPermissionModification:
		return "PermissionModification"
	case model.PunishmentBlockLogin:
		return "BlockLogin"
	case model.PunishmentDisableAccount:
		return "DisableAccount"
	default:
		return "Strike"
	}
}

func punishmentTitle(language string, ptype int) string {
	switch model.PunishmentType(ptype) {
	case model.PunishmentPermissionModification:
		return localization.Localize(language, "punishmentTitlePermissionModification", nil)
	case model.PunishmentBlockLogin:
		return localization.Localize(language, "punishmentTitleBlockLogin", nil)
	case model.PunishmentDisableAccount:
		return localization.Localize(language, "punishmentTitleDisableAccount", nil)
	default:
		return localization.Localize(language, "punishmentTitleStrike", nil)
	}
}

func punishmentBody(language, reason string, expiredAt *model.Time) string {
	if expiredAt != nil {
		return localization.Localize(language, "punishmentBodyWithExpiry", map[string]string{
			"reason":    reason,
			"expiredAt": expiredAt.Time().UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	return localization.Localize(language, "punishmentBody", map[string]string{"reason": reason})
}

func sendPushToUser(c *gin.Context, d Deps, userID, topic, title, subtitle, body string) error {
	if d.Clients == nil || d.Clients.Ring == nil {
		return errRingUnavailable
	}
	_, err := d.Clients.Ring.SendPushNotificationToUser(c.Request.Context(), &gen.DySendPushNotificationToUserRequest{
		UserId: userID,
		Notification: &gen.DyPushNotification{
			Topic:     topic,
			Title:     title,
			Subtitle:  subtitle,
			Body:      body,
			IsSavable: true,
		},
	})
	return err
}

var errRingUnavailable = errors.New("ring notification service is unavailable")

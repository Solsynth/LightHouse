package adminctl

// Port of Padlock's AccountAdminController (admin account management:
// list/detail, devices, sessions, contacts, auth factors, notifications,
// emails + CSV export, suspend/activate/delete). Route paths, DTO field
// names, error codes/messages and [AskPermission] keys match the C# file.

import (
	"encoding/base32"
	"encoding/csv"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/stargate/internal/auth"
	"src.solsynth.dev/sosys/go/pkg/errs"
	"src.solsynth.dev/sosys/stargate/internal/middleware"
	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/permission"
	"src.solsynth.dev/sosys/stargate/internal/spell"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// ─────────────────────────── Wire DTOs ───────────────────────────

// accountAuthFactorSummary mirrors AccountAdminController.AccountAuthFactorSummary.
type accountAuthFactorSummary struct {
	Id          string         `json:"id"`
	Type        int            `json:"type"`
	Trustworthy int            `json:"trustworthy"`
	HasSecret   bool           `json:"has_secret"`
	Config      map[string]any `json:"config,omitempty"`
	EnabledAt   *model.Time    `json:"enabled_at,omitempty"`
	ExpiredAt   *model.Time    `json:"expired_at,omitempty"`
	CreatedAt   *model.Time    `json:"created_at,omitempty"`
	UpdatedAt   *model.Time    `json:"updated_at,omitempty"`
}

// adminAccountSummaryResponse mirrors AdminAccountSummaryResponse.
type adminAccountSummaryResponse struct {
	Account            *model.Account  `json:"account"`
	PrimaryEmail       *string         `json:"primary_email,omitempty"`
	ContactCount       int             `json:"contact_count"`
	AuthFactorCount    int             `json:"auth_factor_count"`
	HasPassword        bool            `json:"has_password"`
	ActiveSessionCount int             `json:"active_session_count"`
	ActiveDeviceCount  int             `json:"active_device_count"`
	ActivePunishment   *punishmentView `json:"active_punishment,omitempty"`
}

// adminAccountDetailResponse mirrors AdminAccountDetailResponse.
type adminAccountDetailResponse struct {
	Account            *model.Account             `json:"account"`
	Contacts           []model.Contact            `json:"contacts"`
	AuthFactors        []accountAuthFactorSummary `json:"auth_factors"`
	ActiveSessionCount int                        `json:"active_session_count"`
	ActiveDeviceCount  int                        `json:"active_device_count"`
	ActivePunishment   *punishmentView            `json:"active_punishment,omitempty"`
	ActivePunishments  []punishmentView           `json:"active_punishments"`
}

// adminMessageDispatchResponse mirrors AdminMessageDispatchResponse.
type adminMessageDispatchResponse struct {
	Requested      int  `json:"requested"`
	Resolved       int  `json:"resolved"`
	Sent           int  `json:"sent"`
	Skipped        int  `json:"skipped"`
	BroadcastToAll bool `json:"broadcast_to_all"`
}

type adminNotificationRequest struct {
	AccountId      *uuid.UUID     `json:"account_id"`
	AccountIds     []uuid.UUID    `json:"account_ids"`
	BroadcastToAll bool           `json:"broadcast_to_all"`
	Topic          string         `json:"topic"`
	Title          *string        `json:"title"`
	Subtitle       *string        `json:"subtitle"`
	Body           *string        `json:"body"`
	ActionUri      *string        `json:"action_uri"`
	PushType       *string        `json:"push_type"`
	IsSilent       bool           `json:"is_silent"`
	IsSavable      bool           `json:"is_savable"`
	Meta           map[string]any `json:"meta"`
}

type adminEmailRequest struct {
	AccountId      *uuid.UUID  `json:"account_id"`
	AccountIds     []uuid.UUID `json:"account_ids"`
	BroadcastToAll bool        `json:"broadcast_to_all"`
	Subject        string      `json:"subject"`
	HtmlBody       string      `json:"html_body"`
}

type adminContactRequest struct {
	Type    int    `json:"type"`
	Content string `json:"content"`
}

type updateAdminContactRequest struct {
	Type    *int    `json:"type"`
	Content *string `json:"content"`
}

type setAdminContactVisibilityRequest struct {
	IsPublic bool `json:"is_public"`
}

type adminContactVerificationRequest struct {
	VerifiedAt *model.Time `json:"verified_at"`
}

type adminAccountAuthFactorRequest struct {
	Type   int     `json:"type"`
	Secret *string `json:"secret"`
	Enable *bool   `json:"enable"`
	Code   *string `json:"code"`
}

type adminResetPasswordFactorRequest struct {
	NewPassword    string `json:"new_password"`
	RevokeSessions bool   `json:"revoke_sessions"`
}

type updateAdminDeviceLabelRequest struct {
	Label string `json:"label"`
}

// registerAccountAdmin mounts the /api/admin/accounts route family.
func registerAccountAdmin(g *gin.RouterGroup, d Deps) {
	g.GET("", requirePerm(d, permission.AccountsView), listAccounts(d))
	g.GET("emails/export", requirePerm(d, permission.EmailsSend), exportEmailContactsCsv(d))
	g.GET("punishments/created", requirePerm(d, permission.PunishmentsView), getCreatedPunishments(d))
	g.POST("notifications", requirePerm(d, permission.NotificationsSend), sendNotification(d))
	g.POST("emails", requirePerm(d, permission.EmailsSend), sendEmails(d))

	g.GET(":name", requirePerm(d, permission.AccountsView), getAccount(d))
	g.DELETE(":name", requirePerm(d, permission.AccountsDeletion), adminDeleteAccount(d))
	g.POST(":name/activate", requirePerm(d, permission.AccountsManage), activateAccount(d))
	g.POST(":name/sessions/revoke", requirePerm(d, permission.AccountsManage), revokeAllSessions(d))
	g.POST(":name/suspend", requirePerm(d, permission.PunishmentsCreate), suspendAccount(d))
	g.POST(":name/punishments", requirePerm(d, permission.PunishmentsCreate), createPunishment(d))
	g.PATCH(":name/punishments/:punishmentId", requirePerm(d, permission.PunishmentsUpdate), updatePunishment(d))
	g.DELETE(":name/punishments/:punishmentId", requirePerm(d, permission.PunishmentsDelete), deletePunishment(d))

	g.GET(":name/devices", requirePerm(d, permission.AccountsView), listAccountDevices(d))
	g.PATCH(":name/devices/:deviceId/label", requirePerm(d, permission.AccountDevicesManage), updateAccountDeviceLabel(d))
	g.POST(":name/devices/:deviceId/sessions/revoke", requirePerm(d, permission.AuthSessionsManage), revokeAccountDeviceSessions(d))
	g.DELETE(":name/devices/:deviceId", requirePerm(d, permission.AccountDevicesManage), deleteAccountDevice(d))

	g.GET(":name/sessions", requirePerm(d, permission.AccountsView), listAccountSessions(d))
	g.GET(":name/sessions/:sessionId/children", requirePerm(d, permission.AccountsView), listAccountSessionChildren(d))
	g.DELETE(":name/sessions/:sessionId", requirePerm(d, permission.AuthSessionsManage), revokeAccountSession(d))

	g.GET(":name/contacts", requirePerm(d, permission.AccountsView), listAccountContacts(d))
	g.POST(":name/contacts", requirePerm(d, permission.AccountContactsManage), createAccountContact(d))
	g.PATCH(":name/contacts/:contactId", requirePerm(d, permission.AccountContactsManage), updateAccountContact(d))
	g.POST(":name/contacts/:contactId/verify/request", requirePerm(d, permission.AccountContactsManage), requestAccountContactVerification(d))
	g.POST(":name/contacts/:contactId/verify", requirePerm(d, permission.AccountContactsManage), verifyAccountContact(d))
	g.DELETE(":name/contacts/:contactId/verify", requirePerm(d, permission.AccountContactsManage), unverifyAccountContact(d))
	g.POST(":name/contacts/:contactId/primary", requirePerm(d, permission.AccountContactsManage), setPrimaryAccountContact(d))
	g.POST(":name/contacts/:contactId/visibility", requirePerm(d, permission.AccountContactsManage), setAccountContactVisibility(d))
	g.DELETE(":name/contacts/:contactId", requirePerm(d, permission.AccountContactsManage), deleteAccountContact(d))

	g.GET(":name/spells", requirePerm(d, permission.AccountsView), listAccountMagicSpells(d))
	g.POST(":name/spells", requirePerm(d, permission.AccountsManage), createAccountMagicSpell(d))
	g.POST(":name/spells/:spellId/resend", requirePerm(d, permission.AccountsManage), resendAccountMagicSpell(d))
	g.DELETE(":name/spells/:spellId", requirePerm(d, permission.AccountsManage), deleteAccountMagicSpell(d))

	g.GET(":name/factors", requirePerm(d, permission.AccountsView), listAccountAuthFactors(d))
	g.POST(":name/factors", requirePerm(d, permission.AuthFactorsManage), createAccountAuthFactor(d))
	g.POST(":name/factors/:factorId/enable", requirePerm(d, permission.AuthFactorsManage), enableAccountAuthFactor(d))
	g.POST(":name/factors/:factorId/disable", requirePerm(d, permission.AuthFactorsManage), disableAccountAuthFactor(d))
	g.POST(":name/factors/password/reset", requirePerm(d, permission.AuthFactorsManage), resetAccountPasswordFactor(d))
	g.DELETE(":name/factors/:factorId", requirePerm(d, permission.AuthFactorsManage), deleteAccountAuthFactor(d))
}

// ─────────────────────────── Account list / detail ───────────────────────────

func listAccounts(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		take := queryTake(c, 50)
		offset := queryOffset(c)
		query := c.Query("query")
		orderBy := c.Query("orderBy")

		accounts, total, err := d.Store.AdminListAccounts(c.Request.Context(), query, orderBy, take, offset)
		if err != nil {
			serverError(c, err, d)
			return
		}
		if len(accounts) == 0 {
			setTotal(c, total)
			c.JSON(http.StatusOK, []adminAccountSummaryResponse{})
			return
		}

		ids := make([]uuid.UUID, 0, len(accounts))
		for i := range accounts {
			id, err := uuid.Parse(accounts[i].Id)
			if err != nil {
				continue
			}
			ids = append(ids, id)
		}

		profiles, err := d.Store.AdminLoadProfiles(c.Request.Context(), ids)
		if err != nil {
			serverError(c, err, d)
			return
		}
		emails, err := d.Store.AdminContactSummaries(c.Request.Context(), ids)
		if err != nil {
			serverError(c, err, d)
			return
		}
		factors, err := d.Store.AdminFactorSummaries(c.Request.Context(), ids)
		if err != nil {
			serverError(c, err, d)
			return
		}
		now := time.Now().UTC()
		sessionCounts, err := d.Store.AdminActiveSessionCounts(c.Request.Context(), ids, now)
		if err != nil {
			serverError(c, err, d)
			return
		}
		deviceCounts, err := d.Store.AdminActiveDeviceCounts(c.Request.Context(), ids)
		if err != nil {
			serverError(c, err, d)
			return
		}
		activePunishments, err := d.Store.AdminListActivePunishments(c.Request.Context(), ids, now)
		if err != nil {
			serverError(c, err, d)
			return
		}
		byAccount := map[string][]model.Punishment{}
		for i := range activePunishments {
			byAccount[activePunishments[i].AccountId] = append(byAccount[activePunishments[i].AccountId], activePunishments[i])
		}
		punishmentLookup := map[string]*punishmentView{}
		for accountID, list := range byAccount {
			idx := store.SelectMostSeverePunishment(list)
			punishmentLookup[accountID] = &punishmentView{Punishment: list[idx]}
		}
		hydratePunishments(c, d, punishmentLookup)

		response := make([]adminAccountSummaryResponse, 0, len(accounts))
		for i := range accounts {
			account := &accounts[i]
			if profile, ok := profiles[account.Id]; ok {
				account.Profile = profile
			}
			email := emails[account.Id]
			factorInfo := factors[account.Id]
			entry := adminAccountSummaryResponse{
				Account:            account,
				PrimaryEmail:       email.PrimaryEmail,
				ContactCount:       email.Count,
				AuthFactorCount:    factorInfo.Count,
				HasPassword:        factorInfo.HasPassword,
				ActiveSessionCount: sessionCounts[account.Id],
				ActiveDeviceCount:  deviceCounts[account.Id],
				ActivePunishment:   punishmentLookup[account.Id],
			}
			response = append(response, entry)
		}
		setTotal(c, total)
		c.JSON(http.StatusOK, response)
	}
}

func getAccount(d Deps) gin.HandlerFunc {
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

		withProfile, err := d.Store.GetAccountWithProfile(c.Request.Context(), accountID)
		if err != nil && err != store.ErrNotFound {
			serverError(c, err, d)
			return
		}
		if withProfile != nil {
			account = withProfile
		}
		contacts, err := d.Store.AdminListContacts(c.Request.Context(), accountID)
		if err != nil {
			serverError(c, err, d)
			return
		}
		factors, err := d.Store.AdminListAuthFactors(c.Request.Context(), accountID)
		if err != nil {
			serverError(c, err, d)
			return
		}
		now := time.Now().UTC()
		sessionCounts, err := d.Store.AdminActiveSessionCounts(c.Request.Context(), []uuid.UUID{accountID}, now)
		if err != nil {
			serverError(c, err, d)
			return
		}
		deviceCounts, err := d.Store.AdminActiveDeviceCounts(c.Request.Context(), []uuid.UUID{accountID})
		if err != nil {
			serverError(c, err, d)
			return
		}
		activePunishments, err := d.Store.AdminListActivePunishments(c.Request.Context(), []uuid.UUID{accountID}, now)
		if err != nil {
			serverError(c, err, d)
			return
		}
		sortPunishmentsNewestFirst(activePunishments)
		views := make([]punishmentView, 0, len(activePunishments))
		lookup := map[string]*punishmentView{}
		for i := range activePunishments {
			views = append(views, punishmentView{Punishment: activePunishments[i]})
			lookup[activePunishments[i].Id] = &views[len(views)-1]
		}
		hydratePunishments(c, d, lookup)

		summary := make([]accountAuthFactorSummary, 0, len(factors))
		for i := range factors {
			summary = append(summary, toAuthFactorSummary(&factors[i]))
		}

		var activePunishment *punishmentView
		if len(activePunishments) > 0 {
			idx := store.SelectMostSeverePunishment(activePunishments)
			activePunishment = lookup[activePunishments[idx].Id]
		}
		account.Contacts = contacts
		c.JSON(http.StatusOK, adminAccountDetailResponse{
			Account:            account,
			Contacts:           contacts,
			AuthFactors:        summary,
			ActiveSessionCount: sessionCounts[accountID.String()],
			ActiveDeviceCount:  deviceCounts[accountID.String()],
			ActivePunishment:   activePunishment,
			ActivePunishments:  views,
		})
	}
}

// ─────────────────────────── Devices ───────────────────────────

func listAccountDevices(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		take := queryTake(c, 20)
		offset := queryOffset(c)
		includeDeleted := c.Query("includeDeleted") == "true"
		includeSessions := c.DefaultQuery("includeSessions", "true") != "false"

		account := lookupAccount(c, d, c.Param("name"))
		if account == nil {
			return
		}
		accountID, _ := uuid.Parse(account.Id)
		devices, total, err := d.Store.AdminListDevices(c.Request.Context(), accountID, includeDeleted, take, offset)
		if err != nil {
			serverError(c, err, d)
			return
		}
		setTotal(c, total)
		if !includeSessions || len(devices) == 0 {
			c.JSON(http.StatusOK, devices)
			return
		}
		clientIDs := make([]uuid.UUID, 0, len(devices))
		for i := range devices {
			id, err := uuid.Parse(devices[i].Id)
			if err != nil {
				continue
			}
			clientIDs = append(clientIDs, id)
		}
		sessionsByClient, err := d.Store.AdminListDeviceSessions(c.Request.Context(), clientIDs)
		if err != nil {
			serverError(c, err, d)
			return
		}
		response := make([]model.AuthClientWithSessions, 0, len(devices))
		for i := range devices {
			device := devices[i]
			item := model.AuthClientWithSessions{AuthClient: device}
			item.Sessions = sessionsByClient[device.Id]
			response = append(response, item)
		}
		c.JSON(http.StatusOK, response)
	}
}

func updateAccountDeviceLabel(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request updateAdminDeviceLabelRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, errs.New("PADLOCK_DEVICE_LABEL_REQUIRED", "Label is required.", http.StatusBadRequest))
			return
		}
		if strings.TrimSpace(request.Label) == "" {
			c.JSON(http.StatusBadRequest, errs.New("PADLOCK_DEVICE_LABEL_REQUIRED", "Label is required.", http.StatusBadRequest))
			return
		}
		account := lookupAccount(c, d, c.Param("name"))
		if account == nil {
			return
		}
		accountID, _ := uuid.Parse(account.Id)
		if err := d.Store.AdminUpdateDeviceLabel(c.Request.Context(), accountID, c.Param("deviceId"), strings.TrimSpace(request.Label)); err != nil {
			if err == store.ErrNotFound {
				accountNotFound(c)
				return
			}
			serverError(c, err, d)
			return
		}
		logAction(d, c, accountID, model.ActionLogDeviceRename, map[string]any{
			"device_id": c.Param("deviceId"),
			"label":     strings.TrimSpace(request.Label),
		})
		c.Status(http.StatusNoContent)
	}
}

func revokeAccountDeviceSessions(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		account := lookupAccount(c, d, c.Param("name"))
		if account == nil {
			return
		}
		accountID, _ := uuid.Parse(account.Id)
		if err := deleteDeviceFlow(c, d, accountID, c.Param("deviceId")); err != nil {
			return
		}
		c.JSON(http.StatusOK, gin.H{})
	}
}

func deleteAccountDevice(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		account := lookupAccount(c, d, c.Param("name"))
		if account == nil {
			return
		}
		accountID, _ := uuid.Parse(account.Id)
		if err := deleteDeviceFlow(c, d, accountID, c.Param("deviceId")); err != nil {
			return
		}
		c.Status(http.StatusNoContent)
	}
}

// deleteDeviceFlow mirrors AccountService.DeleteDevice: expire the device's
// sessions and soft-delete the auth client, with the admin action log.
func deleteDeviceFlow(c *gin.Context, d Deps, accountID uuid.UUID, deviceID string) error {
	now := time.Now().UTC()
	if _, err := d.Store.AdminDeleteDevice(c.Request.Context(), accountID, deviceID, now); err != nil {
		if err == store.ErrNotFound {
			accountNotFound(c)
			return err
		}
		serverError(c, err, d)
		return err
	}
	logAction(d, c, accountID, model.ActionLogDeviceRevoke, map[string]any{
		"device_id": deviceID,
	})
	return nil
}

// ─────────────────────────── Sessions ───────────────────────────

func listAccountSessions(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		take := queryTake(c, 20)
		offset := queryOffset(c)
		includeChildren := c.Query("includeChildren") == "true"
		activeOnly := c.Query("activeOnly") == "true"

		account := lookupAccount(c, d, c.Param("name"))
		if account == nil {
			return
		}
		accountID, _ := uuid.Parse(account.Id)

		var typ *int
		if raw := c.Query("type"); raw != "" {
			if parsed, err := strconv.Atoi(raw); err == nil {
				typ = &parsed
			}
		}
		var clientID *uuid.UUID
		if raw := c.Query("clientId"); raw != "" {
			if parsed, err := uuid.Parse(raw); err == nil {
				clientID = &parsed
			}
		}

		sessions, total, err := d.Store.AdminListSessions(c.Request.Context(), accountID, typ, clientID, includeChildren, activeOnly, take, offset)
		if err != nil {
			serverError(c, err, d)
			return
		}
		attachChildrenCounts(c, d, sessions)
		setTotal(c, total)
		c.JSON(http.StatusOK, sessions)
	}
}

func listAccountSessionChildren(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		take := queryTake(c, 20)
		offset := queryOffset(c)

		account := lookupAccount(c, d, c.Param("name"))
		if account == nil {
			return
		}
		accountID, _ := uuid.Parse(account.Id)
		parentID, err := uuid.Parse(c.Param("sessionId"))
		if err != nil {
			accountNotFound(c)
			return
		}
		if _, err := d.Store.AdminGetSession(c.Request.Context(), accountID, parentID); err != nil {
			if err == store.ErrNotFound {
				accountNotFound(c)
				return
			}
			serverError(c, err, d)
			return
		}
		sessions, total, err := d.Store.AdminListSessionChildren(c.Request.Context(), accountID, parentID, take, offset)
		if err != nil {
			serverError(c, err, d)
			return
		}
		attachChildrenCounts(c, d, sessions)
		setTotal(c, total)
		c.JSON(http.StatusOK, sessions)
	}
}

func attachChildrenCounts(c *gin.Context, d Deps, sessions []model.AuthSession) {
	if len(sessions) == 0 {
		return
	}
	ids := make([]uuid.UUID, 0, len(sessions))
	for i := range sessions {
		id, err := uuid.Parse(sessions[i].Id)
		if err != nil {
			continue
		}
		ids = append(ids, id)
	}
	counts, err := d.Store.AdminCountSessionChildren(c.Request.Context(), ids)
	if err != nil {
		return
	}
	for i := range sessions {
		if count, ok := counts[sessions[i].Id]; ok {
			sessions[i].ChildrenCount = &count
		}
	}
}

func revokeAccountSession(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		account := lookupAccount(c, d, c.Param("name"))
		if account == nil {
			return
		}
		accountID, _ := uuid.Parse(account.Id)
		sessionID, err := uuid.Parse(c.Param("sessionId"))
		if err != nil {
			accountNotFound(c)
			return
		}
		now := time.Now().UTC()
		session, err := d.Store.AdminRevokeSession(c.Request.Context(), accountID, sessionID, now)
		if err != nil {
			if err == store.ErrNotFound {
				accountNotFound(c)
				return
			}
			serverError(c, err, d)
			return
		}
		removeSessionCache(d, c, session.Id)
		logAction(d, c, accountID, model.ActionLogSessionRevoke, map[string]any{
			"session_id": sessionID.String(),
		})
		c.Status(http.StatusNoContent)
	}
}

func revokeAllSessions(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		account := lookupAccount(c, d, c.Param("name"))
		if account == nil {
			return
		}
		accountID, _ := uuid.Parse(account.Id)
		now := time.Now().UTC()
		sessions, err := d.Store.AdminRevokeAllSessions(c.Request.Context(), accountID, now)
		if err != nil {
			serverError(c, err, d)
			return
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
		c.JSON(http.StatusOK, gin.H{})
	}
}

// removeSessionCache mirrors AuthCacheConstants.Session/RemoveGroup.
func removeSessionCache(d Deps, c *gin.Context, sessionID string) {
	if d.Redis == nil || d.Redis.Cache == nil {
		return
	}
	ctx := c.Request.Context()
	_ = d.Redis.Cache.Remove(ctx, "auth:session:"+sessionID)
	_ = d.Redis.Cache.RemoveGroup(ctx, "auth:session_tokens:"+sessionID)
}

// ─────────────────────────── Contacts ───────────────────────────

func listAccountContacts(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		account := lookupAccount(c, d, c.Param("name"))
		if account == nil {
			return
		}
		accountID, _ := uuid.Parse(account.Id)
		contacts, err := d.Store.AdminListContacts(c.Request.Context(), accountID)
		if err != nil {
			serverError(c, err, d)
			return
		}
		c.JSON(http.StatusOK, contacts)
	}
}

func createAccountContact(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request adminContactRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, errs.New("PADLOCK_CONTACT_CONTENT_REQUIRED", "Content is required.", http.StatusBadRequest))
			return
		}
		if strings.TrimSpace(request.Content) == "" {
			c.JSON(http.StatusBadRequest, errs.New("PADLOCK_CONTACT_CONTENT_REQUIRED", "Content is required.", http.StatusBadRequest))
			return
		}
		account := lookupAccount(c, d, c.Param("name"))
		if account == nil {
			return
		}
		accountID, _ := uuid.Parse(account.Id)
		contact, err := d.Store.AdminCreateContact(c.Request.Context(), accountID, request.Type, strings.TrimSpace(request.Content))
		if err != nil {
			serverError(c, err, d)
			return
		}
		c.JSON(http.StatusOK, contact)
	}
}

func updateAccountContact(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request updateAdminContactRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, errs.New("PADLOCK_ACCOUNT_NOT_FOUND", "Account not found.", http.StatusBadRequest))
			return
		}
		account := lookupAccount(c, d, c.Param("name"))
		if account == nil {
			return
		}
		accountID, _ := uuid.Parse(account.Id)
		contactID, err := uuid.Parse(c.Param("contactId"))
		if err != nil {
			accountNotFound(c)
			return
		}
		contact, err := d.Store.AdminUpdateContact(c.Request.Context(), accountID, contactID, request.Type, request.Content)
		if err != nil {
			if err == store.ErrNotFound {
				accountNotFound(c)
				return
			}
			serverError(c, err, d)
			return
		}
		c.JSON(http.StatusOK, contact)
	}
}

func requestAccountContactVerification(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		account := lookupAccount(c, d, c.Param("name"))
		if account == nil {
			return
		}
		accountID, _ := uuid.Parse(account.Id)
		contactID, err := uuid.Parse(c.Param("contactId"))
		if err != nil {
			accountNotFound(c)
			return
		}
		contact, err := d.Store.AdminGetContact(c.Request.Context(), accountID, contactID)
		if err != nil {
			if err == store.ErrNotFound {
				accountNotFound(c)
				return
			}
			serverError(c, err, d)
			return
		}
		// Mirrors the C# dispatch: RequestContactVerification creates a 24h
		// contact-verification magic spell and emails it.
		if contact.VerifiedAt != nil {
			c.JSON(http.StatusBadRequest, errs.New("PADLOCK_OPERATION_FAILED", "Contact has already been verified.", http.StatusBadRequest))
			return
		}
		if contact.Type != int(model.ContactTypeEmail) {
			c.JSON(http.StatusBadRequest, errs.New("PADLOCK_OPERATION_FAILED", "Only email contact methods can be verified.", http.StatusBadRequest))
			return
		}
		expiresAt := time.Now().UTC().Add(24 * time.Hour)
		spell, err := d.Spells.CreateMagicSpell(c.Request.Context(), account.Id, model.MagicSpellTypeContactVerification, map[string]any{
			"contact_id":     contact.Id,
			"contact_type":   "Email",
			"contact_method": contact.Content,
		}, spell.CreateOptions{ExpiresAt: &expiresAt, PreventRepeat: true})
		if err != nil {
			serverError(c, err, d)
			return
		}
		if err := d.Spells.NotifyMagicSpell(c.Request.Context(), spell, true); err != nil {
			serverError(c, err, d)
			return
		}
		c.JSON(http.StatusOK, contact)
	}
}

// ─────────────────────────── Admin magic spells ───────────────────────────

// listAccountMagicSpells mirrors Passport AccountAdminController.
// ListAccountMagicSpells: the account's spells, newest first.
func listAccountMagicSpells(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		account := lookupAccount(c, d, c.Param("name"))
		if account == nil {
			return
		}
		spells, err := d.Store.ListMagicSpellsByAccount(c.Request.Context(), account.Id)
		if err != nil {
			serverError(c, err, d)
			return
		}
		c.JSON(http.StatusOK, spells)
	}
}

// createAdminMagicSpellRequest mirrors CreateAdminMagicSpellRequest.
type createAdminMagicSpellRequest struct {
	Type          *int           `json:"type"`
	Meta          map[string]any `json:"meta"`
	ExpiresAt     *model.Time    `json:"expires_at"`
	AffectedAt    *model.Time    `json:"affected_at"`
	Code          *string        `json:"code"`
	PreventRepeat bool           `json:"prevent_repeat"`
	SendEmail     *bool          `json:"send_email"`
	BypassVerify  *bool          `json:"bypass_verify"`
}

// createAccountMagicSpell mirrors Passport AccountAdminController.
// CreateAccountMagicSpell: 201 Created with the spell; optionally emails it.
func createAccountMagicSpell(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		account := lookupAccount(c, d, c.Param("name"))
		if account == nil {
			return
		}
		var request createAdminMagicSpellRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, errs.New("PASSPORT_SPELL_TYPE_REQUIRED", "A supported magic spell type is required.", http.StatusBadRequest))
			return
		}
		typ := model.MagicSpellType(-1)
		if request.Type != nil {
			typ = model.MagicSpellType(*request.Type)
		}
		if typ < 0 || typ > model.MagicSpellTypeContactVerification {
			c.JSON(http.StatusBadRequest, errs.New("PASSPORT_SPELL_TYPE_REQUIRED", "A supported magic spell type is required.", http.StatusBadRequest))
			return
		}
		if typ == model.MagicSpellTypeAccountDeactivation {
			c.JSON(http.StatusBadRequest, errs.New("PASSPORT_SPELL_DEACTIVATION_NO_EMAIL", "Account deactivation magic spells cannot be sent by email.", http.StatusBadRequest))
			return
		}
		opts := spell.CreateOptions{
			Code:          derefString(request.Code),
			PreventRepeat: request.PreventRepeat,
		}
		if request.ExpiresAt != nil {
			expires := request.ExpiresAt.Time()
			opts.ExpiresAt = &expires
		}
		if request.AffectedAt != nil {
			affected := request.AffectedAt.Time()
			opts.AffectedAt = &affected
		}
		created, err := d.Spells.CreateMagicSpell(c.Request.Context(), account.Id, typ, request.Meta, opts)
		if err != nil {
			serverError(c, err, d)
			return
		}
		sendEmail := request.SendEmail == nil || *request.SendEmail
		if sendEmail {
			bypass := request.BypassVerify == nil || *request.BypassVerify
			if err := d.Spells.ResendMagicSpell(c.Request.Context(), created, bypass); err != nil {
				serverError(c, err, d)
				return
			}
		}
		c.Header("Location", "/api/admin/accounts/"+account.Id+"/spells/"+created.Id)
		c.JSON(http.StatusCreated, created)
	}
}

// resendAdminMagicSpellRequest mirrors ResendAdminMagicSpellRequest.
type resendAdminMagicSpellRequest struct {
	BypassVerify *bool `json:"bypass_verify"`
}

// resendAccountMagicSpell mirrors Passport AccountAdminController.
// ResendAccountMagicSpell: 204 NoContent.
func resendAccountMagicSpell(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		account := lookupAccount(c, d, c.Param("name"))
		if account == nil {
			return
		}
		spell, ok := loadAccountSpell(c, d, account)
		if !ok {
			return
		}
		if spell.Type == model.MagicSpellTypeAccountDeactivation {
			c.JSON(http.StatusBadRequest, errs.New("PASSPORT_SPELL_DEACTIVATION_NO_EMAIL", "Account deactivation magic spells cannot be sent by email.", http.StatusBadRequest))
			return
		}
		var request resendAdminMagicSpellRequest
		_ = c.ShouldBindJSON(&request)
		bypass := request.BypassVerify == nil || *request.BypassVerify
		if err := d.Spells.ResendMagicSpell(c.Request.Context(), spell, bypass); err != nil {
			serverError(c, err, d)
			return
		}
		c.Status(http.StatusNoContent)
	}
}

// deleteAccountMagicSpell mirrors Passport AccountAdminController.
// DeleteAccountMagicSpell: 204 NoContent.
func deleteAccountMagicSpell(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		account := lookupAccount(c, d, c.Param("name"))
		if account == nil {
			return
		}
		spell, ok := loadAccountSpell(c, d, account)
		if !ok {
			return
		}
		if err := d.Store.DeleteMagicSpell(c.Request.Context(), spell.Id); err != nil {
			serverError(c, err, d)
			return
		}
		c.Status(http.StatusNoContent)
	}
}

// loadAccountSpell loads a spell that belongs to the account, aborting with
// the canonical 404 PASSPORT_SPELL_NOT_FOUND otherwise.
func loadAccountSpell(c *gin.Context, d Deps, account *model.Account) (*model.MagicSpell, bool) {
	spellID, err := uuid.Parse(c.Param("spellId"))
	if err != nil {
		c.JSON(http.StatusNotFound, errs.New("PASSPORT_SPELL_NOT_FOUND", "Magic spell not found.", http.StatusNotFound))
		return nil, false
	}
	spell, err := d.Store.GetMagicSpellByID(c.Request.Context(), spellID)
	if err != nil || spell.AccountId != account.Id {
		c.JSON(http.StatusNotFound, errs.New("PASSPORT_SPELL_NOT_FOUND", "Magic spell not found.", http.StatusNotFound))
		return nil, false
	}
	return spell, true
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func verifyAccountContact(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		account := lookupAccount(c, d, c.Param("name"))
		if account == nil {
			return
		}
		accountID, _ := uuid.Parse(account.Id)
		contactID, err := uuid.Parse(c.Param("contactId"))
		if err != nil {
			accountNotFound(c)
			return
		}
		verifiedAt := time.Now().UTC()
		var request adminContactVerificationRequest
		if err := c.ShouldBindJSON(&request); err == nil && request.VerifiedAt != nil {
			verifiedAt = request.VerifiedAt.Time()
		}
		contact, err := d.Store.AdminSetContactVerified(c.Request.Context(), accountID, contactID, verifiedAt)
		if err != nil {
			if err == store.ErrNotFound {
				accountNotFound(c)
				return
			}
			serverError(c, err, d)
			return
		}
		c.JSON(http.StatusOK, contact)
	}
}

func unverifyAccountContact(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		account := lookupAccount(c, d, c.Param("name"))
		if account == nil {
			return
		}
		accountID, _ := uuid.Parse(account.Id)
		contactID, err := uuid.Parse(c.Param("contactId"))
		if err != nil {
			accountNotFound(c)
			return
		}
		contact, err := d.Store.AdminClearContactVerified(c.Request.Context(), accountID, contactID)
		if err != nil {
			if err == store.ErrNotFound {
				accountNotFound(c)
				return
			}
			serverError(c, err, d)
			return
		}
		c.JSON(http.StatusOK, contact)
	}
}

func setPrimaryAccountContact(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		account := lookupAccount(c, d, c.Param("name"))
		if account == nil {
			return
		}
		accountID, _ := uuid.Parse(account.Id)
		contactID, err := uuid.Parse(c.Param("contactId"))
		if err != nil {
			accountNotFound(c)
			return
		}
		contact, err := d.Store.AdminSetContactPrimary(c.Request.Context(), accountID, contactID)
		if err != nil {
			if err == store.ErrNotFound {
				accountNotFound(c)
				return
			}
			serverError(c, err, d)
			return
		}
		c.JSON(http.StatusOK, contact)
	}
}

func setAccountContactVisibility(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request setAdminContactVisibilityRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, errs.New("PADLOCK_ACCOUNT_NOT_FOUND", "Account not found.", http.StatusBadRequest))
			return
		}
		account := lookupAccount(c, d, c.Param("name"))
		if account == nil {
			return
		}
		accountID, _ := uuid.Parse(account.Id)
		contactID, err := uuid.Parse(c.Param("contactId"))
		if err != nil {
			accountNotFound(c)
			return
		}
		contact, err := d.Store.AdminSetContactPublic(c.Request.Context(), accountID, contactID, request.IsPublic)
		if err != nil {
			if err == store.ErrNotFound {
				accountNotFound(c)
				return
			}
			serverError(c, err, d)
			return
		}
		c.JSON(http.StatusOK, contact)
	}
}

func deleteAccountContact(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		account := lookupAccount(c, d, c.Param("name"))
		if account == nil {
			return
		}
		accountID, _ := uuid.Parse(account.Id)
		contactID, err := uuid.Parse(c.Param("contactId"))
		if err != nil {
			accountNotFound(c)
			return
		}
		if err := d.Store.AdminDeleteContact(c.Request.Context(), accountID, contactID); err != nil {
			if err == store.ErrNotFound {
				accountNotFound(c)
				return
			}
			serverError(c, err, d)
			return
		}
		c.Status(http.StatusNoContent)
	}
}

// ─────────────────────────── Auth factors ───────────────────────────

func listAccountAuthFactors(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		account := lookupAccount(c, d, c.Param("name"))
		if account == nil {
			return
		}
		accountID, _ := uuid.Parse(account.Id)
		factors, err := d.Store.AdminListAuthFactors(c.Request.Context(), accountID)
		if err != nil {
			serverError(c, err, d)
			return
		}
		response := make([]accountAuthFactorSummary, 0, len(factors))
		for i := range factors {
			response = append(response, toAuthFactorSummary(&factors[i]))
		}
		c.JSON(http.StatusOK, response)
	}
}

func createAccountAuthFactor(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request adminAccountAuthFactorRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, errs.New("PADLOCK_AUTH_FACTOR_INVALID", "Invalid factor request.", http.StatusBadRequest))
			return
		}
		account := lookupAccount(c, d, c.Param("name"))
		if account == nil {
			return
		}
		accountID, _ := uuid.Parse(account.Id)

		exists, err := d.Store.AdminCheckAuthFactorExists(c.Request.Context(), accountID, request.Type)
		if err != nil {
			serverError(c, err, d)
			return
		}
		if exists {
			c.JSON(http.StatusBadRequest, errs.New("PADLOCK_AUTH_FACTOR_ALREADY_EXISTS",
				"Auth factor with type "+strconv.Itoa(request.Type)+" already exists.", http.StatusBadRequest))
			return
		}

		factor, err := buildAdminAuthFactor(accountID, account.Name, request)
		if err != nil {
			c.JSON(http.StatusBadRequest, errs.New("PADLOCK_AUTH_FACTOR_INVALID", "Invalid factor request.", http.StatusBadRequest))
			return
		}
		inserted, err := d.Store.AdminInsertAuthFactor(c.Request.Context(), factor)
		if err != nil {
			serverError(c, err, d)
			return
		}
		enable := true
		if request.Enable != nil {
			enable = *request.Enable
		}
		if enable && inserted.EnabledAt == nil {
			inserted, err = enableFactorFlow(c, d, inserted, request.Code)
			if err != nil {
				if errors.Is(err, errFactorInvalidCode) {
					c.JSON(http.StatusBadRequest, errs.New("PADLOCK_OPERATION_FAILED", err.Error(), http.StatusBadRequest))
					return
				}
				serverError(c, err, d)
				return
			}
		}
		logAction(d, c, accountID, model.ActionLogAuthFactorCreate, map[string]any{
			"factor_type": factorTypeName(inserted.Type),
		})
		c.JSON(http.StatusOK, toAuthFactorSummary(inserted))
	}
}

func enableAccountAuthFactor(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		account := lookupAccount(c, d, c.Param("name"))
		if account == nil {
			return
		}
		accountID, _ := uuid.Parse(account.Id)
		factorID, err := uuid.Parse(c.Param("factorId"))
		if err != nil {
			accountNotFound(c)
			return
		}
		factor, err := d.Store.AdminGetAuthFactor(c.Request.Context(), accountID, factorID)
		if err != nil {
			if err == store.ErrNotFound {
				accountNotFound(c)
				return
			}
			serverError(c, err, d)
			return
		}
		var code *string
		var raw json.RawMessage
		if err := c.ShouldBindJSON(&raw); err == nil && len(raw) > 0 && string(raw) != "null" {
			var s string
			if err := json.Unmarshal(raw, &s); err == nil {
				code = &s
			}
		}
		factor, err = enableFactorFlow(c, d, factor, code)
		if err != nil {
			if errors.Is(err, errFactorInvalidCode) {
				c.JSON(http.StatusBadRequest, errs.New("PADLOCK_OPERATION_FAILED", err.Error(), http.StatusBadRequest))
				return
			}
			serverError(c, err, d)
			return
		}
		logAction(d, c, accountID, model.ActionLogAuthFactorEnable, map[string]any{
			"factor_type": factorTypeName(factor.Type),
		})
		c.JSON(http.StatusOK, toAuthFactorSummary(factor))
	}
}

func disableAccountAuthFactor(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		account := lookupAccount(c, d, c.Param("name"))
		if account == nil {
			return
		}
		accountID, _ := uuid.Parse(account.Id)
		factorID, err := uuid.Parse(c.Param("factorId"))
		if err != nil {
			accountNotFound(c)
			return
		}
		factor, err := d.Store.AdminGetAuthFactor(c.Request.Context(), accountID, factorID)
		if err != nil {
			if err == store.ErrNotFound {
				accountNotFound(c)
				return
			}
			serverError(c, err, d)
			return
		}
		factor.EnabledAt = nil
		if err := d.Store.AdminUpdateAuthFactor(c.Request.Context(), factor); err != nil {
			serverError(c, err, d)
			return
		}
		logAction(d, c, accountID, model.ActionLogAuthFactorDisable, map[string]any{
			"factor_type": factorTypeName(factor.Type),
		})
		c.JSON(http.StatusOK, toAuthFactorSummary(factor))
	}
}

func resetAccountPasswordFactor(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request adminResetPasswordFactorRequest
		if err := c.ShouldBindJSON(&request); err != nil || strings.TrimSpace(request.NewPassword) == "" {
			c.JSON(http.StatusBadRequest, errs.New("PADLOCK_PASSWORD_REQUIRED", "New password is required.", http.StatusBadRequest))
			return
		}
		account := lookupAccount(c, d, c.Param("name"))
		if account == nil {
			return
		}
		accountID, _ := uuid.Parse(account.Id)

		hash, err := auth.HashPassword(request.NewPassword)
		if err != nil {
			serverError(c, err, d)
			return
		}
		factor, err := d.Store.AdminUpsertPasswordFactor(c.Request.Context(), accountID, hash, time.Now().UTC())
		if err != nil {
			serverError(c, err, d)
			return
		}
		if request.RevokeSessions {
			now := time.Now().UTC()
			sessions, err := d.Store.AdminRevokeAllSessions(c.Request.Context(), accountID, now)
			if err != nil {
				serverError(c, err, d)
				return
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
		logAction(d, c, accountID, model.ActionLogAuthFactorResetPassword, map[string]any{
			"factor_type": "Password",
		})
		c.JSON(http.StatusOK, toAuthFactorSummary(factor))
	}
}

func deleteAccountAuthFactor(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		account := lookupAccount(c, d, c.Param("name"))
		if account == nil {
			return
		}
		accountID, _ := uuid.Parse(account.Id)
		factorID, err := uuid.Parse(c.Param("factorId"))
		if err != nil {
			accountNotFound(c)
			return
		}
		factor, err := d.Store.AdminGetAuthFactor(c.Request.Context(), accountID, factorID)
		if err != nil {
			if err == store.ErrNotFound {
				accountNotFound(c)
				return
			}
			serverError(c, err, d)
			return
		}
		if err := d.Store.AdminDeleteAuthFactor(c.Request.Context(), accountID, factorID); err != nil {
			serverError(c, err, d)
			return
		}
		logAction(d, c, accountID, model.ActionLogAuthFactorDelete, map[string]any{
			"factor_type": factorTypeName(factor.Type),
		})
		c.Status(http.StatusNoContent)
	}
}

// buildAdminAuthFactor mirrors AccountService.CreateAuthFactor.
func buildAdminAuthFactor(accountID uuid.UUID, accountName string, request adminAccountAuthFactorRequest) (*model.AuthFactor, error) {
	now := time.Now().UTC()
	secret := ""
	if request.Secret != nil {
		secret = *request.Secret
	}
	switch model.AuthFactorType(request.Type) {
	case model.AuthFactorTypeRecoveryCode:
		code := strings.ReplaceAll(uuid.NewString(), "-", "")
		return &model.AuthFactor{
			Type:            model.AuthFactorTypeRecoveryCode,
			Trustworthy:     0,
			AccountId:       accountID.String(),
			Secret:          code,
			EnabledAt:       model.NewTime(now),
			CreatedResponse: map[string]any{"recovery_code": code},
		}, nil
	case model.AuthFactorTypePassword:
		if strings.TrimSpace(secret) == "" {
			return nil, errs.New("PADLOCK_AUTH_FACTOR_INVALID", "Invalid factor request.", http.StatusBadRequest)
		}
		hash, err := auth.HashPassword(secret)
		if err != nil {
			return nil, err
		}
		return &model.AuthFactor{
			Type:        model.AuthFactorTypePassword,
			Trustworthy: 1,
			AccountId:   accountID.String(),
			Secret:      hash,
			EnabledAt:   model.NewTime(now),
		}, nil
	case model.AuthFactorTypeEmailCode:
		return &model.AuthFactor{
			Type:        model.AuthFactorTypeEmailCode,
			Trustworthy: 2,
			AccountId:   accountID.String(),
			EnabledAt:   model.NewTime(now),
		}, nil
	case model.AuthFactorTypeInAppCode:
		return &model.AuthFactor{
			Type:        model.AuthFactorTypeInAppCode,
			Trustworthy: 2,
			AccountId:   accountID.String(),
			EnabledAt:   model.NewTime(now),
		}, nil
	case model.AuthFactorTypeTimedCode:
		totpSecret := secret
		if strings.TrimSpace(totpSecret) == "" {
			raw := make([]byte, 20)
			if _, err := rand.Read(raw); err != nil {
				return nil, err
			}
			totpSecret = base32.StdEncoding.EncodeToString(raw)
		}
		label := accountName
		if label == "" {
			label = accountID.String()
		}
		uri := "otpauth://totp/SolarNetwork:" + urlEscape(label) + "?secret=" + totpSecret + "&issuer=SolarNetwork&digits=6&period=30"
		return &model.AuthFactor{
			Type:        model.AuthFactorTypeTimedCode,
			Trustworthy: 3,
			AccountId:   accountID.String(),
			Secret:      totpSecret,
			// TimedCode factors are created disabled (EnabledAt nil) and
			// respect the request.Enable flag like the C#.
			CreatedResponse: map[string]any{"secret": totpSecret, "uri": uri},
		}, nil
	case model.AuthFactorTypePinCode:
		if strings.TrimSpace(secret) == "" {
			return nil, errs.New("PADLOCK_AUTH_FACTOR_INVALID", "Invalid factor request.", http.StatusBadRequest)
		}
		hash, err := auth.HashPassword(secret)
		if err != nil {
			return nil, err
		}
		return &model.AuthFactor{
			Type:        model.AuthFactorTypePinCode,
			Trustworthy: 0,
			AccountId:   accountID.String(),
			Secret:      hash,
			EnabledAt:   model.NewTime(now),
		}, nil
	case model.AuthFactorTypeNfcToken:
		f := &model.AuthFactor{
			Type:        model.AuthFactorTypeNfcToken,
			Trustworthy: 1,
			AccountId:   accountID.String(),
			EnabledAt:   model.NewTime(now),
		}
		if strings.TrimSpace(secret) != "" {
			f.Config = map[string]any{"tag_id": secret}
		}
		return f, nil
	case model.AuthFactorTypePasskey:
		return &model.AuthFactor{
			Type:        model.AuthFactorTypePasskey,
			Trustworthy: 4,
			AccountId:   accountID.String(),
			EnabledAt:   model.NewTime(now),
		}, nil
	case model.AuthFactorTypeQrLogin:
		return &model.AuthFactor{
			Type:        model.AuthFactorTypeQrLogin,
			Trustworthy: 3,
			AccountId:   accountID.String(),
			EnabledAt:   model.NewTime(now),
		}, nil
	default:
		return nil, errs.New("PADLOCK_AUTH_FACTOR_INVALID", "Invalid factor request.", http.StatusBadRequest)
	}
}

func urlEscape(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, ":", "%3A"), "@", "%40")
}

// errFactorInvalidCode is the enable-flow failure surfaced as
// PADLOCK_OPERATION_FAILED with the C# message.
var errFactorInvalidCode = errors.New("Invalid factor code.")

// enableFactorFlow mirrors AccountService.EnableAuthFactor.
func enableFactorFlow(c *gin.Context, d Deps, factor *model.AuthFactor, code *string) (*model.AuthFactor, error) {
	now := time.Now().UTC()
	if factor.Type == model.AuthFactorTypeRecoveryCode {
		newCode := strings.ReplaceAll(uuid.NewString(), "-", "")
		factor.Secret = newCode
		factor.EnabledAt = model.NewTime(now)
		factor.CreatedResponse = map[string]any{"recovery_code": newCode}
		if err := d.Store.AdminUpdateAuthFactor(c.Request.Context(), factor); err != nil {
			return nil, err
		}
		return factor, nil
	}
	switch model.AuthFactorType(factor.Type) {
	case model.AuthFactorTypePassword, model.AuthFactorTypeTimedCode, model.AuthFactorTypePasskey, model.AuthFactorTypeQrLogin:
		factor.EnabledAt = model.NewTime(now)
		if err := d.Store.AdminUpdateAuthFactor(c.Request.Context(), factor); err != nil {
			return nil, err
		}
		return factor, nil
	}
	if code == nil || strings.TrimSpace(*code) == "" {
		return nil, errFactorInvalidCode
	}
	ok, err := verifyAdminFactorCode(c, d, factor, *code)
	if err != nil || !ok {
		return nil, errFactorInvalidCode
	}
	factor.EnabledAt = model.NewTime(now)
	if err := d.Store.AdminUpdateAuthFactor(c.Request.Context(), factor); err != nil {
		return nil, err
	}
	return factor, nil
}

// verifyAdminFactorCode mirrors AccountService.VerifyFactorCode.
func verifyAdminFactorCode(c *gin.Context, d Deps, factor *model.AuthFactor, code string) (bool, error) {
	switch model.AuthFactorType(factor.Type) {
	case model.AuthFactorTypeEmailCode, model.AuthFactorTypeInAppCode:
		if d.Redis == nil || d.Redis.Cache == nil {
			return false, nil
		}
		key := "authfactor:" + factor.Id + ":code"
		var cached string
		found, err := d.Redis.Cache.Get(c.Request.Context(), key, &cached)
		if err != nil || !found {
			return false, err
		}
		if cached != code {
			return false, nil
		}
		_ = d.Redis.Cache.Remove(c.Request.Context(), key)
		return true, nil
	case model.AuthFactorTypePassword, model.AuthFactorTypePinCode, model.AuthFactorTypeTimedCode:
		return auth.VerifyFactorPassword(factor, code)
	case model.AuthFactorTypeNfcToken:
		// NFC validation is delegated to Passport's gRPC service in the C#
		// fleet; Stargate has no NFC client, so the code never verifies.
		if d.Log != nil {
			d.Log.Warn("NFC factor enable attempted without NFC validation service", "factor", factor.Id)
		}
		return false, nil
	default:
		return false, nil
	}
}

// toAuthFactorSummary mirrors ToAuthFactorSummary.
func toAuthFactorSummary(factor *model.AuthFactor) accountAuthFactorSummary {
	return accountAuthFactorSummary{
		Id:          factor.Id,
		Type:        int(factor.Type),
		Trustworthy: factor.Trustworthy,
		HasSecret:   strings.TrimSpace(factor.Secret) != "",
		Config:      factor.Config,
		EnabledAt:   factor.EnabledAt,
		ExpiredAt:   factor.ExpiredAt,
		CreatedAt:   factor.CreatedAt,
		UpdatedAt:   factor.UpdatedAt,
	}
}

func factorTypeName(ftype model.AuthFactorType) string {
	switch model.AuthFactorType(ftype) {
	case model.AuthFactorTypePassword:
		return "Password"
	case model.AuthFactorTypeEmailCode:
		return "EmailCode"
	case model.AuthFactorTypeInAppCode:
		return "InAppCode"
	case model.AuthFactorTypeTimedCode:
		return "TimedCode"
	case model.AuthFactorTypePinCode:
		return "PinCode"
	case model.AuthFactorTypeRecoveryCode:
		return "RecoveryCode"
	case model.AuthFactorTypeNfcToken:
		return "NfcToken"
	case model.AuthFactorTypePasskey:
		return "Passkey"
	case model.AuthFactorTypeQrLogin:
		return "QrLogin"
	default:
		return strconv.Itoa(int(ftype))
	}
}

// ─────────────────────────── Notifications / emails ───────────────────────────

func sendNotification(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request adminNotificationRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, errs.New("PADLOCK_NOTIFICATION_TARGET_REQUIRED", "Provide account_id, account_ids, or set broadcast_to_all=true.", http.StatusBadRequest))
			return
		}
		if !request.BroadcastToAll && request.AccountId == nil && len(request.AccountIds) == 0 {
			c.JSON(http.StatusBadRequest, errs.New("PADLOCK_NOTIFICATION_TARGET_REQUIRED", "Provide account_id, account_ids, or set broadcast_to_all=true.", http.StatusBadRequest))
			return
		}
		if strings.TrimSpace(request.Topic) == "" {
			c.JSON(http.StatusBadRequest, errs.New("PADLOCK_NOTIFICATION_TOPIC_REQUIRED", "Topic is required.", http.StatusBadRequest))
			return
		}

		requested := countRequested(request.AccountId, request.AccountIds, request.BroadcastToAll)
		targetIDs, err := d.Store.AdminResolveTargetAccountIDs(c.Request.Context(), mergeIDs(request.AccountId, request.AccountIds), request.BroadcastToAll)
		if err != nil {
			serverError(c, err, d)
			return
		}
		if len(targetIDs) == 0 {
			c.JSON(http.StatusOK, adminMessageDispatchResponse{Requested: requested, BroadcastToAll: request.BroadcastToAll})
			return
		}

		notification := &gen.DyPushNotification{
			Topic:     strings.TrimSpace(request.Topic),
			Title:     strOrEmpty(request.Title),
			Subtitle:  strOrEmpty(request.Subtitle),
			Body:      strOrEmpty(request.Body),
			IsSilent:  request.IsSilent,
			IsSavable: request.IsSavable,
			PushType:  request.PushType,
			ActionUri: request.ActionUri,
		}
		if request.Meta != nil {
			if meta, err := json.Marshal(request.Meta); err == nil {
				notification.Meta = meta
			}
		}
		userIDs := make([]string, 0, len(targetIDs))
		for _, id := range targetIDs {
			userIDs = append(userIDs, id.String())
		}
		sent := len(targetIDs)
		if err := sendPushToUsers(c, d, userIDs, notification); err != nil {
			if d.Log != nil {
				d.Log.Warn("admin notification push failed", "error", err)
			}
			sent = 0
		}
		c.JSON(http.StatusOK, adminMessageDispatchResponse{
			Requested:      requested,
			Resolved:       len(targetIDs),
			Sent:           sent,
			Skipped:        0,
			BroadcastToAll: request.BroadcastToAll,
		})
	}
}

func sendEmails(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request adminEmailRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, errs.New("PADLOCK_NOTIFICATION_TARGET_REQUIRED", "Provide account_id, account_ids, or set broadcast_to_all=true.", http.StatusBadRequest))
			return
		}
		if !request.BroadcastToAll && request.AccountId == nil && len(request.AccountIds) == 0 {
			c.JSON(http.StatusBadRequest, errs.New("PADLOCK_NOTIFICATION_TARGET_REQUIRED", "Provide account_id, account_ids, or set broadcast_to_all=true.", http.StatusBadRequest))
			return
		}
		if strings.TrimSpace(request.Subject) == "" {
			c.JSON(http.StatusBadRequest, errs.New("PADLOCK_EMAIL_SUBJECT_REQUIRED", "Subject is required.", http.StatusBadRequest))
			return
		}
		if strings.TrimSpace(request.HtmlBody) == "" {
			c.JSON(http.StatusBadRequest, errs.New("PADLOCK_EMAIL_BODY_REQUIRED", "HTML body is required.", http.StatusBadRequest))
			return
		}

		requested := countRequested(request.AccountId, request.AccountIds, request.BroadcastToAll)
		targetIDs, err := d.Store.AdminResolveTargetAccountIDs(c.Request.Context(), mergeIDs(request.AccountId, request.AccountIds), request.BroadcastToAll)
		if err != nil {
			serverError(c, err, d)
			return
		}
		if len(targetIDs) == 0 {
			c.JSON(http.StatusOK, adminMessageDispatchResponse{Requested: requested, BroadcastToAll: request.BroadcastToAll})
			return
		}

		recipients, err := d.Store.AdminListEmailContacts(c.Request.Context(), targetIDs, true)
		if err != nil {
			serverError(c, err, d)
			return
		}
		sent := 0
		for _, recipient := range recipients {
			if err := sendEmailTo(c, d, recipient, request.Subject, request.HtmlBody); err != nil {
				if d.Log != nil {
					d.Log.Warn("admin email send failed", "account", recipient.AccountID, "error", err)
				}
				continue
			}
			sent++
		}
		c.JSON(http.StatusOK, adminMessageDispatchResponse{
			Requested:      requested,
			Resolved:       len(targetIDs),
			Sent:           sent,
			Skipped:        len(targetIDs) - len(recipients),
			BroadcastToAll: request.BroadcastToAll,
		})
	}
}

func exportEmailContactsCsv(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		broadcast := c.Query("broadcastToAll") == "true"
		accountID := parseUUIDQuery(c, "accountId")
		accountIDs := parseUUIDArray(c, "accountIds")
		if !broadcast && accountID == nil && len(accountIDs) == 0 {
			c.JSON(http.StatusBadRequest, errs.New("PADLOCK_NOTIFICATION_TARGET_REQUIRED", "Provide account_id, account_ids, or set broadcast_to_all=true.", http.StatusBadRequest))
			return
		}
		targetIDs, err := d.Store.AdminResolveTargetAccountIDs(c.Request.Context(), mergeIDs(accountID, accountIDs), broadcast)
		if err != nil {
			serverError(c, err, d)
			return
		}
		recipients, err := d.Store.AdminListEmailContacts(c.Request.Context(), targetIDs, false)
		if err != nil {
			serverError(c, err, d)
			return
		}
		sortRecipientsByName(recipients)

		var buf strings.Builder
		bw := csv.NewWriter(&buf)
		_ = bw.Write([]string{"EmailAddr", "UserName"})
		for _, recipient := range recipients {
			_ = bw.Write([]string{recipient.Content, recipient.UserName})
		}
		bw.Flush()

		payload := append([]byte{0xEF, 0xBB, 0xBF}, []byte(buf.String())...)
		filename := "account-email-contacts-" + time.Now().UTC().Format("20060102150405") + ".csv"
		c.Header("Content-Disposition", "attachment; filename="+filename)
		c.Data(http.StatusOK, "text/csv; charset=utf-8", payload)
	}
}

func sortRecipientsByName(recipients []store.AdminEmailRecipient) {
	for i := 1; i < len(recipients); i++ {
		for j := i; j > 0 && strings.ToLower(recipients[j].UserName) < strings.ToLower(recipients[j-1].UserName); j-- {
			recipients[j], recipients[j-1] = recipients[j-1], recipients[j]
		}
	}
}

func sendPushToUsers(c *gin.Context, d Deps, userIDs []string, notification *gen.DyPushNotification) error {
	if d.Clients == nil || d.Clients.Ring == nil {
		return errors.New("ring service not configured")
	}
	_, err := d.Clients.Ring.SendPushNotificationToUsers(c.Request.Context(), &gen.DySendPushNotificationToUsersRequest{
		UserIds:      userIDs,
		Notification: notification,
	})
	return err
}

func sendEmailTo(c *gin.Context, d Deps, recipient store.AdminEmailRecipient, subject, body string) error {
	if d.Clients == nil || d.Clients.Ring == nil {
		return errors.New("ring service not configured")
	}
	_, err := d.Clients.Ring.SendEmail(c.Request.Context(), &gen.DySendEmailRequest{
		Email: &gen.DyEmailMessage{
			ToName:    recipient.UserName,
			ToAddress: recipient.Content,
			Subject:   subject,
			Body:      body,
		},
	})
	return err
}

func countRequested(accountID *uuid.UUID, accountIDs []uuid.UUID, broadcast bool) int {
	if broadcast {
		return 0
	}
	seen := map[uuid.UUID]struct{}{}
	if accountID != nil {
		seen[*accountID] = struct{}{}
	}
	for _, id := range accountIDs {
		seen[id] = struct{}{}
	}
	return len(seen)
}

func mergeIDs(accountID *uuid.UUID, accountIDs []uuid.UUID) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(accountIDs)+1)
	if accountID != nil {
		ids = append(ids, *accountID)
	}
	return append(ids, accountIDs...)
}

func strOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func parseUUIDQuery(c *gin.Context, name string) *uuid.UUID {
	raw := c.Query(name)
	if raw == "" {
		return nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return nil
	}
	return &id
}

func parseUUIDArray(c *gin.Context, name string) []uuid.UUID {
	values := c.QueryArray(name)
	ids := make([]uuid.UUID, 0, len(values))
	for _, value := range values {
		if id, err := uuid.Parse(value); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

// ─────────────────────────── Activate / delete ───────────────────────────

func activateAccount(d Deps) gin.HandlerFunc {
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
		now := time.Now().UTC()
		if err := activateAccountFlow(c, d, accountID, now); err != nil {
			serverError(c, err, d)
			return
		}
		withProfile, err := d.Store.GetAccountWithProfile(c.Request.Context(), accountID)
		if err != nil && err != store.ErrNotFound {
			serverError(c, err, d)
			return
		}
		if withProfile != nil {
			account = withProfile
		}
		c.JSON(http.StatusOK, account)
	}
}

// activateAccountFlow mirrors ActivateAccountAndGrantDefaultPermissions:
// set activated_at and (re)grant the `verified` group membership.
func activateAccountFlow(c *gin.Context, d Deps, accountID uuid.UUID, activatedAt time.Time) error {
	if _, err := d.Store.DB.Exec(c.Request.Context(),
		`UPDATE accounts SET activated_at = $1, updated_at = now()
		 WHERE id = $2 AND (activated_at IS NULL OR activated_at < $1)`, activatedAt, accountID); err != nil {
		return err
	}
	var groupID uuid.UUID
	if err := d.Store.DB.QueryRow(c.Request.Context(),
		`SELECT id FROM permission_groups WHERE "key" = $1 AND deleted_at IS NULL`, "verified").Scan(&groupID); err != nil {
		return err
	}
	if _, err := d.Store.DB.Exec(c.Request.Context(),
		`INSERT INTO permission_group_members (group_id, actor, affected_at, expired_at, created_at, updated_at)
		 VALUES ($1, $2, NULL, NULL, now(), now())
		 ON CONFLICT (group_id, actor) DO UPDATE SET affected_at = NULL, expired_at = NULL, updated_at = now()`,
		groupID, accountID.String()); err != nil {
		return err
	}
	clearActorPermissionCache(d, c, accountID.String())
	return nil
}

func adminDeleteAccount(d Deps) gin.HandlerFunc {
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
		now := time.Now().UTC()
		if _, err := d.Store.DB.Exec(c.Request.Context(),
			`UPDATE auth_sessions SET deleted_at = $1, updated_at = $1 WHERE account_id = $2`, now, accountID); err != nil {
			serverError(c, err, d)
			return
		}
		if _, err := d.Store.DB.Exec(c.Request.Context(),
			`UPDATE accounts SET deleted_at = $1, updated_at = $1 WHERE id = $2 AND deleted_at IS NULL`, now, accountID); err != nil {
			serverError(c, err, d)
			return
		}
		if d.Log != nil {
			d.Log.Warn("admin deleted account", "account", accountID, slog.String("actor", currentUserName(c)))
		}
		// The C# publishes an AccountDeletedEvent on the event bus; Stargate
		// has no such stream yet (gap noted in the phase report).
		c.JSON(http.StatusOK, gin.H{})
	}
}

func currentUserName(c *gin.Context) string {
	user := middleware.CurrentUser(c.Request.Context())
	if user == nil {
		return ""
	}
	return user.Name
}

// ─────────────────────────── helpers ───────────────────────────

func serverError(c *gin.Context, err error, d Deps) {
	if d.Log != nil {
		d.Log.Error("admin request failed", "error", err)
	}
	c.JSON(http.StatusInternalServerError, errs.New("SERVER_ERROR", "An internal server error occurred.", http.StatusInternalServerError))
}

func sortPunishmentsNewestFirst(punishments []model.Punishment) {
	for i := 1; i < len(punishments); i++ {
		for j := i; j > 0 && punishmentNewer(&punishments[j], &punishments[j-1]); j-- {
			punishments[j], punishments[j-1] = punishments[j-1], punishments[j]
		}
	}
}

func punishmentNewer(a, b *model.Punishment) bool {
	at := a.CreatedAt
	bt := b.CreatedAt
	if at == nil {
		return false
	}
	if bt == nil {
		return true
	}
	return at.Time().After(bt.Time())
}

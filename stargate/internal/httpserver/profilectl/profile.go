// Package profilectl ports Passport's profile + social-graph HTTP surface
// into Stargate: the merged GET /api/accounts/me identity, the BasicInfo and
// Profile PATCHes, account-deletion request, public accounts, relationships,
// and followers/following. Account/profile/relationship data lives in
// Stargate's own tables (board stays in Passport); badges, verification,
// perk subscription and file references hydrate via outbound gRPC (degrading
// gracefully when a target is unset).
package profilectl

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"google.golang.org/protobuf/encoding/protojson"
	"src.solsynth.dev/sosys/go/pkg/models"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/go/pkg/errs"
	"src.solsynth.dev/sosys/stargate/internal/actionlog"
	"src.solsynth.dev/sosys/stargate/internal/config"
	"src.solsynth.dev/sosys/stargate/internal/grpcclient"
	"src.solsynth.dev/sosys/stargate/internal/middleware"
	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/permission"
	"src.solsynth.dev/sosys/stargate/internal/redis"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// Deps is the dependency set the profile controllers need.
type Deps struct {
	Store   *store.Store
	Redis   *redis.Client
	Cfg     *config.Config
	Perm    *permission.Service
	Logs    *actionlog.Service
	Clients *grpcclient.Clients
	Log     *slog.Logger
}

// Register mounts the moved Passport profile + social-graph routes on the
// /api group.
func Register(api *gin.RouterGroup, d Deps) {
	me := api.Group("/accounts/me")
	me.GET("", middleware.RequireAuth(), d.getCurrentIdentity)
	me.PATCH("", middleware.RequireAuth(), middleware.AskPermission(d.Perm, permission.AccountsManage), d.updateBasicInfo)
	me.DELETE("", middleware.RequireAuth(), d.requestDeleteAccount)
	me.PATCH("/profile", middleware.RequireAuth(), d.updateProfile)

	accounts := api.Group("/accounts")
	accounts.GET("/id/:id", d.getAccountByID)
	accounts.GET("/search", d.searchAccounts)
	accounts.GET("/:name", d.getAccountByName)
	accounts.GET("/:name/picture", d.getAccountPicture)
	accounts.GET("/:name/background", d.getAccountBackground)
	accounts.GET("/:name/connections", d.getPublicConnections)
	accounts.GET("/:name/followers", d.getFollowPage(false))
	accounts.GET("/:name/following", d.getFollowPage(true))

	registerRelationships(api, d)
}

// ─────────────────────────── shared helpers ───────────────────────────

// notFound mirrors the C# ApiError.NotFound(resource) shape.
func notFound(resource string) *errs.ApiError {
	msg := "The requested resource '" + resource + "' was not found."
	return &errs.ApiError{Code: "NOT_FOUND", Message: msg, Status: http.StatusNotFound, Detail: &resource}
}

func internalError(c *gin.Context, err error) {
	c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Internal server error.", http.StatusInternalServerError))
}

// requireCurrentUser mirrors the C# CurrentUser check: 401 when absent.
func requireCurrentUser(c *gin.Context) *model.Account {
	user := middleware.CurrentUser(c.Request.Context())
	if user == nil {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized("Authentication is required."))
		return nil
	}
	return user
}

// accountIDOf parses the current user's account id (always valid in practice).
func accountIDOf(account *model.Account) uuid.UUID {
	id, _ := uuid.Parse(account.Id)
	return id
}

func parsePagination(c *gin.Context) (offset, take int) {
	take, _ = strconv.Atoi(c.DefaultQuery("take", "20"))
	if take <= 0 {
		take = 20
	}
	if take > 200 {
		take = 200
	}
	offset, _ = strconv.Atoi(c.DefaultQuery("offset", "0"))
	if offset < 0 {
		offset = 0
	}
	return offset, take
}

// logAction writes an action log with the request's user-agent/ip/session,
// mirroring ActionLogService.CreateActionLogFromRequest.
func (d Deps) logAction(c *gin.Context, accountID string, action model.ActionLogType, meta map[string]any) {
	if d.Logs == nil {
		return
	}
	ua := c.Request.UserAgent()
	ip := middleware.ClientIP(c.Request)
	var sessionID *string
	if session := middleware.CurrentSession(c.Request.Context()); session != nil {
		sessionID = &session.Id
	}
	_ = d.Logs.Create(c.Request.Context(), accountID, action, meta, ua, ip, nil, sessionID)
}

// ─────────────────────── hydration (gRPC-backed) ───────────────────────

// enrichOwn hydrates the merged GET /api/accounts/me shape: profile (with
// badges + verification via Passport, perk subscription via Wallet,
// contacts public-only from the local table. A failure to ensure the profile
// row is fatal; the optional gRPC hydrations degrade.
func (d Deps) enrichOwn(ctx context.Context, account *model.Account) error {
	accountID := accountIDOf(account)
	if account.Profile == nil {
		profile, err := d.Store.GetOrCreateAccountProfile(ctx, accountID)
		if err != nil {
			return err
		}
		account.Profile = profile
	}
	if d.Clients != nil && d.Clients.Pass != nil {
		if badges, err := d.listBadges(ctx, account.Id); err == nil {
			account.Badges = badges
		} else {
			d.Log.Warn("badges hydration", "account", account.Id, "error", err)
		}
		if verification, err := d.verificationOf(ctx, account.Id); err == nil && verification != nil {
			account.Profile.Verification = verification
		} else if err != nil {
			d.Log.Warn("verification hydration", "account", account.Id, "error", err)
		}
	}
	d.hydratePerk(ctx, account)
	contacts, err := d.Store.ListPublicContacts(ctx, accountID)
	if err != nil {
		d.Log.Warn("load public contacts", "account", account.Id, "error", err)
	} else {
		account.Contacts = contacts
	}
	return nil
}

// enrichPublic mirrors Passport's EnrichPublicAccountAsync: profile, badges,
// public-only contacts and perk subscription.
func (d Deps) enrichPublic(ctx context.Context, account *model.Account) error {
	accountID := accountIDOf(account)
	if account.Profile == nil {
		profile, err := d.Store.GetOrCreateAccountProfile(ctx, accountID)
		if err != nil {
			return err
		}
		account.Profile = profile
	}
	if d.Clients != nil && d.Clients.Pass != nil {
		if badges, err := d.listBadges(ctx, account.Id); err == nil {
			account.Badges = badges
		} else {
			d.Log.Warn("badges hydration", "account", account.Id, "error", err)
		}
	}
	contacts, err := d.Store.ListPublicContacts(ctx, accountID)
	if err != nil {
		d.Log.Warn("load public contacts", "account", account.Id, "error", err)
	} else {
		account.Contacts = contacts
	}
	d.hydratePerk(ctx, account)
	return nil
}

// hydratePerk populates PerkSubscription/PerkLevel from the wallet service,
// mirroring the C# try/catch (degrade on failure).
func (d Deps) hydratePerk(ctx context.Context, account *model.Account) {
	perk, err := d.perkOf(ctx, account.Id)
	if err != nil {
		d.Log.Warn("perk hydration", "account", account.Id, "error", err)
		account.PerkSubscription = nil
		account.PerkLevel = 0
		return
	}
	if perk == nil {
		account.PerkSubscription = nil
		account.PerkLevel = 0
		return
	}
	account.PerkSubscription = perk
	account.PerkLevel = perk.PerkLevel
}

func (d Deps) perkOf(ctx context.Context, accountID string) (*model.SnSubscriptionReferenceObject, error) {
	if d.Clients == nil || d.Clients.Wallet == nil {
		return nil, nil
	}
	provider := &grpcclient.WalletPerkProvider{Client: d.Clients.Wallet, Log: d.Log}
	return provider.GetPerkSubscription(ctx, accountID)
}

// listBadges hydrates badges via Passport's DyProfileService.ListBadges,
// serializing each badge with the C# SnAccountBadge snake_case wire shape.
func (d Deps) listBadges(ctx context.Context, accountID string) ([]any, error) {
	resp, err := d.Clients.Pass.ListBadges(ctx, &gen.DyListBadgesRequest{AccountId: accountID})
	if err != nil || resp == nil {
		return nil, err
	}
	badges := make([]any, 0, len(resp.Badges))
	for _, badge := range resp.Badges {
		badges = append(badges, badgeToJSON(badge))
	}
	return badges, nil
}

func badgeToJSON(b *gen.DyAccountBadge) map[string]any {
	raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(b)
	if err != nil {
		return map[string]any{"id": b.Id, "type": b.Type, "meta": map[string]any{}}
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return map[string]any{"id": b.Id, "type": b.Type, "meta": map[string]any{}}
	}
	// The Dart SDK strict-casts badge.meta as Map<String, dynamic>
	// (account.g.dart); protojson drops empty maps, so a badge without meta
	// would omit the key and crash the client. Normalize to an empty object.
	if _, ok := m["meta"]; !ok {
		m["meta"] = map[string]any{}
	}
	return m
}

// verificationOf hydrates the profile verification mark via Passport's
// DyProfileService.GetProfile (the mark stays owned by Passport).
func (d Deps) verificationOf(ctx context.Context, accountID string) (*model.SnVerificationMark, error) {
	resp, err := d.Clients.Pass.GetProfile(ctx, &gen.DyGetProfileRequest{AccountId: accountID})
	if err != nil || resp == nil {
		return nil, err
	}
	return verificationFromProto(resp.Verification), nil
}

func verificationFromProto(v *gen.DyVerificationMark) *model.SnVerificationMark {
	if v == nil {
		return nil
	}
	m := &model.SnVerificationMark{Type: int(v.Type)}
	if v.Title != "" {
		m.Title = &v.Title
	}
	if v.Description != "" {
		m.Description = &v.Description
	}
	if v.VerifiedBy != "" {
		m.VerifiedBy = &v.VerifiedBy
	}
	return m
}

// resolveFile fetches a file reference via Drive's DyFileService.GetFile.
// A nil drive client degrades to an error (callers decide how to surface it).
func (d Deps) resolveFile(ctx context.Context, fileID string) (*model.SnCloudFileReferenceObject, error) {
	if d.Clients == nil || d.Clients.Drive == nil {
		return nil, errors.New("drive client not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	file, err := d.Clients.Drive.GetFile(ctx, &gen.DyGetFileRequest{Id: fileID})
	if err != nil || file == nil {
		return nil, err
	}
	return fileRefFromProto(file), nil
}

func fileRefFromProto(f *gen.DyCloudFile) *model.SnCloudFileReferenceObject {
	ref := models.FromProtoValue(f)
	return &ref
}

// fileURL builds the redirect target for {name}/picture|background. The C#
// uses the FileUrl config; Stargate has no such key so the stored file
// reference URL is preferred, with a BaseUrl fallback.
func fileURL(cfg *config.Config, ref *model.SnCloudFileReferenceObject) string {
	if ref.Url != "" {
		return ref.Url
	}
	return cfg.BaseUrl + "/files/" + ref.Id
}

// resolveAccount mirrors AccountPublicController's lookup: GUID probes load
// by id, anything else is a case-insensitive name lookup.
func (d Deps) resolveAccount(ctx context.Context, probe string) (*model.Account, error) {
	if id, err := uuid.Parse(probe); err == nil {
		return d.Store.GetAccountWithProfile(ctx, id)
	}
	return d.Store.GetAccountWithProfileByNameFold(ctx, probe)
}

// isProfileComplete mirrors the C# IsProfileComplete helper.
func isProfileComplete(p *model.Profile) bool {
	return nonBlank(p.FirstName) && nonBlank(p.LastName) && nonBlank(p.Bio) &&
		nonBlank(p.Location) && nonBlank(p.Pronouns) && p.Birthday != nil && p.Picture != nil
}

func nonBlank(s *string) bool {
	return s != nil && strings.TrimSpace(*s) != ""
}

// ─────────────────────── GET /api/accounts/me ───────────────────────

// getCurrentIdentity is the MERGED variant serving both
// /padlock/accounts/me and /passport/accounts/me: hydrated SnAccount
// (account + profile + badges + verification + perk + contacts).
func (d Deps) getCurrentIdentity(c *gin.Context) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	ctx := c.Request.Context()
	account, err := d.Store.GetAccountWithProfile(ctx, accountIDOf(user))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			c.JSON(http.StatusNotFound, errs.New("PADLOCK_ACCOUNT_NOT_FOUND", "Account not found.", http.StatusNotFound))
			return
		}
		internalError(c, err)
		return
	}
	if err := d.enrichOwn(ctx, account); err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, account)
}

// basicInfoRequest mirrors Padlock's BasicInfoRequest.
type basicInfoRequest struct {
	Nick     *string `json:"nick"`
	Language *string `json:"language"`
	Region   *string `json:"region"`
}

// updateBasicInfo ports Padlock's PATCH /api/accounts/me (BasicInfo).
func (d Deps) updateBasicInfo(c *gin.Context) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	var req basicInfoRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{"body": {"Invalid request body."}}))
		return
	}
	if (req.Nick != nil && len(*req.Nick) > 256) ||
		(req.Language != nil && len(*req.Language) > 32) ||
		(req.Region != nil && len(*req.Region) > 32) {
		c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{"body": {"Field exceeds the maximum allowed length."}}))
		return
	}
	ctx := c.Request.Context()
	account, err := d.Store.UpdateAccountBasicInfo(ctx, accountIDOf(user), req.Nick, req.Language, req.Region)
	if err != nil {
		internalError(c, err)
		return
	}
	fields := make([]string, 0, 3)
	if req.Nick != nil {
		fields = append(fields, "nick")
	}
	if req.Language != nil {
		fields = append(fields, "language")
	}
	if req.Region != nil {
		fields = append(fields, "region")
	}
	if len(fields) > 0 {
		d.logAction(c, user.Id, model.ActionLogAccountProfileUpdate, map[string]any{"fields": fields})
	}
	c.JSON(http.StatusOK, account)
}

// profileRequest mirrors Passport's ProfileRequest.
type profileRequest struct {
	FirstName     *string              `json:"first_name"`
	MiddleName    *string              `json:"middle_name"`
	LastName      *string              `json:"last_name"`
	Gender        *string              `json:"gender"`
	Pronouns      *string              `json:"pronouns"`
	TimeZone      *string              `json:"time_zone"`
	Location      *string              `json:"location"`
	Bio           *string              `json:"bio"`
	UsernameColor *model.UsernameColor `json:"username_color"`
	Birthday      *model.Time          `json:"birthday"`
	Links         []model.Link         `json:"links"`
	PictureId     *string              `json:"picture_id"`
	BackgroundId  *string              `json:"background_id"`
}

// updateProfile ports Passport's PATCH /api/accounts/me/profile.
func (d Deps) updateProfile(c *gin.Context) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	var req profileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{"body": {"Invalid request body."}}))
		return
	}
	// MaxLength attributes from the C# ProfileRequest.
	if (req.FirstName != nil && len(*req.FirstName) > 256) ||
		(req.MiddleName != nil && len(*req.MiddleName) > 256) ||
		(req.LastName != nil && len(*req.LastName) > 256) ||
		(req.Gender != nil && len(*req.Gender) > 1024) ||
		(req.Pronouns != nil && len(*req.Pronouns) > 1024) ||
		(req.TimeZone != nil && len(*req.TimeZone) > 1024) ||
		(req.Location != nil && len(*req.Location) > 1024) ||
		(req.Bio != nil && len(*req.Bio) > 4096) ||
		(req.PictureId != nil && len(*req.PictureId) > 32) ||
		(req.BackgroundId != nil && len(*req.BackgroundId) > 32) {
		c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{"body": {"Field exceeds the maximum allowed length."}}))
		return
	}

	ctx := c.Request.Context()
	accountID := accountIDOf(user)
	profile, err := d.Store.GetOrCreateAccountProfile(ctx, accountID)
	if err != nil {
		internalError(c, err)
		return
	}
	changed := make([]string, 0, 12)
	if req.FirstName != nil {
		profile.FirstName = req.FirstName
		changed = append(changed, "first_name")
	}
	if req.MiddleName != nil {
		profile.MiddleName = req.MiddleName
		changed = append(changed, "middle_name")
	}
	if req.LastName != nil {
		profile.LastName = req.LastName
		changed = append(changed, "last_name")
	}
	if req.Bio != nil {
		profile.Bio = req.Bio
		changed = append(changed, "bio")
	}
	if req.Gender != nil {
		profile.Gender = req.Gender
		changed = append(changed, "gender")
	}
	if req.Pronouns != nil {
		profile.Pronouns = req.Pronouns
		changed = append(changed, "pronouns")
	}
	if req.Birthday != nil {
		profile.Birthday = req.Birthday
		changed = append(changed, "birthday")
	}
	if req.Location != nil {
		profile.Location = req.Location
		changed = append(changed, "location")
	}
	if req.TimeZone != nil {
		profile.TimeZone = req.TimeZone
		changed = append(changed, "time_zone")
	}
	if req.Links != nil {
		profile.Links = req.Links
		changed = append(changed, "links")
	}
	if req.UsernameColor != nil {
		profile.UsernameColor = req.UsernameColor
		changed = append(changed, "username_color")
	}
	if req.PictureId != nil {
		if d.Clients == nil || d.Clients.Drive == nil {
			d.Log.Warn("drive client unset; skipping picture resolution")
		} else {
			file, err := d.resolveFile(ctx, *req.PictureId)
			if err != nil {
				d.Log.Warn("resolve picture", "account", user.Id, "file", *req.PictureId, "error", err)
				internalError(c, err)
				return
			}
			profile.Picture = file
			changed = append(changed, "picture")
		}
	}
	if req.BackgroundId != nil {
		if d.Clients == nil || d.Clients.Drive == nil {
			d.Log.Warn("drive client unset; skipping background resolution")
		} else {
			file, err := d.resolveFile(ctx, *req.BackgroundId)
			if err != nil {
				d.Log.Warn("resolve background", "account", user.Id, "file", *req.BackgroundId, "error", err)
				internalError(c, err)
				return
			}
			profile.Background = file
			changed = append(changed, "background")
		}
	}
	if err := d.Store.SaveProfile(ctx, profile); err != nil {
		internalError(c, err)
		return
	}
	profile.UpdatedAt = model.NewTime(time.Now())
	if len(changed) > 0 {
		d.logAction(c, user.Id, model.ActionLogAccountProfileUpdate, map[string]any{"fields": changed})
	}
	if req.PictureId != nil && profile.Picture != nil {
		d.logAction(c, user.Id, model.ActionLogAccountAvatar, map[string]any{})
	}
	if isProfileComplete(profile) {
		d.logAction(c, user.Id, model.ActionLogAccountProfileComplete, map[string]any{})
	}
	c.JSON(http.StatusOK, profile)
}

// requestDeleteAccount ports Passport's DELETE /api/accounts/me. The C#
// creates an account_removal magic spell with a 24h prevent-repeat gate;
// Stargate replicates that gate with a Redis flag (the magic-spell email flow
// stays in Passport).
func (d Deps) requestDeleteAccount(c *gin.Context) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	ctx := c.Request.Context()
	if d.Redis == nil || !d.Redis.Available() {
		c.JSON(http.StatusServiceUnavailable, errs.New("SERVICE_UNAVAILABLE", "This feature requires the cache service.", http.StatusServiceUnavailable))
		return
	}
	key := "accounts:deletion:" + user.Id
	found, err := d.Redis.Cache.HasFlag(ctx, key)
	if err != nil {
		internalError(c, err)
		return
	}
	if found {
		c.JSON(http.StatusBadRequest, errs.New("PASSPORT_ACCOUNT_DELETION_TOO_MANY_REQUESTS",
			"You already requested account deletion within 24 hours.", http.StatusBadRequest))
		return
	}
	if err := d.Redis.Cache.SetFlag(ctx, key, 24*time.Hour); err != nil {
		internalError(c, err)
		return
	}
	c.Status(http.StatusOK)
}

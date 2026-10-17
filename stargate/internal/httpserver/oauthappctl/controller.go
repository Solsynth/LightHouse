// Package oauthappctl mounts the OAuth/OIDC app surface owned by
// app.solian.id.oauthapps: the client registrations Stargate keeps for
// itself, either linked to a Develop project or owned by a single account.
package oauthappctl

import (
	"context"
	"crypto/rand"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/stargate/internal/middleware"
	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/permission"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// Deps carries the dependencies the OAuth app controllers need.
type Deps struct {
	Store *store.Store
	Perm  *permission.Service
	Log   *slog.Logger
	// Invalidate drops the OIDC provider's cached view of a client. An app
	// edit must be visible to the provider immediately, not after its TTL.
	Invalidate func(ctx context.Context, appID string, slugs ...string)
}

type controller struct {
	d Deps
}

// invalidate drops the cached client for the app, including a slug the app
// no longer uses (a rename must not keep serving the old identifier).
func (c *controller) invalidate(ctx context.Context, app *model.OAuthApp, previousSlug string) {
	if c.d.Invalidate == nil || app == nil {
		return
	}
	slugs := []string{app.Slug}
	if previousSlug != "" && previousSlug != app.Slug {
		slugs = append(slugs, previousSlug)
	}
	c.d.Invalidate(ctx, app.Id, slugs...)
}

// CacheKeyPrefixClientID and CacheKeyPrefixClientSlug are the OIDC
// provider's client-cache key prefixes. The module supplies an invalidator
// built from them so an app edit is visible immediately.
const (
	CacheKeyPrefixClientID   = "auth:oidc-client:id:"
	CacheKeyPrefixClientSlug = "auth:oidc-client:slug:"
)

// Register mounts the OAuth app routes on the /api router group.
func Register(api *gin.RouterGroup, d Deps) {
	c := &controller{d: d}

	apps := api.Group("/oauth-apps", middleware.RequireAuth())
	apps.GET("", c.listApps)
	apps.POST("", c.createApp)
	apps.GET("/:appId", c.getApp)
	apps.PATCH("/:appId", c.updateApp)
	apps.DELETE("/:appId", middleware.RequireSudo(), c.deleteApp)

	secrets := apps.Group("/:appId/secrets")
	secrets.GET("", c.listSecrets)
	secrets.POST("", middleware.RequireSudo(), c.createSecret)
	secrets.GET("/:secretId", c.getSecret)
	secrets.DELETE("/:secretId", middleware.RequireSudo(), c.deleteSecret)
	secrets.POST("/:secretId/rotate", middleware.RequireSudo(), c.rotateSecret)
}

// ── Requests ───────────────────────────────────────────────────────────────

type oauthConfigRequest struct {
	ClientUri              *string   `json:"client_uri"`
	RedirectUris           *[]string `json:"redirect_uris"`
	PostLogoutRedirectUris *[]string `json:"post_logout_redirect_uris"`
	AllowedScopes          *[]string `json:"allowed_scopes"`
	AllowedGrantTypes      *[]string `json:"allowed_grant_types"`
	RequirePkce            *bool     `json:"require_pkce"`
	AllowOfflineAccess     *bool     `json:"allow_offline_access"`
	IsPublicClient         *bool     `json:"is_public_client"`
}

type oauthLinksRequest struct {
	HomePage       *string `json:"home_page"`
	PrivacyPolicy  *string `json:"privacy_policy"`
	TermsOfService *string `json:"terms_of_service"`
}

type appRequest struct {
	Slug        *string             `json:"slug"`
	Name        *string             `json:"name"`
	Description *string             `json:"description"`
	Status      *int                `json:"status"`
	ProjectId   *string             `json:"project_id"`
	OauthConfig *oauthConfigRequest `json:"oauth_config"`
	Links       *oauthLinksRequest  `json:"links"`
}

type secretRequest struct {
	Description *string `json:"description"`
	// ExpiresIn accepts a Go duration string ("720h") or a plain number of
	// seconds, matching the C# TimeSpan request shape.
	ExpiresIn *durationValue `json:"expires_in"`
	// Type selects "oidc" (the default) or "api_key".
	Type *string `json:"type"`
}

type durationValue time.Duration

func (d *durationValue) UnmarshalJSON(data []byte) error {
	text := strings.Trim(string(data), `"`)
	if text == "" || text == "null" {
		*d = 0
		return nil
	}
	if seconds, err := strconv.ParseFloat(text, 64); err == nil {
		*d = durationValue(seconds * float64(time.Second))
		return nil
	}
	parsed, err := time.ParseDuration(text)
	if err != nil {
		return err
	}
	*d = durationValue(parsed)
	return nil
}

// ── Apps ───────────────────────────────────────────────────────────────────

// GET /api/oauth-apps
func (c *controller) listApps(ctx *gin.Context) {
	reqCtx := ctx.Request.Context()
	user := middleware.CurrentUser(reqCtx)
	if user == nil {
		ctx.Status(http.StatusUnauthorized)
		return
	}

	var projectID *string
	if value := strings.TrimSpace(ctx.Query("projectId")); value != "" {
		// Listing a project's apps is a developer-team operation.
		if err := c.requirePermission(reqCtx, *user, permission.CustomAppsUpdate); err != nil {
			respondError(ctx, err)
			return
		}
		projectID = &value
	}

	apps, err := c.d.Store.ListOAuthApps(reqCtx, nil, projectID)
	if err != nil {
		jsonError(ctx, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to load the OAuth apps.")
		return
	}
	if projectID == nil {
		// Without a project filter the caller sees only what it owns.
		apps = ownedApps(apps, user.Id)
	}
	ctx.Header("X-Total", strconv.Itoa(len(apps)))
	ctx.JSON(http.StatusOK, apps)
}

func ownedApps(apps []*model.OAuthApp, accountID string) []*model.OAuthApp {
	out := make([]*model.OAuthApp, 0, len(apps))
	for _, app := range apps {
		if app.AccountId != nil && *app.AccountId == accountID {
			out = append(out, app)
		}
	}
	return out
}

// POST /api/oauth-apps
func (c *controller) createApp(ctx *gin.Context) {
	reqCtx := ctx.Request.Context()
	user := middleware.CurrentUser(reqCtx)
	if user == nil {
		ctx.Status(http.StatusUnauthorized)
		return
	}
	var request appRequest
	if err := ctx.ShouldBindJSON(&request); err != nil {
		jsonError(ctx, http.StatusBadRequest, "OAUTH_APP_REQUEST_INVALID", err.Error())
		return
	}
	app := &model.OAuthApp{Id: uuid.New().String()}
	if err := c.applyRequest(reqCtx, &request, app, user); err != nil {
		respondError(ctx, err)
		return
	}
	if err := c.d.Store.CreateOAuthApp(reqCtx, app); err != nil {
		jsonError(ctx, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to create the OAuth app.")
		return
	}
	c.invalidate(reqCtx, app, "")
	ctx.JSON(http.StatusCreated, app)
}

// GET /api/oauth-apps/:appId
func (c *controller) getApp(ctx *gin.Context) {
	reqCtx := ctx.Request.Context()
	user := middleware.CurrentUser(reqCtx)
	if user == nil {
		ctx.Status(http.StatusUnauthorized)
		return
	}
	app, err := c.d.Store.GetOAuthAppByIdentifier(reqCtx, ctx.Param("appId"))
	if err != nil {
		respondError(ctx, errNotFound)
		return
	}
	if err := c.authorize(reqCtx, *user, app, permission.CustomAppsUpdate); err != nil {
		respondError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, app)
}

// PATCH /api/oauth-apps/:appId
func (c *controller) updateApp(ctx *gin.Context) {
	reqCtx := ctx.Request.Context()
	user := middleware.CurrentUser(reqCtx)
	if user == nil {
		ctx.Status(http.StatusUnauthorized)
		return
	}
	app, err := c.d.Store.GetOAuthAppByIdentifier(reqCtx, ctx.Param("appId"))
	if err != nil {
		respondError(ctx, errNotFound)
		return
	}
	if err := c.authorize(reqCtx, *user, app, permission.CustomAppsUpdate); err != nil {
		respondError(ctx, err)
		return
	}
	var request appRequest
	if err := ctx.ShouldBindJSON(&request); err != nil {
		jsonError(ctx, http.StatusBadRequest, "OAUTH_APP_REQUEST_INVALID", err.Error())
		return
	}
	previousSlug := app.Slug
	if err := c.applyRequest(reqCtx, &request, app, user); err != nil {
		respondError(ctx, err)
		return
	}
	if err := c.d.Store.UpdateOAuthApp(reqCtx, app); err != nil {
		jsonError(ctx, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to update the OAuth app.")
		return
	}
	c.invalidate(reqCtx, app, previousSlug)
	ctx.JSON(http.StatusOK, app)
}

// DELETE /api/oauth-apps/:appId
func (c *controller) deleteApp(ctx *gin.Context) {
	reqCtx := ctx.Request.Context()
	user := middleware.CurrentUser(reqCtx)
	if user == nil {
		ctx.Status(http.StatusUnauthorized)
		return
	}
	app, err := c.d.Store.GetOAuthAppByIdentifier(reqCtx, ctx.Param("appId"))
	if err != nil {
		respondError(ctx, errNotFound)
		return
	}
	if err := c.authorize(reqCtx, *user, app, permission.CustomAppsDelete); err != nil {
		respondError(ctx, err)
		return
	}
	if err := c.d.Store.DeleteOAuthApp(reqCtx, app.Id); err != nil {
		jsonError(ctx, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to delete the OAuth app.")
		return
	}
	c.invalidate(reqCtx, app, "")
	ctx.Status(http.StatusNoContent)
}

// applyRequest merges a create or update request into an app and validates
// the result. Ownership is assigned on create only: a patch never moves an
// app between owners.
func (c *controller) applyRequest(ctx context.Context, request *appRequest, app *model.OAuthApp, user *model.Account) error {
	creating := app.Slug == ""

	if request.Slug != nil || creating {
		slug := strings.TrimSpace(valueOr(request.Slug, app.Slug))
		if slug == "" {
			return errSlugRequired
		}
		if slug != app.Slug {
			count, err := c.d.Store.CountOAuthAppsWithSlug(ctx, slug)
			if err != nil {
				return err
			}
			if count > 0 {
				return errSlugTaken
			}
		}
		app.Slug = slug
	}
	if request.Name != nil || creating {
		name := strings.TrimSpace(valueOr(request.Name, app.Name))
		if name == "" {
			return errNameRequired
		}
		app.Name = name
	}
	if request.Description != nil {
		app.Description = *request.Description
	}
	if request.Status != nil {
		app.Status = *request.Status
	}
	switch {
	case creating:
		if err := c.assignOwnership(request, app, user); err != nil {
			return err
		}
	case request.ProjectId != nil:
		// Moving an existing app to another owner is not supported.
		return errOwnerAmbiguous
	}
	if request.OauthConfig != nil {
		applyOauthConfig(request.OauthConfig, app)
	}
	if request.Links != nil {
		if request.Links.HomePage != nil {
			app.Links.HomePage = *request.Links.HomePage
		}
		if request.Links.PrivacyPolicy != nil {
			app.Links.PrivacyPolicy = *request.Links.PrivacyPolicy
		}
		if request.Links.TermsOfService != nil {
			app.Links.TermsOfService = *request.Links.TermsOfService
		}
	}
	return validateApp(app)
}

// assignOwnership links a new app to a project when one is given, and
// otherwise makes the calling account its individual owner.
func (c *controller) assignOwnership(request *appRequest, app *model.OAuthApp, user *model.Account) error {
	projectID := strings.TrimSpace(valueOr(request.ProjectId, ""))
	if projectID == "" {
		if user == nil || user.Id == "" {
			return errOwnerRequired
		}
		owner := user.Id
		app.AccountId = &owner
		return nil
	}
	if _, err := uuid.Parse(projectID); err != nil {
		return errRequestInvalid
	}
	app.ProjectId = &projectID
	return nil
}

func applyOauthConfig(request *oauthConfigRequest, app *model.OAuthApp) {
	if request.ClientUri != nil {
		app.OauthConfig.ClientUri = strings.TrimSpace(*request.ClientUri)
	}
	if request.RedirectUris != nil {
		app.OauthConfig.RedirectUris = cleanList(*request.RedirectUris)
	}
	if request.PostLogoutRedirectUris != nil {
		app.OauthConfig.PostLogoutRedirectUris = cleanList(*request.PostLogoutRedirectUris)
	}
	if request.AllowedScopes != nil {
		app.OauthConfig.AllowedScopes = cleanList(*request.AllowedScopes)
	}
	if request.AllowedGrantTypes != nil {
		app.OauthConfig.AllowedGrantTypes = cleanList(*request.AllowedGrantTypes)
	}
	if request.RequirePkce != nil {
		app.OauthConfig.RequirePkce = *request.RequirePkce
	}
	if request.AllowOfflineAccess != nil {
		app.OauthConfig.AllowOfflineAccess = *request.AllowOfflineAccess
	}
	if request.IsPublicClient != nil {
		app.OauthConfig.IsPublicClient = *request.IsPublicClient
	}
}

// validateApp enforces the invariants the OIDC provider relies on.
func validateApp(app *model.OAuthApp) error {
	if app.Name == "" || app.Slug == "" {
		return errNameRequired
	}
	if len(app.OauthConfig.AllowedScopes) == 0 {
		return errScopesRequired
	}
	// Only a production client (status 2) enforces its redirect list, which
	// is exactly what the provider does at authorize time.
	if app.Status == customAppStatusProduction && len(app.OauthConfig.RedirectUris) == 0 {
		return errRedirectMissing
	}
	return nil
}

// authorize reports whether the account may manage the app: its individual
// owner, or anyone holding the developer permission for a project-linked app.
func (c *controller) authorize(ctx context.Context, user model.Account, app *model.OAuthApp, key string) error {
	if app.AccountId != nil && *app.AccountId == user.Id {
		return nil
	}
	if app.ProjectId != nil {
		return c.requirePermission(ctx, user, key)
	}
	return errForbidden
}

func (c *controller) requirePermission(ctx context.Context, user model.Account, key string) error {
	if c.d.Perm == nil {
		return errForbidden
	}
	id, err := uuid.Parse(user.Id)
	if err != nil {
		return errUnauthorized
	}
	allowed, err := c.d.Perm.HasPermission(ctx, id, key)
	if err != nil || !allowed {
		return errForbidden
	}
	return nil
}

// ── Secrets ────────────────────────────────────────────────────────────────

// GET /api/oauth-apps/:appId/secrets
func (c *controller) listSecrets(ctx *gin.Context) {
	reqCtx := ctx.Request.Context()
	user := middleware.CurrentUser(reqCtx)
	if user == nil {
		ctx.Status(http.StatusUnauthorized)
		return
	}
	app, err := c.loadApp(reqCtx, ctx, user, permission.CustomAppsUpdate)
	if err != nil {
		respondError(ctx, err)
		return
	}
	secrets, err := c.d.Store.ListOAuthAppSecrets(reqCtx, app.Id)
	if err != nil {
		jsonError(ctx, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to load the app secrets.")
		return
	}
	ctx.Header("X-Total", strconv.Itoa(len(secrets)))
	ctx.JSON(http.StatusOK, secrets)
}

// POST /api/oauth-apps/:appId/secrets — the plaintext is returned once.
func (c *controller) createSecret(ctx *gin.Context) {
	reqCtx := ctx.Request.Context()
	user := middleware.CurrentUser(reqCtx)
	if user == nil {
		ctx.Status(http.StatusUnauthorized)
		return
	}
	app, err := c.loadApp(reqCtx, ctx, user, permission.CustomAppsSecretsManage)
	if err != nil {
		respondError(ctx, err)
		return
	}
	var request secretRequest
	if err := ctx.ShouldBindJSON(&request); err != nil {
		jsonError(ctx, http.StatusBadRequest, "OAUTH_APP_REQUEST_INVALID", err.Error())
		return
	}
	secret := &model.OAuthAppSecret{
		Id:     uuid.New().String(),
		AppId:  app.Id,
		Secret: GenerateSecret(),
		IsOidc: secretIsOidc(request.Type),
	}
	if request.Description != nil {
		secret.Description = *request.Description
	}
	if request.ExpiresIn != nil && time.Duration(*request.ExpiresIn) > 0 {
		expires := model.NewTime(time.Now().UTC().Add(time.Duration(*request.ExpiresIn)))
		secret.ExpiredAt = expires
	}
	if err := c.d.Store.CreateOAuthAppSecret(reqCtx, secret); err != nil {
		jsonError(ctx, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to create the app secret.")
		return
	}
	c.invalidate(reqCtx, app, "")
	ctx.JSON(http.StatusCreated, secret)
}

// GET /api/oauth-apps/:appId/secrets/:secretId
func (c *controller) getSecret(ctx *gin.Context) {
	reqCtx := ctx.Request.Context()
	user := middleware.CurrentUser(reqCtx)
	if user == nil {
		ctx.Status(http.StatusUnauthorized)
		return
	}
	app, err := c.loadApp(reqCtx, ctx, user, permission.CustomAppsUpdate)
	if err != nil {
		respondError(ctx, err)
		return
	}
	secret, err := c.d.Store.GetOAuthAppSecret(reqCtx, app.Id, ctx.Param("secretId"))
	if err != nil {
		respondError(ctx, errSecretNotFound)
		return
	}
	// The stored value is never returned after creation or rotation.
	secret.Secret = ""
	ctx.JSON(http.StatusOK, secret)
}

// DELETE /api/oauth-apps/:appId/secrets/:secretId
func (c *controller) deleteSecret(ctx *gin.Context) {
	reqCtx := ctx.Request.Context()
	user := middleware.CurrentUser(reqCtx)
	if user == nil {
		ctx.Status(http.StatusUnauthorized)
		return
	}
	app, err := c.loadApp(reqCtx, ctx, user, permission.CustomAppsSecretsManage)
	if err != nil {
		respondError(ctx, err)
		return
	}
	if err := c.d.Store.DeleteOAuthAppSecret(reqCtx, app.Id, ctx.Param("secretId")); err != nil {
		jsonError(ctx, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to delete the app secret.")
		return
	}
	c.invalidate(reqCtx, app, "")
	ctx.Status(http.StatusNoContent)
}

// POST /api/oauth-apps/:appId/secrets/:secretId/rotate — a new plaintext is
// generated and returned once.
func (c *controller) rotateSecret(ctx *gin.Context) {
	reqCtx := ctx.Request.Context()
	user := middleware.CurrentUser(reqCtx)
	if user == nil {
		ctx.Status(http.StatusUnauthorized)
		return
	}
	app, err := c.loadApp(reqCtx, ctx, user, permission.CustomAppsSecretsManage)
	if err != nil {
		respondError(ctx, err)
		return
	}
	var request secretRequest
	if err := ctx.ShouldBindJSON(&request); err != nil {
		jsonError(ctx, http.StatusBadRequest, "OAUTH_APP_REQUEST_INVALID", err.Error())
		return
	}
	description := ""
	if request.Description != nil {
		description = *request.Description
	}
	secret, err := c.d.Store.RotateOAuthAppSecret(reqCtx, app.Id, ctx.Param("secretId"), GenerateSecret(), description)
	if err != nil {
		respondError(ctx, errSecretNotFound)
		return
	}
	c.invalidate(reqCtx, app, "")
	ctx.JSON(http.StatusOK, secret)
}

// loadApp resolves the app from the path and authorizes the caller for the
// given permission key.
func (c *controller) loadApp(ctx context.Context, ginCtx *gin.Context, user *model.Account, key string) (*model.OAuthApp, error) {
	app, err := c.d.Store.GetOAuthAppByIdentifier(ctx, ginCtx.Param("appId"))
	if err != nil {
		return nil, errNotFound
	}
	if err := c.authorize(ctx, *user, app, key); err != nil {
		return nil, err
	}
	return app, nil
}

// ── Helpers ────────────────────────────────────────────────────────────────

func valueOr(value *string, fallback string) string {
	if value == nil {
		return fallback
	}
	return *value
}

func cleanList(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, dup := seen[trimmed]; dup {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func secretIsOidc(kind *string) bool {
	if kind == nil {
		return true
	}
	return !strings.EqualFold(strings.TrimSpace(*kind), "api_key")
}

// secretAlphabet mirrors CustomAppService.GenerateRandomSecret: 64 CSPRNG
// characters from the OAuth-safe alphabet. The modulo bias is preserved
// deliberately — this service replaces the C# issuer for the same values.
const secretAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890-._~+"

// GenerateSecret returns a fresh 64-character client secret.
func GenerateSecret() string {
	buffer := make([]byte, 64)
	if _, err := rand.Read(buffer); err != nil {
		panic("oauthappctl: " + err.Error())
	}
	out := make([]byte, len(buffer))
	for i, value := range buffer {
		out[i] = secretAlphabet[int(value)%len(secretAlphabet)]
	}
	return string(out)
}

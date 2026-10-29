// Package oauthapps owns Stargate's OAuth/OIDC client registry: the app
// registrations the provider authorizes against.
//
// A registration is either project-linked (a Develop dev project) or owned
// by an individual account, so a deployment can run the OAuth provider
// without the Develop service. The module publishes the oauthClients port
// and resolves in the precedence the provider already documented:
// deployment configuration, then its own table, then the Develop
// integration.
package oauthapps

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gen "src.solsynth.dev/sosys/go/proto"
	"src.solsynth.dev/sosys/stargate/internal/config"
	"src.solsynth.dev/sosys/stargate/internal/httpserver/oauthappctl"
	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/module"
	"src.solsynth.dev/sosys/stargate/internal/modules/common"
	"src.solsynth.dev/sosys/stargate/internal/modules/integrations"
	"src.solsynth.dev/sosys/stargate/internal/ports"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// moduleID is this module's identifier, used for the factory, port
// attribution and import bookkeeping.
const moduleID = "app.solian.id.oauthapps"

// Factory returns the compiled module descriptor.
func Factory() module.Factory {
	return module.Factory{
		ID:             moduleID,
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Requires:       []string{"app.solian.id.auth"},
		Capabilities:   []string{"oauth.apps"},
		Models: []any{
			&store.OAuthAppEntity{},
			&store.OAuthAppSecretEntity{},
		},
		SQL:      []module.SchemaHook{appIndexes},
		Provides: []module.PortID{ports.PortOAuthClients},
		Importer: func() module.Importer { return &importer{} },
		New:      func() module.Instance { return &instance{} },
	}
}

// appIndexes adds what AutoMigrate cannot express: an app must have exactly
// one owner (a Develop project or an individual account, never both or
// neither). The slug uniqueness and lookup indexes come from the entity
// tags, so they are not duplicated here.
func appIndexes(ctx context.Context, db *gorm.DB) error {
	statements := []string{
		`ALTER TABLE oauth_apps DROP CONSTRAINT IF EXISTS ck_oauth_apps_owner`,
		`ALTER TABLE oauth_apps ADD CONSTRAINT ck_oauth_apps_owner
		 CHECK (num_nonnulls(project_id, account_id) = 1)`,
	}
	for _, statement := range statements {
		if err := db.WithContext(ctx).Exec(statement).Error; err != nil {
			return err
		}
	}
	return nil
}

type instance struct{}

// ProvidePorts publishes the client registry before any module configures,
// so the OIDC provider resolves clients regardless of Configure order.
func (i *instance) ProvidePorts(_ context.Context, host *module.Host) error {
	if host == nil || host.Ports == nil {
		return errors.New("oauthapps: missing host port registry")
	}
	// Register once: the adapter also implements OAuthClientsSlugged, and a
	// second Provide would make Resolve report two feature providers for the
	// same port (which FromRegistry would silently drop).
	impl := &adapter{src: newSource(host)}
	module.Provide[ports.OAuthClients](host.Ports, ports.PortOAuthClients, moduleID, module.KindFeature, impl)
	return nil
}

func (i *instance) Configure(_ context.Context, host *module.Host) error {
	oauthappctl.Register(host.API, common.OAuthApp(host.Core, ports.FromRegistry(host.Ports)))
	// Token validation resolves an OAuth session's client slug. Point it at
	// this module's registry so a Stargate-owned app validates without the
	// Develop service.
	if host.Core != nil && host.Core.Token != nil {
		host.Core.Token.SetCustomAppProvider(&slugProvider{src: newSource(host)})
	}
	return nil
}

// slugProvider adapts the client source to auth.CustomAppProvider.
type slugProvider struct {
	src *source
}

func (p *slugProvider) GetCustomAppSlug(ctx context.Context, appID string) (string, error) {
	if p == nil || p.src == nil {
		return "", nil
	}
	app, err := p.src.Get(ctx, appID)
	if err != nil || app == nil {
		return "", err
	}
	return app.Slug, nil
}

func (i *instance) Stop(context.Context) error { return nil }

// ── Client resolution ──────────────────────────────────────────────────────

// source resolves a client from configuration, the module's own table and
// Develop, in that order. Every field may be absent: a standalone
// deployment has no Develop client and no configured clients.
type source struct {
	cfg     *config.Config
	store   *store.Store
	develop gen.DyCustomAppServiceClient
	log     *slog.Logger
}

func newSource(host *module.Host) *source {
	src := &source{}
	if host == nil {
		return src
	}
	if cfg, ok := host.Config().(*config.Config); ok {
		src.cfg = cfg
	}
	if host.Core != nil {
		src.store = host.Core.Store
		src.log = host.Core.Log
		if host.Core.Clients != nil {
			src.develop = host.Core.Clients.Develop
		}
	}
	return src
}

// Get resolves a client by ID.
func (s *source) Get(ctx context.Context, id string) (*ports.OAuthApp, error) {
	if s == nil || id == "" {
		return nil, nil
	}
	if s.cfg != nil {
		if client := s.cfg.FindLocalOAuthClientByID(id); client != nil {
			return integrations.LocalOAuthApp(client), nil
		}
	}
	if app, err := s.fromStore(ctx, id); err != nil || app != nil {
		return app, err
	}
	return s.fromDevelop(ctx, id, "")
}

// GetBySlug resolves a client by its human-readable slug.
func (s *source) GetBySlug(ctx context.Context, slug string) (*ports.OAuthApp, error) {
	if s == nil || slug == "" {
		return nil, nil
	}
	if s.cfg != nil {
		if client := s.cfg.FindLocalOAuthClient(slug); client != nil {
			return integrations.LocalOAuthApp(client), nil
		}
	}
	if s.store != nil {
		app, err := s.store.GetOAuthAppBySlug(ctx, slug)
		switch {
		case err == nil:
			return appToPort(app), nil
		case !errors.Is(err, store.ErrNotFound):
			return nil, err
		}
	}
	return s.fromDevelop(ctx, "", slug)
}

// CheckSecret verifies a presented client secret.
func (s *source) CheckSecret(ctx context.Context, appID, secret string) (bool, error) {
	if s == nil || appID == "" {
		return false, nil
	}
	if s.cfg != nil {
		if client := s.cfg.FindLocalOAuthClientByID(appID); client != nil {
			if client.ClientSecret == "" {
				return false, nil
			}
			return subtle.ConstantTimeCompare([]byte(client.ClientSecret), []byte(secret)) == 1, nil
		}
	}
	if s.store != nil {
		app, err := s.store.GetOAuthAppByIdentifier(ctx, appID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return false, err
		}
		if app != nil {
			found, err := s.store.FindValidOAuthAppSecret(ctx, app.Id, secret, true)
			if err != nil {
				return false, err
			}
			return found != nil, nil
		}
	}
	if s.develop == nil {
		return false, nil
	}
	isOidc := true
	response, err := s.develop.CheckCustomAppSecret(ctx, &gen.DyCheckCustomAppSecretRequest{
		SecretIdentifier: &gen.DyCheckCustomAppSecretRequest_AppId{AppId: appID},
		Secret:           secret,
		IsOidc:           &isOidc,
	})
	if err != nil {
		return false, err
	}
	return response.GetValid(), nil
}

func (s *source) fromStore(ctx context.Context, id string) (*ports.OAuthApp, error) {
	if s.store == nil {
		return nil, nil
	}
	app, err := s.store.GetOAuthAppByIdentifier(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return appToPort(app), nil
}

// fromDevelop mirrors the provider's old Develop path: a missing client is
// not an error, a transport failure is.
func (s *source) fromDevelop(ctx context.Context, id, slug string) (*ports.OAuthApp, error) {
	if s.develop == nil {
		if s.log != nil {
			s.log.Warn("OAuth client lookup skipped: Develop gRPC client is not configured ([services] develop)", "id", id, "slug", slug)
		}
		return nil, nil
	}
	request := &gen.DyGetCustomAppRequest{}
	if id != "" {
		request.Query = &gen.DyGetCustomAppRequest_Id{Id: id}
	} else {
		request.Query = &gen.DyGetCustomAppRequest_Slug{Slug: slug}
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	response, err := s.develop.GetCustomApp(ctx, request)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, nil
		}
		return nil, err
	}
	if response == nil || response.GetApp() == nil {
		return nil, nil
	}
	return integrations.OAuthAppFromProto(response.GetApp()), nil
}

// ── Port adapter ───────────────────────────────────────────────────────────

type adapter struct {
	src *source
}

func (a *adapter) Available() bool { return a != nil && a.src != nil }

func (a *adapter) Get(ctx context.Context, id string) (*ports.OAuthApp, error) {
	if a == nil || a.src == nil {
		return nil, ports.ErrUnavailable
	}
	return a.src.Get(ctx, id)
}

func (a *adapter) GetBySlug(ctx context.Context, slug string) (*ports.OAuthApp, error) {
	if a == nil || a.src == nil {
		return nil, ports.ErrUnavailable
	}
	return a.src.GetBySlug(ctx, slug)
}

func (a *adapter) CheckSecret(ctx context.Context, appID, secret string) (bool, error) {
	if a == nil || a.src == nil {
		return false, ports.ErrUnavailable
	}
	return a.src.CheckSecret(ctx, appID, secret)
}

// appToPort maps a stored app onto the port shape. ClientSecret stays empty:
// a secret is verified through CheckSecret, never shipped with the client.
func appToPort(app *model.OAuthApp) *ports.OAuthApp {
	out := &ports.OAuthApp{
		ID:                     app.Id,
		Slug:                   app.Slug,
		Name:                   app.Name,
		Status:                 app.Status,
		RedirectURIs:           app.OauthConfig.RedirectUris,
		PostLogoutRedirectURIs: app.OauthConfig.PostLogoutRedirectUris,
		Scopes:                 app.OauthConfig.AllowedScopes,
		RequirePkce:            app.OauthConfig.RequirePkce,
		AllowOfflineAccess:     app.OauthConfig.AllowOfflineAccess,
		IsPublicClient:         app.OauthConfig.IsPublicClient,
		// The consent screen renders these; clientFromPort already carries
		// them into the provider's cached client.
		Picture:    app.Picture,
		Background: app.Background,
	}
	if app.Links.HomePage != "" {
		value := app.Links.HomePage
		out.HomeUri = &value
	}
	if app.Links.PrivacyPolicy != "" {
		value := app.Links.PrivacyPolicy
		out.PolicyUri = &value
	}
	if app.Links.TermsOfService != "" {
		value := app.Links.TermsOfService
		out.TermsOfServiceUri = &value
	}
	return out
}

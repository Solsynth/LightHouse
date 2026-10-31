// Package presenceprovidersmod wires Passport's Steam and Last.fm presence
// scanners into Stargate as a feature module: the admin scan routes belonging
// to AccountAdminController and the three PresenceUpdateJob stages. It owns no
// tables; sessions are written through the presences module's activity store.
package presenceprovidersmod

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
	"src.solsynth.dev/sosys/stargate/core"
	"src.solsynth.dev/sosys/stargate/httpserver/presenceprovidersctl"
	"src.solsynth.dev/sosys/stargate/ports"
	"src.solsynth.dev/sosys/stargate/presenceproviders"
)

// Factory returns the compiled module descriptor.
func Factory() module.Factory[core.Services] {
	return module.Factory[core.Services]{
		ID:             presenceproviders.ModuleID,
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Requires:       []string{"app.solian.id.accounts", "app.solian.id.presences"},
		Capabilities: []string{
			"accounts.presences.steam",
			"accounts.presences.lastfm",
			"presences.scan",
		},
		Ports: []module.PortRequirement{
			{ID: ports.PortFiles, Optional: true},
			{ID: ports.PortPresence, Optional: true},
		},
		New: func() module.Instance[core.Services] { return &instance{} },
	}
}

type instance struct {
	service *presenceproviders.Service
}

func (i *instance) Configure(ctx context.Context, host *module.Host[core.Services]) error {
	if host == nil || host.Core == nil || host.Core.DB == nil || host.Core.Store == nil {
		return errors.New("presenceproviders: missing host core database")
	}
	if host.Core.Perm == nil {
		return errors.New("presenceproviders: missing permission service")
	}
	log := coreLog(host)
	set := ports.FromRegistry(host.Ports)

	service := presenceproviders.New(presenceproviders.Options{
		Store:        host.Core.Store,
		DB:           host.Core.DB,
		Presence:     host.Core.PresenceResolver,
		PresencePort: set.Presence,
		Broadcast:    host.Core.Broadcast,
		Log:          log,
		Settings:     resolveSettings(host),
	})

	presenceprovidersctl.Register(host.API, presenceprovidersctl.Deps{
		Service: service,
		Perm:    host.Core.Perm,
		Log:     log,
	})
	service.StartJobs(ctx)
	i.service = service
	return nil
}

func (i *instance) Stop(context.Context) error {
	if i.service != nil {
		i.service.Stop()
	}
	return nil
}

func coreLog(host *module.Host[core.Services]) *slog.Logger {
	if host != nil && host.Core != nil && host.Core.Log != nil {
		return host.Core.Log
	}
	return slog.Default()
}

// resolveSettings reads the module's settings subtree with the documented
// primary keys:
//
//	[modules.settings."app.solian.id.presenceproviders"]
//	steamApiKey = "..."
//	lastFmApiKey = "..."
//	lastFmApiSecret = "..."
//
// The oidc* names are accepted as aliases, and an unset value falls back to
// the Oidc TOML section (Oidc:Steam:ApiKey, Oidc:LastFm:ApiKey/ApiSecret) so a
// deployment that already configured the login providers needs no duplication.
func resolveSettings(host *module.Host[core.Services]) presenceproviders.Settings {
	raw := host.ModuleSettings(presenceproviders.ModuleID)
	get := func(primary, alias string) string {
		for _, key := range []string{primary, alias} {
			if value, ok := raw[key].(string); ok && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
		return ""
	}
	settings := presenceproviders.Settings{
		SteamAPIKey:     get("steamApiKey", "oidcSteamApiKey"),
		LastFMAPIKey:    get("lastFmApiKey", "oidcLastFmApiKey"),
		LastFMAPISecret: get("lastFmApiSecret", "oidcLastFmApiSecret"),
	}
	if cfg := host.Core.Config; cfg != nil {
		if settings.SteamAPIKey == "" {
			settings.SteamAPIKey = strings.TrimSpace(cfg.Oidc.Steam.APIKey)
		}
		if settings.LastFMAPIKey == "" {
			settings.LastFMAPIKey = strings.TrimSpace(cfg.Oidc.LastFm.ApiKey)
		}
		if settings.LastFMAPISecret == "" {
			settings.LastFMAPISecret = strings.TrimSpace(cfg.Oidc.LastFm.ApiSecret)
		}
	}
	return settings
}

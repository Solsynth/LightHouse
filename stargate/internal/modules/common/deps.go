// Package common builds controller dependency bundles from the shared core
// services. Keeping the raw struct literals here avoids copy/paste drift as
// modules are added or removed.
package common

import (
	"context"

	"src.solsynth.dev/sosys/stargate/internal/core"
	"src.solsynth.dev/sosys/stargate/internal/grpcserver"
	"src.solsynth.dev/sosys/stargate/internal/httpserver/adminctl"
	"src.solsynth.dev/sosys/stargate/internal/httpserver/authctl"
	"src.solsynth.dev/sosys/stargate/internal/httpserver/e2eectl"
	"src.solsynth.dev/sosys/stargate/internal/httpserver/oauthappctl"
	"src.solsynth.dev/sosys/stargate/internal/httpserver/oidcctl"
	"src.solsynth.dev/sosys/stargate/internal/httpserver/profilectl"
	"src.solsynth.dev/sosys/stargate/internal/httpserver/securityctl"
	"src.solsynth.dev/sosys/stargate/internal/httpserver/socialctl"
	"src.solsynth.dev/sosys/stargate/internal/httpserver/spellctl"
	"src.solsynth.dev/sosys/stargate/internal/ports"
)

func GRPC(c *core.Services) grpcserver.Deps {
	return grpcserver.Deps{
		Store: c.Store, Redis: c.Redis, Auth: c.Auth, Token: c.Token, JWT: c.JWT,
		Perm: c.Perm, Logs: c.Logs, Cfg: c.Config, Log: c.Log, Presence: c.Presence,
		SingleUser: c.SingleUser,
	}
}

func Auth(c *core.Services, p *ports.Set) authctl.Deps {
	return authctl.Deps{
		Store: c.Store, Redis: c.Redis, Cfg: c.Config, Token: c.Token, Auth: c.Auth,
		Geo: c.Geo, Clients: c.Clients, Events: c.NATS, Spells: c.Spells, Log: c.Log,
		SingleUser: c.SingleUser, Ports: p,
	}
}

func Security(c *core.Services, p *ports.Set) securityctl.Deps {
	return securityctl.Deps{
		Store: c.Store, Redis: c.Redis, Cfg: c.Config, Auth: c.Auth, Token: c.Token,
		Perm: c.Perm, Logs: c.Logs, Spells: c.Spells,
		Presence: c.Presence, Ports: p, Log: c.Log,
	}
}

func Profile(c *core.Services, p *ports.Set) profilectl.Deps {
	return profilectl.Deps{
		Store: c.Store, Redis: c.Redis, Cfg: c.Config, Perm: c.Perm,
		Logs: c.Logs, Clients: c.Clients, Ports: p, Log: c.Log,
	}
}

func Social(c *core.Services) socialctl.Deps {
	return socialctl.Deps{
		Store: c.Store, Redis: c.Redis, Cfg: c.Config, Auth: c.Auth,
		Logs: c.Logs, Log: c.Log, Spells: c.Spells,
	}
}

func OIDC(c *core.Services, p *ports.Set) oidcctl.Deps {
	return oidcctl.Deps{
		Store: c.Store, Redis: c.Redis, Cfg: c.Config, JWT: c.JWT, Token: c.Token,
		Auth: c.Auth, Clients: p.OAuthClients, Log: c.Log,
	}
}

// OAuthApp builds the OAuth app controller dependencies.
func OAuthApp(c *core.Services) oauthappctl.Deps {
	deps := oauthappctl.Deps{Store: c.Store, Perm: c.Perm, Log: c.Log}
	if c.Redis != nil && c.Redis.Cache != nil {
		cache := c.Redis.Cache
		deps.Invalidate = func(ctx context.Context, appID string, slugs ...string) {
			if appID != "" {
				_ = cache.Remove(ctx, oauthappctl.CacheKeyPrefixClientID+appID)
			}
			for _, slug := range slugs {
				if slug != "" {
					_ = cache.Remove(ctx, oauthappctl.CacheKeyPrefixClientSlug+slug)
				}
			}
		}
	}
	return deps
}

func Admin(c *core.Services) adminctl.Deps {
	return adminctl.Deps{
		Store: c.Store, Redis: c.Redis, Cfg: c.Config, Perm: c.Perm, Logs: c.Logs,
		Clients: c.Clients, Log: c.Log, Spells: c.Spells,
	}
}

func E2EE(c *core.Services) e2eectl.Deps {
	return e2eectl.Deps{
		Store: c.Store, Events: c.NATS, Clients: c.Clients, Log: c.Log,
	}
}

func Spell(c *core.Services) spellctl.Deps {
	return spellctl.Deps{Store: c.Store, Spell: c.Spells, Log: c.Log}
}

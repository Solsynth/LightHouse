// Package common builds controller dependency bundles from the shared core
// services. Keeping the raw struct literals here avoids copy/paste drift as
// modules are added or removed.
package common

import (
	"src.solsynth.dev/sosys/stargate/core"
	"src.solsynth.dev/sosys/stargate/grpcserver"
	"src.solsynth.dev/sosys/stargate/httpserver/adminctl"
	"src.solsynth.dev/sosys/stargate/httpserver/authctl"
	"src.solsynth.dev/sosys/stargate/httpserver/e2eectl"
	"src.solsynth.dev/sosys/stargate/httpserver/oidcctl"
	"src.solsynth.dev/sosys/stargate/httpserver/profilectl"
	"src.solsynth.dev/sosys/stargate/httpserver/securityctl"
	"src.solsynth.dev/sosys/stargate/httpserver/socialctl"
	"src.solsynth.dev/sosys/stargate/httpserver/spellctl"
	"src.solsynth.dev/sosys/stargate/httpserver/statusctl"
	"src.solsynth.dev/sosys/stargate/ports"
)

func GRPC(c *core.Services) grpcserver.Deps {
	return grpcserver.Deps{
		Store: c.Store, Redis: c.Redis, Auth: c.Auth, Token: c.Token, JWT: c.JWT,
		Perm: c.Perm, Logs: c.Logs, Cfg: c.Config, Log: c.Log, Presence: c.Presence,
		SingleUser: c.SingleUser,
		// The relationships module owns the six DyProfileService social-graph
		// RPCs but does not register the service itself (profiles does), so the
		// module's read service has to travel through every GRPC deps bundle.
		// Dropping it leaves the field nil and every relationship RPC answers
		// Unimplemented even though the module is enabled.
		Relationships: c.Relationships,
		// The two DyProfileService status RPCs belong to the presences module,
		// which does not register the service (profiles does): the resolver has
		// to travel through every GRPC bundle for the same reason.
		PresenceResolver: c.PresenceResolver,
	}
}

// Status builds the account-status/presence-activity controller dependencies.
// The Drive file port resolves status icons and backgrounds; the broadcaster
// publishes the post-write presence notifications.
func Status(c *core.Services, p *ports.Set) statusctl.Deps {
	return statusctl.Deps{
		Store: c.Store, Accounts: c.Store, Perm: c.Perm,
		Presence: c.PresenceResolver, Files: p.Files,
		Clients: c.Clients, Broadcast: c.Broadcast, Log: c.Log,
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
	return spellctl.Deps{Store: c.Store, Spell: c.Spells, Logs: c.Logs, Log: c.Log}
}

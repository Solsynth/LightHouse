// Package ticketsmod wires the ported Passport ticket domain into Stargate as
// a feature module. The domain lives in internal/tickets; this package owns
// the factory, schema registration, HTTP mounting and lifecycle.
package ticketsmod

import (
	"context"
	"errors"
	"log/slog"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
	"src.solsynth.dev/sosys/stargate/core"
	"src.solsynth.dev/sosys/stargate/httpserver/ticketsctl"
	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/ports"
	"src.solsynth.dev/sosys/stargate/store"
	"src.solsynth.dev/sosys/stargate/tickets"
)

// Factory returns the compiled module descriptor.
func Factory() module.Factory[core.Services] {
	return module.Factory[core.Services]{
		ID:             tickets.ModuleID,
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Requires:       []string{"app.solian.id.accounts"},
		Capabilities:   []string{"tickets", "tickets.messages", "tickets.oncall"},
		Ports: []module.PortRequirement{
			{ID: ports.PortFiles, Optional: true},
			{ID: ports.PortPush, Optional: true},
			{ID: ports.PortEmail, Optional: true},
		},
		Models: tickets.Models(),
		SQL:    schemaHooks(),
		New:    func() module.Instance[core.Services] { return &instance{} },
	}
}

func schemaHooks() []module.SchemaHook {
	hooks := tickets.Hooks()
	out := make([]module.SchemaHook, 0, len(hooks))
	for _, hook := range hooks {
		out = append(out, module.SchemaHook(hook))
	}
	return out
}

type instance struct{}

func (i *instance) Configure(_ context.Context, host *module.Host[core.Services]) error {
	if host == nil || host.Core == nil || host.Core.DB == nil {
		return errors.New("tickets: missing host core database")
	}
	log := coreLog(host)
	ticketStore := tickets.NewStore(host.Core.DB)
	set := ports.FromRegistry(host.Ports)
	service := tickets.NewService(ticketStore, host.Core.Store, fileResolver(set), log)

	siteURL := ""
	if host.Core.Config != nil {
		siteURL = host.Core.Config.SiteUrl
	}

	ticketsctl.Register(host.API, ticketsctl.Deps{
		Service:    service,
		Store:      ticketStore,
		Perm:       host.Core.Perm,
		Contacts:   host.Core.Store,
		Superusers: superusersLoader(host.Core.DB, host.Core.Store),
		Push:       pushPort(set),
		Email:      emailPort(set),
		SiteURL:    siteURL,
		Log:        log,
	})
	return nil
}

func (i *instance) Stop(context.Context) error { return nil }

func coreLog(host *module.Host[core.Services]) *slog.Logger {
	if host != nil && host.Core != nil && host.Core.Log != nil {
		return host.Core.Log
	}
	return slog.Default()
}

func fileResolver(set *ports.Set) tickets.FileResolver {
	if set == nil || set.Files == nil || !set.Files.Available() {
		return nil
	}
	return set.Files
}

func pushPort(set *ports.Set) ports.Push {
	if set == nil || set.Push == nil || !set.Push.Available() {
		return nil
	}
	return set.Push
}

func emailPort(set *ports.Set) ports.Email {
	if set == nil || set.Email == nil || !set.Email.Available() {
		return nil
	}
	return set.Email
}

// superusersLoader is the notification fallback when the on-call roster is
// empty (AccountService.GetAllSuperusersAsync).
func superusersLoader(db *gorm.DB, accounts *store.Store) func(context.Context) ([]model.Account, error) {
	return func(ctx context.Context) ([]model.Account, error) {
		if db == nil || accounts == nil {
			return []model.Account{}, nil
		}
		var ids []uuid.UUID
		if err := db.WithContext(ctx).Table("accounts").
			Where("is_superuser = ? AND deleted_at IS NULL", true).
			Pluck("id", &ids).Error; err != nil {
			return nil, err
		}
		return accounts.GetAccountsByIDs(ctx, ids)
	}
}

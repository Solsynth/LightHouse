// Package accounteventsmod wires the ported Passport account-events domain
// into Stargate as a feature module: the four account-event tables, the
// check-in/calendar/notable-day HTTP surfaces and the CN notable-day seed.
package accounteventsmod

import (
	"context"
	"errors"
	"log/slog"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/stargate/accountevents"
	"src.solsynth.dev/sosys/stargate/accountrecords"
	"src.solsynth.dev/sosys/stargate/httpserver/accounteventsctl"
	"src.solsynth.dev/sosys/stargate/module"
	"src.solsynth.dev/sosys/stargate/ports"
)

// ModuleID is the reverse-DNS module identifier.
const ModuleID = "app.solian.id.accountevents"

// Factory returns the compiled module descriptor.
//
// The reward collaborators are optional ports: without a wallet or perks
// provider a check-in still records its row, it just cannot pay out. Account
// XP is written through the sibling accountrecords store, which the module
// requires so its route and table ownership stay stable.
func Factory() module.Factory {
	return module.Factory{
		ID:             ModuleID,
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Requires: []string{
			"app.solian.id.accounts",
			"app.solian.id.accountrecords",
			"app.solian.id.presences",
		},
		Capabilities: []string{
			"accounts.check-in",
			"accounts.calendar",
			"accounts.calendar.events",
			"accounts.calendar.subscriptions",
			"accounts.calendar.notable-days",
			"accounts.timeline",
			"accounts.fortune",
		},
		Ports: []module.PortRequirement{
			{ID: ports.PortPerks, Optional: true},
			{ID: ports.PortPayments, Optional: true},
			{ID: ports.PortFiles, Optional: true},
		},
		Models: accountevents.Models(),
		SQL:    schemaHooks(),
		New:    func() module.Instance { return &instance{} },
	}
}

func schemaHooks() []module.SchemaHook {
	hooks := accountevents.Hooks()
	out := make([]module.SchemaHook, 0, len(hooks))
	for _, hook := range hooks {
		out = append(out, module.SchemaHook(hook))
	}
	return out
}

type instance struct{}

func (i *instance) Configure(ctx context.Context, host *module.Host) error {
	if host == nil || host.Core == nil || host.Core.DB == nil {
		return errors.New("accountevents: missing host core database")
	}
	log := coreLog(host)
	store := accountevents.NewStore(host.Core.DB)
	notable := accountevents.NewNotableService(store)
	set := ports.FromRegistry(host.Ports)

	var captcha accountevents.CaptchaValidator
	if host.Core.Auth != nil {
		captcha = host.Core.Auth
	}
	service := accountevents.NewService(accountevents.Deps{
		Store:    store,
		Accounts: host.Core.Store,
		Rewards:  experienceAdapter{store: accountrecords.NewStore(host.Core.DB)},
		Perks:    set.Perks,
		Creditor: creditorPort(set.Payments),
		Files:    set.Files,
		Captcha:  captcha,
		Notable:  notable,
		Log:      log,
	})

	seed := accountevents.NewSeedService(store, log)
	if err := seed.EnsureSeeded(ctx); err != nil {
		return err
	}

	deps := accounteventsctl.Deps{
		Service:  service,
		Notable:  notable,
		Store:    store,
		Accounts: host.Core.Store,
		Perm:     host.Core.Perm,
		Log:      log,
	}
	accounteventsctl.Register(host.API, deps)
	accounteventsctl.RegisterPublic(host.API, deps)
	return nil
}

func (i *instance) Stop(context.Context) error { return nil }

func coreLog(host *module.Host) *slog.Logger {
	if host != nil && host.Core != nil && host.Core.Log != nil {
		return host.Core.Log
	}
	return slog.Default()
}

// creditorPort narrows the payments port to its credit half, mirroring the
// progression module's runtime type assertion.
func creditorPort(payments ports.Payments) ports.Creditor {
	if payments == nil {
		return nil
	}
	creditor, ok := payments.(ports.Creditor)
	if !ok {
		return nil
	}
	return creditor
}

// experienceAdapter grants check-in XP through the accountrecords store,
// which owns the experience table and the profile experience patch.
type experienceAdapter struct {
	store *accountrecords.Store
}

func (a experienceAdapter) AddExperience(ctx context.Context, accountID uuid.UUID, reasonType, reason string, delta int64, multiplier float64) error {
	if a.store == nil {
		return nil
	}
	_, err := a.store.AddExperienceWithMultiplier(ctx, accountID, reasonType, reason, delta, multiplier)
	return err
}

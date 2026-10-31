// Package purchasesmod wires the ported Passport affiliation-spell and
// name-change-card domains into Stargate as a feature module. The domain lives
// in internal/stargate/purchases; this package owns the factory, schema
// registration, HTTP surface and lifecycle.
package purchasesmod

import (
	"context"
	"errors"
	"log/slog"

	"src.solsynth.dev/sosys/stargate/httpserver/purchasectl"
	"src.solsynth.dev/sosys/stargate/module"
	"src.solsynth.dev/sosys/stargate/ports"
	"src.solsynth.dev/sosys/stargate/purchases"
)

// Factory returns the compiled module descriptor.
//
// Payments and order events are optional: a deployment without the wallet
// module still lists and manages spells, but purchases answer 503 and order
// fulfillment must be driven through purchases.Service.HandleOrderPaid.
func Factory() module.Factory {
	return module.Factory{
		ID:             purchases.ModuleID,
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Requires:       []string{"app.solian.id.accounts"},
		Capabilities: []string{
			"affiliations", "affiliations.results", "accounts.name-change-card",
		},
		Ports: []module.PortRequirement{
			{ID: ports.PortPayments, Optional: true},
			{ID: ports.PortOrderEvents, Optional: true},
		},
		Models: purchases.Models(),
		SQL:    schemaHooks(),
		New:    func() module.Instance { return &instance{} },
	}
}

// schemaHooks adapts the domain's raw-SQL hooks to the module hook type.
func schemaHooks() []module.SchemaHook {
	hooks := purchases.Hooks()
	out := make([]module.SchemaHook, 0, len(hooks))
	for _, hook := range hooks {
		out = append(out, module.SchemaHook(hook))
	}
	return out
}

type instance struct{}

func (i *instance) Configure(ctx context.Context, host *module.Host) error {
	if host == nil || host.Core == nil || host.Core.DB == nil {
		return errors.New("purchases: missing host core database")
	}
	log := coreLog(host)
	set := ports.FromRegistry(host.Ports)

	var payments ports.Payments
	if set.Payments != nil && set.Payments.Available() {
		payments = set.Payments
	}
	var accounts purchases.AccountStore
	if host.Core.Store != nil {
		accounts = host.Core.Store
	}
	// grpcclient.Clients has no Sphere publisher client yet, so the publisher
	// name-change target reports a clear failure instead of pretending to have
	// renamed. Wiring a client later means passing a purchases.PublisherRenamer
	// here; the domain and wire contract already support it.
	service := purchases.NewService(purchases.NewStore(host.Core.DB),
		purchases.ParseSettings(host.ModuleSettings(purchases.ModuleID)),
		payments, accounts, nil, log)

	purchasectl.Register(host.API, purchasectl.Deps{
		Service: service,
		Perm:    host.Core.Perm,
		Log:     log,
	})

	if orderEvents := set.OrderEvents; orderEvents != nil && orderEvents.Available() {
		orderEvents.OnOrderPaid(service.HandleOrderPaid)
	} else {
		log.Info("purchases: order events are unavailable; paid orders need the wallet module to be fulfilled")
	}
	return nil
}

func (i *instance) Stop(context.Context) error { return nil }

func coreLog(host *module.Host) *slog.Logger {
	if host != nil && host.Core != nil && host.Core.Log != nil {
		return host.Core.Log
	}
	return slog.Default()
}

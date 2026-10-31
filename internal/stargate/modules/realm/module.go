// Package realmmod wires the ported Passport realm domain into Stargate as a
// feature module. The domain lives in internal/realm; this package owns the
// factory, the schema registration, the HTTP/gRPC surfaces and the lifecycle.
package realmmod

import (
	"context"
	"errors"
	"log/slog"

	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"
	"src.solsynth.dev/sosys/stargate/httpserver/realmctl"
	"src.solsynth.dev/sosys/stargate/httpserver/statusctl"
	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/module"
	"src.solsynth.dev/sosys/stargate/ports"
	"src.solsynth.dev/sosys/stargate/realm"
)

// Factory returns the compiled module descriptor.
//
// The realm surface depends on accounts/profiles for its member payloads and
// relationships for the invite-block check, but neither is required: a
// standalone deployment with the realm module alone still serves realms, it
// just has no members to authenticate against. relationships is listed because
// the invite route degrades without it.
func Factory() module.Factory {
	return module.Factory{
		ID:             realm.ModuleID,
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Requires:       []string{"app.solian.id.auth", "app.solian.id.accounts"},
		Capabilities: []string{
			"realms", "realms.members", "realms.labels", "realms.boosts",
			"realms.experience", "realms.permissions", "realms.moderation",
			"realms.public",
		},
		Ports: []module.PortRequirement{
			{ID: ports.PortFiles, Optional: true},
			{ID: ports.PortPush, Optional: true},
			{ID: ports.PortPayments, Optional: true},
		},
		Models:   realm.Models(),
		SQL:      schemaHooks(),
		Importer: func() module.Importer { return realm.NewImporter() },
		New:      func() module.Instance { return &instance{} },
	}
}

// schemaHooks adapts the domain's raw-SQL hooks to the module hook type.
func schemaHooks() []module.SchemaHook {
	hooks := realm.Hooks()
	out := make([]module.SchemaHook, 0, len(hooks))
	for _, hook := range hooks {
		out = append(out, module.SchemaHook(hook))
	}
	return out
}

type instance struct {
	stop func()
}

func (i *instance) Configure(ctx context.Context, host *module.Host) error {
	if host == nil || host.Core == nil || host.Core.DB == nil {
		return errors.New("realm: missing host core database")
	}
	log := coreLog(host)
	store := realm.NewStore(host.Core.DB)
	service := realm.NewService(store, perkProvider(host), log)

	set := ports.FromRegistry(host.Ports)
	deps := realmctl.Deps{
		Service:       service,
		Store:         store,
		Accounts:      host.Core.Store,
		Profiles:      host.Core.Store,
		Statuses:      statusLoader(host),
		Files:         fileResolver(set),
		Relationships: relationshipChecker(host),
		Payments:      paymentsPort(set),
		Perm:          host.Core.Perm,
		Logs:          host.Core.Logs,
		Log:           log,
	}
	realmctl.Register(host.API, deps)
	realmctl.RegisterAdmin(host.API, deps)

	if host.GRPC != nil {
		gen.RegisterDyRealmServiceServer(host.GRPC, realm.NewGRPCService(realm.GRPCDeps{
			Service: service, Store: store, Accounts: host.Core.Store,
			Push: pushPort(set),
		}))
	}

	// A local wallet applies its orders in-process (no event bus needed); the
	// NATS consumers stay subscribed for deployments where the wallet lives
	// elsewhere. Both paths deduplicate by order id.
	if orderEvents := set.OrderEvents; orderEvents != nil && orderEvents.Available() {
		orderEvents.OnOrderPaid(func(ctx context.Context, event ports.OrderEvent) error {
			return service.ApplyPaidOrder(ctx, realm.PaymentOrder{
				OrderID: event.OrderID, ProductIdentifier: event.ProductIdentifier, Meta: event.Meta,
			})
		})
	}

	var events realm.Events
	if host.Core.NATS != nil {
		events = host.Core.NATS
	}
	i.stop = realm.StartJobs(ctx, service, events, log)
	return nil
}

func (i *instance) Stop(context.Context) error {
	if i.stop != nil {
		i.stop()
		i.stop = nil
	}
	return nil
}

func coreLog(host *module.Host) *slog.Logger {
	if host != nil && host.Core != nil && host.Core.Log != nil {
		return host.Core.Log
	}
	return slog.Default()
}

// perkProvider adapts the perks port to the realm XP multiplier.
func perkProvider(host *module.Host) realm.PerkProvider {
	if host == nil || host.Ports == nil {
		return nil
	}
	perks := ports.FromRegistry(host.Ports).Perks
	if perks == nil || !perks.Available() {
		return nil
	}
	return perksLookup{perks: perks}
}

type perksLookup struct {
	perks ports.Perks
}

func (p perksLookup) GetPerkSubscription(ctx context.Context, accountID string) (*model.SnSubscriptionReferenceObject, error) {
	return p.perks.Subscription(ctx, accountID)
}

func fileResolver(set *ports.Set) realm.FileResolver {
	if set == nil || set.Files == nil || !set.Files.Available() {
		return nil
	}
	return set.Files
}

func paymentsPort(set *ports.Set) realm.Payments {
	if set == nil || set.Payments == nil || !set.Payments.Available() {
		return nil
	}
	return paymentsBridge{payments: set.Payments}
}

func pushPort(set *ports.Set) realm.Push {
	if set == nil || set.Push == nil || !set.Push.Available() {
		return nil
	}
	return set.Push
}

func relationshipChecker(host *module.Host) realmctl.BlockChecker {
	if host == nil || host.Core == nil || host.Core.Relationships == nil {
		return nil
	}
	return host.Core.Relationships
}

func statusLoader(host *module.Host) realmctl.StatusLoader {
	if host == nil || host.Core == nil || host.Core.PresenceResolver == nil {
		return nil
	}
	return statusctl.Loader{Store: host.Core.Store, Presence: host.Core.PresenceResolver}
}

// paymentsBridge adapts ports.Payments to the realm domain's narrower
// interface, so the domain never imports the port package.
type paymentsBridge struct {
	payments ports.Payments
}

func (p paymentsBridge) CreateOrder(ctx context.Context, req realm.OrderInput) (*realm.OrderResult, error) {
	product := req.ProductIdentifier
	app := req.AppIdentifier
	remarks := req.Remarks
	created, err := p.payments.CreateOrder(ctx, ports.OrderRequest{
		AccountID: req.AccountID, Currency: req.Currency, Amount: req.Amount.Decimal,
		ProductIdentifier: &product, AppIdentifier: &app, Remarks: &remarks,
		Meta: req.Meta,
	})
	if err != nil {
		if errors.Is(err, ports.ErrUnavailable) {
			return nil, realm.ErrUnavailable
		}
		return nil, err
	}
	return &realm.OrderResult{ID: created.ID}, nil
}

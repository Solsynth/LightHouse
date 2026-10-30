// Package walletmod wires the ported DysonNetwork.Wallet service into Stargate
// as a feature module. The domain lives in internal/wallet; this package owns
// the factory, the schema registration, the perks port and the lifecycle.
package walletmod

import (
	"context"
	"errors"
	"log/slog"

	"github.com/google/uuid"

	gen "src.solsynth.dev/sosys/go/proto"
	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/module"
	"src.solsynth.dev/sosys/stargate/internal/ports"
	"src.solsynth.dev/sosys/stargate/internal/wallet"
)

// moduleID is the feature module identifier. The remote adapter keeps
// app.solian.wallet.remote, so both can be compiled in one binary.
const moduleID = "app.solian.wallet"

// Factory returns the compiled module descriptor.
func Factory() module.Factory {
	return module.Factory{
		ID:             moduleID,
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Requires:       []string{"app.solian.id.auth"},
		Capabilities: []string{
			"wallets", "wallets.transactions", "wallets.transfers",
			"wallets.funds", "wallets.exchange", "wallets.orders",
			"wallets.subscriptions", "wallets.merchants",
		},
		Models:   wallet.SchemaModels(),
		SQL:      schemaHooks(),
		Provides: []module.PortID{ports.PortPerks},
		New:      func() module.Instance { return &instance{} },
	}
}

// schemaHooks adapts the domain's raw-SQL hooks to the module hook type.
func schemaHooks() []module.SchemaHook {
	hooks := wallet.SchemaHooks()
	out := make([]module.SchemaHook, 0, len(hooks))
	for _, hook := range hooks {
		out = append(out, module.SchemaHook(hook))
	}
	return out
}

type instance struct {
	stop func()
}

// ProvidePorts publishes the local perks provider, which the port registry
// prefers over the remote wallet adapter.
func (i *instance) ProvidePorts(_ context.Context, host *module.Host) error {
	if host == nil || host.Ports == nil || host.Core == nil || host.Core.DB == nil {
		return errors.New("wallet: missing host core database")
	}
	svc := wallet.NewService(host.Core.DB, coreLog(host), host.Core.NATS, portPush(host))
	subs := wallet.NewSubscriptions(svc)
	subs.Configure(host.ModuleSettings(moduleID), coreLog(host))
	module.Provide[ports.Perks](host.Ports, ports.PortPerks, moduleID, module.KindFeature,
		&perksProvider{subs: subs})
	return nil
}

func (i *instance) Configure(ctx context.Context, host *module.Host) error {
	if host == nil || host.Core == nil || host.Core.DB == nil {
		return errors.New("wallet: missing host core database")
	}
	log := coreLog(host)
	svc := wallet.NewService(host.Core.DB, log, host.Core.NATS, portPush(host))
	if host.Core.Clients != nil && host.Core.Clients.Develop != nil {
		svc.SetAppPublisherResolver(developResolver{client: host.Core.Clients.Develop})
	}
	deps := &wallet.Deps{
		Service:  svc,
		Perm:     host.Core.Perm,
		Log:      log,
		Ports:    ports.FromRegistry(host.Ports),
		Clients:  host.Core.Clients,
		Config:   host.Core.Config,
		Settings: host.ModuleSettings(moduleID),
	}

	subscriptions := wallet.NewSubscriptions(svc)
	subscriptions.Configure(deps.Settings, log)
	products := wallet.ProductsService(deps)
	// Subscription orders are applied first; the product hook ignores orders
	// it does not own.
	svc.OrderHooks = append(svc.OrderHooks, subscriptions.HandleOrderPaid, products.ApplyPaidOrder)

	wallet.RegisterHTTP(host.API, deps)
	if host.GRPC != nil {
		wallet.RegisterGRPC(host.GRPC, deps)
	}
	if host.Core.Token != nil {
		host.Core.Token.SetPerkProvider(&perksProvider{subs: subscriptions})
	}
	i.stop = wallet.StartJobs(ctx, deps)
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

func portPush(host *module.Host) ports.Push {
	if host == nil || host.Ports == nil {
		return nil
	}
	set := ports.FromRegistry(host.Ports)
	if set == nil {
		return nil
	}
	return set.Push
}

// perksProvider serves the perks port and the token authenticator from the
// local subscription table.
type perksProvider struct {
	subs *wallet.Subscriptions
}

func (p *perksProvider) Available() bool { return p != nil && p.subs != nil }

func (p *perksProvider) Subscription(ctx context.Context, accountID string) (*model.SnSubscriptionReferenceObject, error) {
	if !p.Available() {
		return nil, ports.ErrUnavailable
	}
	id, err := uuid.Parse(accountID)
	if err != nil {
		return nil, err
	}
	subscription, err := p.subs.GetPerkSubscription(ctx, id)
	if err != nil {
		return nil, err
	}
	return wallet.PerkReference(subscription), nil
}

// GetPerkSubscription satisfies auth.PerkProvider.
func (p *perksProvider) GetPerkSubscription(ctx context.Context, accountID string) (*model.SnSubscriptionReferenceObject, error) {
	return p.Subscription(ctx, accountID)
}

// developResolver maps a Develop app to its owning publisher through gRPC.
type developResolver struct {
	client gen.DyCustomAppServiceClient
}

func (r developResolver) PublisherIDForApp(ctx context.Context, appID uuid.UUID) (uuid.UUID, error) {
	if r.client == nil {
		return uuid.Nil, errors.New("develop client is not configured")
	}
	response, err := r.client.GetAppDeveloper(ctx, &gen.DyGetAppDeveloperRequest{AppId: appID.String()})
	if err != nil {
		return uuid.Nil, err
	}
	if response == nil || response.Developer == nil {
		return uuid.Nil, errors.New("app developer not found")
	}
	return uuid.Parse(response.Developer.PublisherId)
}

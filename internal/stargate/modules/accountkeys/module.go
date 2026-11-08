// Package accountkeysmod owns account SSH and PGP public keys: the two
// module-owned tables, the /api/keys management routes, the anonymous
// /api/accounts/{name}/keys listing and the validation surface other services
// use to resolve a key to its account.
package accountkeysmod

import (
	"context"
	"errors"
	"log/slog"

	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
	"src.solsynth.dev/sosys/stargate/accountkeys"
	"src.solsynth.dev/sosys/stargate/core"
	"src.solsynth.dev/sosys/stargate/grpcserver"
	"src.solsynth.dev/sosys/stargate/httpserver/keyctl"
)

// Factory returns the compiled module descriptor.
func Factory() module.Factory[core.Services] {
	return module.Factory[core.Services]{
		ID:             "app.solian.id.keys",
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Requires:       []string{"app.solian.id.accounts"},
		Capabilities:   []string{"accounts.ssh_keys", "accounts.pgp_keys"},
		Models:         accountkeys.Models(),
		SQL:            []module.SchemaHook{accountkeys.Indexes},
		New:            func() module.Instance[core.Services] { return &instance{} },
	}
}

type instance struct{}

func (i *instance) Configure(_ context.Context, host *module.Host[core.Services]) error {
	if host == nil || host.Core == nil || host.Core.DB == nil {
		return errors.New("accountkeys: missing host core database")
	}
	log := slog.Default()
	if host.Core.Log != nil {
		log = host.Core.Log
	}
	keys := accountkeys.NewStore(host.Core.DB)
	keyctl.Register(host.API, keyctl.Deps{
		Keys:     keys,
		Accounts: host.Core.Store,
		Logs:     host.Core.Logs,
		Log:      log,
	})
	grpcserver.RegisterAccountKeys(host.GRPC, grpcserver.AccountKeyDeps{
		Keys:  keys,
		Auth:  host.Core.Auth,
		Store: host.Core.Store,
		Cfg:   host.Core.Config,
		Log:   log,
	})
	return nil
}

func (i *instance) Stop(context.Context) error { return nil }

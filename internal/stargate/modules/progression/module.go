// Package progressionmod wires the ported Passport progression domain into
// Stargate as a feature module: the six progression tables, the account and
// admin HTTP surfaces, the badge discovery manifest, and the action-log
// consumer that advances achievements and quests.
package progressionmod

import (
	"context"
	"errors"
	"log/slog"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/stargate/httpserver/progressionctl"
	"src.solsynth.dev/sosys/stargate/module"
	"src.solsynth.dev/sosys/stargate/ports"
	"src.solsynth.dev/sosys/stargate/progression"
	"src.solsynth.dev/sosys/stargate/store"
)

// Factory returns the compiled module descriptor.
//
// Progression consumes action logs, which the accounts module emits, so it
// requires it. The reward collaborators (profile XP, badges, source points)
// are optional ports: a deployment without them still tracks progress, it
// just cannot pay out. Completion packets go straight to the websocket bus.
func Factory() module.Factory {
	return module.Factory{
		ID:             progression.ModuleID,
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Requires:       []string{"app.solian.id.auth", "app.solian.id.accounts"},
		Capabilities: []string{
			"progression.achievements", "progression.quests", "progression.badges",
		},
		Ports: []module.PortRequirement{
			{ID: ports.PortBadges, Optional: true},
			{ID: ports.PortPayments, Optional: true},
		},
		Models: progression.Models(),
		SQL:    schemaHooks(),
		New:    func() module.Instance { return &instance{} },
	}
}

func schemaHooks() []module.SchemaHook {
	hooks := progression.Hooks()
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
		return errors.New("progression: missing host core database")
	}
	log := coreLog(host)
	store := progression.NewStore(host.Core.DB)
	set := ports.FromRegistry(host.Ports)

	service := progression.NewService(progression.Deps{
		Store:      store,
		Experience: experienceAdapter{store: host.Core.Store},
		Badges:     badgeAdapter{port: set.Badges},
		Wallet:     walletAdapter{payments: set.Payments},
		Packets:    packetAdapter{bus: host.Core.NATS},
		Log:        log,
	})
	seed := progression.NewSeedService(store, log)
	if err := seed.EnsureSeeded(ctx); err != nil {
		return err
	}

	deps := progressionctl.Deps{
		Service:        service,
		Seed:           seed,
		Store:          store,
		BadgeIconsPath: badgeIconsPath(host),
		Perm:           host.Core.Perm,
		Log:            log,
	}
	progressionctl.Register(host.API, deps)
	progressionctl.RegisterAdmin(host.API, deps)
	if host.Engine != nil {
		progressionctl.RegisterWellKnown(host.Engine, deps)
	}

	if host.Core.NATS != nil {
		i.stop = progression.StartConsumer(ctx, service, host.Core.NATS, log)
	}
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

// badgeIconsPath reads [modules.settings."app.solian.id.progression"] iconsPath,
// the option the C# [Badges] section carried.
func badgeIconsPath(host *module.Host) string {
	settings := host.ModuleSettings(progression.ModuleID)
	if settings == nil {
		return ""
	}
	if value, ok := settings["iconsPath"].(string); ok {
		return value
	}
	if value, ok := settings["badges"].(map[string]any); ok {
		if path, ok := value["iconsPath"].(string); ok {
			return path
		}
	}
	return ""
}

// ─────────────────────────── reward collaborators ───────────────────────────

// experienceAdapter grants account XP by patching the profile, mirroring
// Passport's ExperienceService + ProfileFieldUpdatedEvent round trip. The
// account curve, level and progress are recomputed on every profile read.
type experienceAdapter struct {
	store *store.Store
}

func (a experienceAdapter) AddRecord(ctx context.Context, reasonType, reason string, delta int64, accountID uuid.UUID) error {
	if a.store == nil || delta == 0 {
		return nil
	}
	// The perk multiplier is applied by the caller that owns the subscription
	// port; progression XP is granted at face value like the C# listener did
	// before the leveling service applied its own bonus.
	amount := int(delta)
	return a.store.ApplyProfileFieldPatch(ctx, accountID, &store.ProfileFieldPatch{ExperienceDelta: &amount})
}

// badgeAdapter grants badges through the badges port.
type badgeAdapter struct {
	port ports.Badges
}

func (a badgeAdapter) Available() bool { return a.port != nil && a.port.Available() }

func (a badgeAdapter) Has(ctx context.Context, accountID, badgeType string) (bool, error) {
	badges, err := a.port.List(ctx, accountID)
	if err != nil {
		return false, err
	}
	for _, badge := range badges {
		if matchesBadgeType(badge, badgeType) {
			return true, nil
		}
	}
	return false, nil
}

func (a badgeAdapter) Grant(ctx context.Context, accountID, badgeType string, label, caption *string, meta map[string]any) error {
	if granter, ok := a.port.(ports.BadgeGranter); ok {
		return granter.Grant(ctx, accountID, badgeType, label, caption, meta)
	}
	// A read-only provider cannot grant; the reward is recorded as granted so
	// the grant does not retry forever.
	return nil
}

// matchesBadgeType compares the badge payload's type across the shapes the
// port can return (map or JSON-shaped struct).
func matchesBadgeType(badge any, badgeType string) bool {
	switch typed := badge.(type) {
	case map[string]any:
		if value, ok := typed["type"].(string); ok {
			return value == badgeType
		}
	case interface{ GetType() string }:
		return typed.GetType() == badgeType
	}
	return false
}

// walletAdapter credits source points through the payments port.
type walletAdapter struct {
	payments ports.Payments
}

func (a walletAdapter) Available() bool { return a.payments != nil && a.payments.Available() }

func (a walletAdapter) Credit(ctx context.Context, accountID, currency, remarks string, amount int64) error {
	creditor, ok := a.payments.(ports.Creditor)
	if !ok {
		return ports.ErrUnavailable
	}
	return creditor.Credit(ctx, accountID, currency, remarks, amount)
}

// packetAdapter publishes the completion packet on Blade's websocket push
// subject, mirroring RemoteRingService.SendWebSocketPacketToUser.
type packetAdapter struct {
	bus interface {
		PublishWS(ctx context.Context, target string, event string, payload any) error
	}
}

func (a packetAdapter) SendPacket(ctx context.Context, accountID, event string, payload any) error {
	if a.bus == nil {
		return ports.ErrUnavailable
	}
	return a.bus.PublishWS(ctx, accountID, event, payload)
}

// Package accountrecordsmod wires the ported Passport account records
// (experience, social credit, badges and the account board) into Stargate as
// a feature module. It owns the four legacy tables, the /api/accounts/me
// surfaces, the public account record routes, the admin routes, the
// DyExperienceService / DySocialCreditService gRPC receivers, and local
// providers for the badges and verification ports.
package accountrecordsmod

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/stargate/accountrecords"
	"src.solsynth.dev/sosys/stargate/httpserver/accountrecordsctl"
	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/module"
	"src.solsynth.dev/sosys/stargate/ports"
	"src.solsynth.dev/sosys/stargate/store"
)

const moduleID = "app.solian.id.accountrecords"

// Factory returns the compiled module descriptor.
func Factory() module.Factory {
	return module.Factory{
		ID:             moduleID,
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Requires:       []string{"app.solian.id.accounts"},
		Capabilities: []string{
			"accounts.records", "accounts.board", "accounts.badges",
			"accounts.credits", "accounts.leveling",
		},
		Ports: []module.PortRequirement{
			{ID: ports.PortPerks, Optional: true},
		},
		Provides: []module.PortID{ports.PortBadges, ports.PortVerification, ports.PortBoards},
		Models:   accountrecords.Models(),
		SQL:      schemaHooks(),
		New:      func() module.Instance { return &instance{} },
	}
}

func schemaHooks() []module.SchemaHook {
	hooks := accountrecords.Hooks()
	out := make([]module.SchemaHook, 0, len(hooks))
	for _, hook := range hooks {
		out = append(out, module.SchemaHook(hook))
	}
	return out
}

type instance struct {
	stopCreditJob func()
}

// ProvidePorts publishes the local badge/verification providers before any
// consumer configures. The port registry prefers a feature provider over the
// Passport integration automatically.
func (i *instance) ProvidePorts(_ context.Context, host *module.Host) error {
	if host == nil || host.Core == nil || host.Core.DB == nil || host.Ports == nil {
		return nil
	}
	records := accountrecords.NewStore(host.Core.DB)
	module.Provide[ports.Badges](host.Ports, ports.PortBadges, moduleID, module.KindFeature, &badgePort{store: records})
	module.Provide[ports.Verification](host.Ports, ports.PortVerification, moduleID, module.KindFeature, &verificationPort{profiles: host.Core.Store})
	module.Provide[ports.Boards](host.Ports, ports.PortBoards, moduleID, module.KindFeature, &boardPort{store: records})
	return nil
}

func (i *instance) Configure(ctx context.Context, host *module.Host) error {
	if host == nil || host.Core == nil || host.Core.DB == nil {
		return errors.New("accountrecords: missing host core database")
	}
	log := coreLog(host)
	records := accountrecords.NewStore(host.Core.DB)
	set := ports.FromRegistry(host.Ports)
	records.Perks = set.Perks
	records.SetWidgets(developValidator(host))

	deps := accountrecordsctl.Deps{
		Store:    records,
		Accounts: host.Core.Store,
		Perm:     host.Core.Perm,
		Log:      log,
	}
	accountrecordsctl.Register(host.API, deps)
	accountrecordsctl.RegisterAdmin(host.API, deps)

	if host.GRPC != nil {
		gen.RegisterDyExperienceServiceServer(host.GRPC, accountrecords.NewExperienceGRPC(records))
		gen.RegisterDySocialCreditServiceServer(host.GRPC, accountrecords.NewSocialCreditGRPC(records))
	}
	i.stopCreditJob = accountrecords.StartSocialCreditJob(ctx, records, accountrecords.DefaultSocialCreditInterval, log)
	return nil
}

func (i *instance) Stop(context.Context) error {
	if i.stopCreditJob != nil {
		i.stopCreditJob()
		i.stopCreditJob = nil
	}
	return nil
}

func coreLog(host *module.Host) *slog.Logger {
	if host != nil && host.Core != nil && host.Core.Log != nil {
		return host.Core.Log
	}
	return slog.Default()
}

// ───────────────────────────── port providers ─────────────────────────────

type badgePort struct {
	store *accountrecords.Store
}

func (p *badgePort) Available() bool { return p != nil && p.store != nil }

func (p *badgePort) List(ctx context.Context, accountID string) ([]any, error) {
	if !p.Available() {
		return nil, ports.ErrUnavailable
	}
	return p.store.BadgePorts(ctx, accountID)
}

// Grant implements ports.BadgeGranter, the write half of the badges port.
func (p *badgePort) Grant(ctx context.Context, accountID, badgeType string, label, caption *string, meta map[string]any) error {
	if !p.Available() {
		return ports.ErrUnavailable
	}
	id, err := store.ParseUUID(accountID)
	if err != nil {
		return err
	}
	_, err = p.store.GrantBadge(ctx, id, badgeType, label, caption, meta)
	return err
}

var _ ports.BadgeGranter = (*badgePort)(nil)

type verificationPort struct {
	profiles *store.Store
}

func (p *verificationPort) Available() bool { return p != nil && p.profiles != nil }

type boardPort struct {
	store *accountrecords.Store
}

func (p *boardPort) Available() bool { return p != nil && p.store != nil }

func (p *boardPort) List(ctx context.Context, accountID string) ([]any, error) {
	if !p.Available() {
		return nil, ports.ErrUnavailable
	}
	id, err := store.ParseUUID(accountID)
	if err != nil {
		return nil, err
	}
	items, err := p.store.ListBoard(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, len(items))
	for _, item := range items {
		out = append(out, item)
	}
	return out, nil
}

func (p *verificationPort) Get(ctx context.Context, accountID string) (*model.SnVerificationMark, error) {
	if !p.Available() {
		return nil, ports.ErrUnavailable
	}
	id, err := store.ParseUUID(accountID)
	if err != nil {
		return nil, err
	}
	profile, err := p.profiles.GetProfileByAccount(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return profile.Verification, nil
}

// ───────────────────────────── Develop validator ─────────────────────────────

// developValidator adapts the Develop custom-app client to the board widget
// validator. A missing client leaves the store on structural validation.
func developValidator(host *module.Host) accountrecords.BoardWidgetValidator {
	if host == nil || host.Core == nil || host.Core.Clients == nil || host.Core.Clients.Develop == nil {
		return nil
	}
	return &widgetValidator{client: host.Core.Clients.Develop}
}

type widgetValidator struct {
	client gen.DyCustomAppServiceClient
}

func (v *widgetValidator) ValidateWidget(ctx context.Context, appID, widgetKey string) error {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	response, err := v.client.GetBoardWidget(ctx, &gen.DyGetBoardWidgetRequest{AppId: appID, WidgetKey: widgetKey})
	if err != nil {
		if status.Code(err) == codes.NotFound || status.Code(err) == codes.FailedPrecondition {
			if detail := status.Convert(err).Message(); detail != "" {
				return errors.New(detail)
			}
		}
		return err
	}
	if response.GetWidget() == nil {
		return errors.New("Board widget is not configured for this app.")
	}
	if !response.GetWidget().GetIsEnabled() {
		return errors.New("Board widget is disabled for this app.")
	}
	return nil
}

func (v *widgetValidator) ValidatePayload(ctx context.Context, appID, widgetKey string, payload map[string]any) (map[string]any, error) {
	structPayload, err := structpb.NewStruct(payload)
	if err != nil {
		return nil, fmt.Errorf("invalid board payload: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	response, err := v.client.ValidateBoardWidgetPayload(ctx, &gen.DyValidateBoardWidgetPayloadRequest{
		AppId:     appID,
		WidgetKey: widgetKey,
		Payload:   structPayload,
	})
	if err != nil {
		return nil, err
	}
	if !response.GetValid() {
		if message := response.GetMessage(); message != "" {
			return nil, errors.New(message)
		}
		return nil, errors.New("Custom app board payload is invalid.")
	}
	if normalized := response.GetNormalizedPayload(); normalized != nil {
		return normalized.AsMap(), nil
	}
	return map[string]any{}, nil
}

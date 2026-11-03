// Package grpcclient holds the outbound gRPC clients Stargate uses to call
// sibling services (wallet for perks, develop for OIDC clients, drive for
// files, pass for profiles + NFC token validation, blade for websocket pushes,
// ring for notifications). Targets come from the [services] config section;
// an empty target leaves the corresponding provider nil unless Blade discovery
// is enabled, in which case the service name is resolved from the registry.
// A missing target degrades gracefully (nil/empty results), matching the C#
// services' behavior with a dependency down.
package grpcclient

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"
	"src.solsynth.dev/sosys/stargate/discovery"

	"src.solsynth.dev/sosys/stargate/config"
	"src.solsynth.dev/sosys/stargate/model"
)

// Dial creates a TLS gRPC connection with CA validation skipped. The fleet
// uses self-signed certs issued by the DysonNetwork CA; per the Golaunch
// README, CA validation is off.
func Dial(target string) (*grpc.ClientConn, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, nil
	}
	return grpc.NewClient(target,
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{InsecureSkipVerify: true})),
		grpc.WithDefaultCallOptions(grpc.WaitForReady(false)),
	)
}

// DialInsecure creates a plaintext gRPC connection. Blade's wsgateway serves
// plaintext h2c — the C# fleet (http://blade) and Stargate's own discovery
// registration dial it that way — so the Blade client must not use the
// TLS-always Dial above.
func DialInsecure(target string) (*grpc.ClientConn, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, nil
	}
	return grpc.NewClient(target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.WaitForReady(false)),
	)
}

// Clients holds the outbound clients.
type Clients struct {
	Wallet  gen.DySubscriptionServiceClient
	Payment gen.DyPaymentServiceClient
	Develop gen.DyCustomAppServiceClient
	Drive   gen.DyFileServiceClient
	Pass    gen.DyProfileServiceClient
	Nfc     gen.DyNfcServiceClient
	Blade   gen.WebSocketServiceClient
	Ring    gen.DyRingServiceClient
	conns   []*grpc.ClientConn
}

// Available reports whether a named optional outbound service was configured
// well enough for its client to be constructed. gRPC remains lazy: a client
// can still become unavailable after construction, in which case callers
// must handle the RPC error and keep the feature degraded.
func (c *Clients) Available(service string) bool {
	if c == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(service)) {
	case "wallet":
		return c.Wallet != nil
	case "payments":
		return c.Payment != nil
	case "develop":
		return c.Develop != nil
	case "drive":
		return c.Drive != nil
	case "pass":
		return c.Pass != nil
	case "nfc":
		return c.Nfc != nil
	case "blade":
		return c.Blade != nil
	case "ring":
		return c.Ring != nil
	default:
		return false
	}
}

// FilterServices clears clients whose integration module is disabled. Keys
// are the short service names ("drive", "wallet", ...). Connections already
// dialed are kept until Close; callers should treat nil as unavailable.
func (c *Clients) FilterServices(enabled map[string]bool) {
	if c == nil {
		return
	}
	if !enabled["wallet"] {
		c.Wallet = nil
		c.Payment = nil
	}
	if !enabled["develop"] {
		c.Develop = nil
	}
	if !enabled["drive"] {
		c.Drive = nil
	}
	if !enabled["pass"] {
		c.Pass = nil
		c.Nfc = nil
	}
	if !enabled["blade"] {
		c.Blade = nil
	}
	if !enabled["ring"] {
		c.Ring = nil
	}
}

// NewClients creates lazy clients only for configured targets. Empty targets
// leave the corresponding client nil, which disables that integration. Use
// NewClientsWithResolver when Blade discovery is enabled.
func NewClients(cfg *config.Config) (*Clients, error) {
	return NewClientsWithResolver(cfg, nil)
}

// NewClientsWithResolver resolves each fleet service through Blade while
// discovery is available and falls back to the configured [services] target
// when the service has no registration. With a resolver, an empty target still
// creates a client: the service is discovered at connect time.
func NewClientsWithResolver(cfg *config.Config, resolver *discovery.Resolver) (*Clients, error) {
	c := &Clients{}
	if cfg == nil {
		return c, nil
	}
	dial := func(service, target string, plaintext bool) *grpc.ClientConn {
		conn, err := dialService(service, target, plaintext, resolver)
		if err != nil || conn == nil {
			return nil
		}
		c.conns = append(c.conns, conn)
		return conn
	}
	if conn := dial("wallet", cfg.Services.Wallet.GRPC, false); conn != nil {
		c.Wallet = gen.NewDySubscriptionServiceClient(conn)
		// The remote wallet also hosts the payment surface the realm boost
		// flow opens its orders on.
		c.Payment = gen.NewDyPaymentServiceClient(conn)
	}
	if conn := dial("develop", cfg.Services.Develop.GRPC, false); conn != nil {
		c.Develop = gen.NewDyCustomAppServiceClient(conn)
	}
	if conn := dial("drive", cfg.Services.Drive.GRPC, false); conn != nil {
		c.Drive = gen.NewDyFileServiceClient(conn)
	}
	if conn := dial("pass", cfg.Services.Pass.GRPC, false); conn != nil {
		c.Pass = gen.NewDyProfileServiceClient(conn)
		// Passport hosts the NFC validation service on the same instance.
		c.Nfc = gen.NewDyNfcServiceClient(conn)
	}
	if conn := dial("blade", cfg.Services.Blade.GRPC, true); conn != nil {
		// Blade's wsgateway is the fleet's plaintext exception (plaintext h2c);
		// the TLS-always Dial would fail the handshake and silently degrade
		// every presence read. See DialInsecure.
		c.Blade = gen.NewWebSocketServiceClient(conn)
	}
	if conn := dial("ring", cfg.Services.Ring.GRPC, false); conn != nil {
		c.Ring = gen.NewDyRingServiceClient(conn)
	}
	return c, nil
}

func dialService(service, target string, plaintext bool, resolver *discovery.Resolver) (*grpc.ClientConn, error) {
	address := strings.TrimSpace(target)
	if resolver != nil {
		address = discovery.Target(service)
	}
	if address == "" {
		return nil, nil
	}
	if discovery.IsTarget(address) && resolver == nil {
		return nil, fmt.Errorf("dial %s: service discovery is not configured", service)
	}
	if plaintext {
		return DialInsecure(address)
	}
	return Dial(address)
}

// Close closes all dialed connections.
func (c *Clients) Close() {
	for _, conn := range c.conns {
		if conn != nil {
			_ = conn.Close()
		}
	}
}

// WalletPerkProvider hydrates perk subscriptions from the wallet service.
type WalletPerkProvider struct {
	Client gen.DySubscriptionServiceClient
	Log    *slog.Logger
}

// GetPerkSubscription mirrors RemoteSubscriptionService.GetPerkSubscription.
func (p *WalletPerkProvider) GetPerkSubscription(ctx context.Context, accountID string) (*model.SnSubscriptionReferenceObject, error) {
	if p == nil || p.Client == nil {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	resp, err := p.Client.GetPerkSubscription(ctx, &gen.DyGetPerkSubscriptionRequest{AccountId: accountID})
	if err != nil {
		return nil, err
	}
	// Mirrors RemoteSubscriptionService.GetPerkSubscription: the wallet returns
	// an empty Id as the "no active perk subscription" sentinel, which must map
	// to nil here (callers then set account.PerkSubscription = nil).
	if resp == nil || resp.Id == "" {
		return nil, nil
	}
	ref := &model.SnSubscriptionReferenceObject{
		Id:          resp.Id,
		Identifier:  resp.Identifier,
		DisplayName: &resp.DisplayName,
		PerkLevel:   int(resp.PerkLevel),
		IsActive:    resp.IsActive,
		IsAvailable: resp.IsAvailable,
		IsFreeTrial: resp.IsFreeTrial,
		Status:      int(resp.Status),
		BegunAt:     tsToModel(resp.BegunAt),
		EndedAt:     tsToModel(resp.EndedAt),
		RenewalAt:   tsToModel(resp.RenewalAt),
		BasePrice:   resp.BasePrice,
		FinalPrice:  resp.FinalPrice,
		AccountId:   resp.AccountId,
		CreatedAt:   tsToModel(resp.CreatedAt),
		UpdatedAt:   tsToModel(resp.UpdatedAt),
	}
	// SnWalletSubscriptionRef.createdAt/updatedAt are non-nullable in the Dart
	// SDK; a provider that omits them must still produce parseable instants.
	if ref.CreatedAt == nil {
		ref.CreatedAt = model.NewTime(time.Now().UTC())
	}
	if ref.UpdatedAt == nil {
		ref.UpdatedAt = ref.CreatedAt
	}
	return ref, nil
}

// DevelopAppProvider looks up OIDC clients from local configuration first,
// then falls back to Develop.
type DevelopAppProvider struct {
	Client gen.DyCustomAppServiceClient
	Cfg    *config.Config
	Log    *slog.Logger
}

// GetCustomAppSlug returns the custom app slug for an OIDC client id.
func (p *DevelopAppProvider) GetCustomAppSlug(ctx context.Context, appID string) (string, error) {
	if p != nil && p.Cfg != nil {
		if client := p.Cfg.FindLocalOAuthClientByID(appID); client != nil {
			slug := client.Slug
			if slug == "" {
				slug = client.Id
			}
			if slug != "" {
				return slug, nil
			}
		}
	}
	if p == nil || p.Client == nil {
		return "", fmt.Errorf("develop client not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	resp, err := p.Client.GetCustomApp(ctx, &gen.DyGetCustomAppRequest{
		Query: &gen.DyGetCustomAppRequest_Id{Id: appID},
	})
	if err != nil {
		if status.Code(err).String() == "NotFound" {
			return "", nil
		}
		return "", err
	}
	if resp == nil || resp.App == nil || resp.App.GetOauthConfig() == nil {
		return "", nil
	}
	return resp.App.Slug, nil
}

func tsToModel(t interface{ AsTime() time.Time }) *model.Time {
	if t == nil {
		return nil
	}
	return model.NewTime(t.AsTime())
}

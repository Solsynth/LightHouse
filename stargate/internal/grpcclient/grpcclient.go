// Package grpcclient holds the outbound gRPC clients Stargate uses to call
// sibling services (wallet for perks, develop for OIDC clients, drive for
// files, pass for badges, blade for websocket pushes, ring for
// notifications). Targets come from the [services] config section; when a
// target is empty the provider degrades gracefully (nil/empty results),
// matching the C# services' behavior with a dependency down.
package grpcclient

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/stargate/internal/config"
	"src.solsynth.dev/sosys/stargate/internal/model"
)

// Dial creates a TLS gRPC connection with CA validation skipped. The fleet
// uses self-signed certs issued by the DysonNetwork CA; per the Golaunch
// README, CA validation is off.
func Dial(target string) (*grpc.ClientConn, error) {
	if target == "" {
		return nil, nil
	}
	return grpc.NewClient(target,
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{InsecureSkipVerify: true})),
		grpc.WithDefaultCallOptions(grpc.WaitForReady(false)),
	)
}

// Clients holds the outbound clients.
type Clients struct {
	Wallet  gen.DySubscriptionServiceClient
	Develop gen.DyCustomAppServiceClient
	Drive   gen.DyFileServiceClient
	Pass    gen.DyProfileServiceClient
	Blade   gen.WebSocketServiceClient
	Ring    gen.DyRingServiceClient
	conns   []*grpc.ClientConn
}

// NewClients dials every configured target.
func NewClients(cfg *config.Config) (*Clients, error) {
	c := &Clients{}
	dial := func(target string) *grpc.ClientConn {
		conn, err := Dial(target)
		if err != nil {
			return nil
		}
		c.conns = append(c.conns, conn)
		return conn
	}
	if conn := dial(cfg.Services.Wallet.GRPC); conn != nil {
		c.Wallet = gen.NewDySubscriptionServiceClient(conn)
	}
	if conn := dial(cfg.Services.Develop.GRPC); conn != nil {
		c.Develop = gen.NewDyCustomAppServiceClient(conn)
	}
	if conn := dial(cfg.Services.Drive.GRPC); conn != nil {
		c.Drive = gen.NewDyFileServiceClient(conn)
	}
	if conn := dial(cfg.Services.Pass.GRPC); conn != nil {
		c.Pass = gen.NewDyProfileServiceClient(conn)
	}
	if conn := dial(cfg.Services.Blade.GRPC); conn != nil {
		c.Blade = gen.NewWebSocketServiceClient(conn)
	}
	if conn := dial(cfg.Services.Ring.GRPC); conn != nil {
		c.Ring = gen.NewDyRingServiceClient(conn)
	}
	return c, nil
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
	}
	if resp.UpdatedAt != nil {
		ref.UpdatedAt = tsToModel(resp.UpdatedAt)
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
	if resp == nil || resp.App == nil {
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

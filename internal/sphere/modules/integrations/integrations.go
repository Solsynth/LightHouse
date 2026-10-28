// Package integrations publishes the fleet integrations (accounts, files,
// notifications, realtime, payments, subscriptions, domain blocks) as ports.
// A missing target leaves the port unpublished, so a feature module that
// consumes it degrades instead of failing at boot.
package integrations

import (
	"context"
	"encoding/json"

	"src.solsynth.dev/sosys/go/pkg/models"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/lighthouse/internal/platform/eventbus"
	"src.solsynth.dev/sosys/lighthouse/internal/platform/grpcclient"
	"src.solsynth.dev/sosys/lighthouse/internal/platform/module"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/core"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/ports"
)

// ModuleID is the integrations module's registry ID.
const ModuleID = "app.solian.sphere.integrations"

// Factory returns the integrations module factory.
func Factory() module.Factory[core.Services] {
	return module.Factory[core.Services]{
		ID:             ModuleID,
		Kind:           module.KindIntegration,
		DefaultEnabled: true,
		Capabilities: []string{
			"integrations.accounts", "integrations.files", "integrations.realtime",
			"integrations.notify", "integrations.payments", "integrations.subscriptions",
			"integrations.domains",
		},
		New: func() module.Instance[core.Services] { return &instance{} },
	}
}

type instance struct{}

func (i *instance) Configure(context.Context, *module.Host[core.Services]) error { return nil }

func (i *instance) Stop(context.Context) error { return nil }

// ProvidePorts publishes every integration whose target is configured.
func (i *instance) ProvidePorts(_ context.Context, host *module.Host[core.Services]) error {
	if host.Core.Clients == nil {
		return nil
	}
	clients := host.Core.Clients
	if clients.Profile != nil {
		module.Provide[ports.Accounts](host.Ports, ports.PortAccounts, ModuleID,
			module.KindIntegration, &accountsPort{clients: clients})
	}
	if clients.Drive != nil {
		module.Provide[ports.Files](host.Ports, ports.PortFiles, ModuleID,
			module.KindIntegration, &filesPort{clients: clients})
	}
	if clients.Ring != nil {
		module.Provide[ports.Notify](host.Ports, ports.PortNotify, ModuleID,
			module.KindIntegration, &notifyPort{clients: clients})
	}
	if host.Core.NATS != nil && host.Core.NATS.Enabled() {
		module.Provide[ports.Realtime](host.Ports, ports.PortRealtime, ModuleID,
			module.KindIntegration, &realtimePort{bus: host.Core.NATS})
	}
	if clients.Wallet != nil {
		module.Provide[ports.Payments](host.Ports, ports.PortPayments, ModuleID,
			module.KindIntegration, &paymentsPort{clients: clients})
	}
	if clients.Subs != nil {
		module.Provide[ports.Subscriptions](host.Ports, ports.PortSubscriptions, ModuleID,
			module.KindIntegration, &subscriptionsPort{clients: clients})
	}
	if clients.DomainBlock != nil {
		module.Provide[ports.Domains](host.Ports, ports.PortDomains, ModuleID,
			module.KindIntegration, &domainsPort{clients: clients})
	}
	return nil
}

type accountsPort struct{ clients *grpcclient.Clients }

func (p *accountsPort) GetAccount(ctx context.Context, id string) (*gen.DyAccount, error) {
	if id == "" {
		return nil, ports.ErrUnavailable
	}
	return p.clients.Profile.GetAccount(ctx, &gen.DyGetAccountRequest{Id: id})
}

func (p *accountsPort) GetAccounts(ctx context.Context, ids []string) ([]*gen.DyAccount, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	resp, err := p.clients.Profile.GetAccountBatch(ctx, &gen.DyGetAccountBatchRequest{Id: ids})
	if err != nil {
		return nil, err
	}
	return resp.GetAccounts(), nil
}

func (p *accountsPort) SearchAccounts(ctx context.Context, query string, take int) ([]*gen.DyAccount, error) {
	resp, err := p.clients.Profile.SearchAccount(ctx, &gen.DySearchAccountRequest{Query: query})
	if err != nil {
		return nil, err
	}
	accounts := resp.GetAccounts()
	if take > 0 && len(accounts) > take {
		accounts = accounts[:take]
	}
	return accounts, nil
}

func (p *accountsPort) HasRelationship(ctx context.Context, accountID, relatedID string) (bool, error) {
	value, err := p.clients.Profile.HasRelationship(ctx, &gen.DyGetRelationshipRequest{
		AccountId: accountID, RelatedId: relatedID, EitherDirection: true,
	})
	if err != nil {
		return false, err
	}
	return value.GetValue(), nil
}

type filesPort struct{ clients *grpcclient.Clients }

func (p *filesPort) GetFile(ctx context.Context, id string) (*model.CloudFileReferenceObject, error) {
	if id == "" {
		return nil, ports.ErrUnavailable
	}
	file, err := p.clients.Drive.GetFile(ctx, &gen.DyGetFileRequest{Id: id})
	if err != nil {
		return nil, err
	}
	reference := models.FromProtoValue(file)
	return &reference, nil
}

func (p *filesPort) GetFiles(ctx context.Context, ids []string) (map[string]model.CloudFileReferenceObject, error) {
	out := map[string]model.CloudFileReferenceObject{}
	if len(ids) == 0 {
		return out, nil
	}
	resp, err := p.clients.Drive.GetFileBatch(ctx, &gen.DyGetFileBatchRequest{Ids: ids})
	if err != nil {
		return nil, err
	}
	for _, file := range resp.GetFiles() {
		out[file.GetId()] = models.FromProtoValue(file)
	}
	return out, nil
}

type notifyPort struct{ clients *grpcclient.Clients }

func (p *notifyPort) SendPush(ctx context.Context, accountID, title, body string, meta map[string]string) error {
	return p.SendPushToAccounts(ctx, []string{accountID}, title, body, meta)
}

func (p *notifyPort) SendPushToAccounts(ctx context.Context, accountIDs []string, title, body string, meta map[string]string) error {
	if len(accountIDs) == 0 {
		return nil
	}
	notification := &gen.DyPushNotification{Title: title, Body: body}
	if len(meta) > 0 {
		if raw, err := json.Marshal(meta); err == nil {
			notification.Meta = raw
		}
	}
	_, err := p.clients.Ring.SendPushNotificationToUsers(ctx, &gen.DySendPushNotificationToUsersRequest{
		UserIds: accountIDs, Notification: notification,
	})
	return err
}

type realtimePort struct{ bus *eventbus.Client }

func (p *realtimePort) Push(ctx context.Context, accountIDs []string, event string, payload any) error {
	return p.bus.PublishWSToAccounts(ctx, accountIDs, event, payload, "")
}

type paymentsPort struct{ clients *grpcclient.Clients }

func (p *paymentsPort) CreateOrder(ctx context.Context, accountID, productIdentifier, amount, currency string, meta map[string]any) (string, error) {
	if currency == "" {
		currency = "gold"
	}
	request := &gen.DyCreateOrderRequest{
		Currency:          currency,
		Amount:            amount,
		ProductIdentifier: &productIdentifier,
	}
	if len(meta) > 0 {
		raw, err := json.Marshal(meta)
		if err != nil {
			return "", err
		}
		request.Meta = raw
	}
	order, err := p.clients.Wallet.CreateOrder(ctx, request)
	if err != nil {
		return "", err
	}
	return order.GetId(), nil
}

type subscriptionsPort struct{ clients *grpcclient.Clients }

func (p *subscriptionsPort) GetPerkSubscription(ctx context.Context, accountID string) (*model.SubscriptionRef, error) {
	if accountID == "" {
		return nil, nil
	}
	sub, err := p.clients.Subs.GetPerkSubscription(ctx, &gen.DyGetPerkSubscriptionRequest{AccountId: accountID})
	if err != nil || sub == nil {
		return nil, err
	}
	return &model.SubscriptionRef{
		ID:          sub.GetId(),
		Identifier:  sub.GetIdentifier(),
		PerkLevel:   int(sub.GetPerkLevel()),
		IsActive:    sub.GetIsActive(),
		IsAvailable: sub.GetIsAvailable(),
		IsFreeTrial: sub.GetIsFreeTrial(),
		Status:      int(sub.GetStatus()),
	}, nil
}

type domainsPort struct{ clients *grpcclient.Clients }

func (p *domainsPort) ValidateURL(ctx context.Context, rawURL string) (bool, error) {
	resp, err := p.clients.DomainBlock.ValidateUrl(ctx, &gen.DyValidateUrlRequest{Url: rawURL})
	if err != nil {
		return false, err
	}
	return resp.GetIsAllowed(), nil
}

func (p *domainsPort) IsDomainBlocked(ctx context.Context, domain string) (bool, error) {
	resp, err := p.clients.DomainBlock.IsDomainBlocked(ctx, &gen.DyDomainBlockCheckRequest{Host: domain})
	if err != nil {
		return false, err
	}
	return resp.GetIsBlocked(), nil
}

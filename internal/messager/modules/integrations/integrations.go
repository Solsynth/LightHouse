// Package integrations publishes Messager's fleet integrations as ports.
package integrations

import (
	"context"
	"encoding/json"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"src.solsynth.dev/sosys/lighthouse/shared/pkg/models"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"

	"src.solsynth.dev/sosys/lighthouse/internal/messager/core"
	"src.solsynth.dev/sosys/lighthouse/internal/messager/model"
	"src.solsynth.dev/sosys/lighthouse/internal/messager/ports"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/eventbus"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/grpcclient"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
)

// ModuleID is the integrations module's registry ID.
const ModuleID = "app.solian.messager.integrations"

// Factory returns the integrations module factory.
func Factory() module.Factory[core.Services] {
	return module.Factory[core.Services]{
		ID:             ModuleID,
		Kind:           module.KindIntegration,
		DefaultEnabled: true,
		Capabilities: []string{
			"integrations.accounts", "integrations.files", "integrations.notify",
			"integrations.realtime", "integrations.sphere",
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
	if clients.Realm != nil {
		module.Provide[ports.Realms](host.Ports, ports.PortRealms, ModuleID,
			module.KindIntegration, &realmsPort{clients: clients})
	}
	if clients.Surveys != nil || clients.Stickers != nil || clients.Autocomplete != nil ||
		clients.WebReader != nil || clients.Publishers != nil {
		module.Provide[ports.Sphere](host.Ports, ports.PortSphere, ModuleID,
			module.KindIntegration, &spherePort{clients: clients})
	}
	return nil
}

type accountsPort struct{ clients *grpcclient.Clients }

func (p *accountsPort) GetAccount(ctx context.Context, id string) (*gen.DyAccount, error) {
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

func (p *accountsPort) LookupAccounts(ctx context.Context, names []string) ([]*gen.DyAccount, error) {
	if len(names) == 0 {
		return nil, nil
	}
	resp, err := p.clients.Profile.LookupAccountBatch(ctx, &gen.DyLookupAccountBatchRequest{Names: names})
	if err != nil {
		return nil, err
	}
	return resp.GetAccounts(), nil
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

func (p *notifyPort) SendPush(ctx context.Context, accountID, topic, title, body string, meta map[string]any) error {
	return p.SendPushToAccounts(ctx, []string{accountID}, topic, title, body, meta)
}

func (p *notifyPort) SendPushToAccounts(ctx context.Context, accountIDs []string, topic, title, body string, meta map[string]any) error {
	if len(accountIDs) == 0 {
		return nil
	}
	notification := &gen.DyPushNotification{Topic: topic, Title: title, Body: body}
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

type spherePort struct{ clients *grpcclient.Clients }

func (p *spherePort) GetSurvey(ctx context.Context, id string) (*gen.DySurvey, error) {
	if p.clients.Surveys == nil {
		return nil, ports.ErrUnavailable
	}
	return p.clients.Surveys.GetSurvey(ctx, &gen.DyGetSurveyRequest{Identifier: &gen.DyGetSurveyRequest_Id{Id: id}})
}

func (p *spherePort) GetStickerByIdentifier(ctx context.Context, identifier string) (*gen.DySticker, error) {
	if p.clients.Stickers == nil {
		return nil, ports.ErrUnavailable
	}
	return p.clients.Stickers.GetStickerByIdentifier(ctx, &gen.DyGetStickerRequest{Identifier: identifier})
}

func (p *spherePort) Autocomplete(ctx context.Context, content, chatID, realmID string, limit int) ([]byte, error) {
	if p.clients.Autocomplete == nil {
		return nil, ports.ErrUnavailable
	}
	resp, err := p.clients.Autocomplete.Autocomplete(ctx, &gen.DyAutocompletionRequest{
		Content: content, ChatId: chatID, RealmId: realmID,
	})
	if err != nil {
		return nil, err
	}
	return protojson.Marshal(resp)
}

func (p *spherePort) GetLinkPreview(ctx context.Context, url string) ([]byte, error) {
	if p.clients.WebReader == nil {
		return nil, ports.ErrUnavailable
	}
	resp, err := p.clients.WebReader.GetLinkPreview(ctx, &gen.DyGetLinkPreviewRequest{Url: url})
	if err != nil {
		return nil, err
	}
	return protojson.Marshal(resp)
}

func (p *spherePort) IsPublisherMember(ctx context.Context, publisherID, accountID string) (bool, error) {
	if p.clients.Publishers == nil {
		return false, ports.ErrUnavailable
	}
	resp, err := p.clients.Publishers.IsPublisherMember(ctx, &gen.DyIsPublisherMemberRequest{
		PublisherId: publisherID, AccountId: accountID,
	})
	if err != nil {
		return false, err
	}
	return resp.GetValid(), nil
}

var _ = strings.TrimSpace

// realmsPort resolves realm snapshots from Passport/Stargate.
type realmsPort struct{ clients *grpcclient.Clients }

func (p *realmsPort) GetRealm(ctx context.Context, id string) (*ports.RealmInfo, error) {
	if p.clients.Realm == nil || id == "" {
		return nil, ports.ErrUnavailable
	}
	realm, err := p.clients.Realm.GetRealm(ctx, &gen.DyGetRealmRequest{
		Query: &gen.DyGetRealmRequest_Id{Id: id},
	})
	if err != nil {
		return nil, err
	}
	return realmInfo(realm), nil
}

func (p *realmsPort) GetRealmBySlug(ctx context.Context, slug string) (*ports.RealmInfo, error) {
	if p.clients.Realm == nil || slug == "" {
		return nil, ports.ErrUnavailable
	}
	realm, err := p.clients.Realm.GetRealm(ctx, &gen.DyGetRealmRequest{
		Query: &gen.DyGetRealmRequest_Slug{Slug: slug},
	})
	if err != nil {
		return nil, err
	}
	return realmInfo(realm), nil
}

func realmInfo(realm *gen.DyRealm) *ports.RealmInfo {
	if realm == nil {
		return nil
	}
	info := &ports.RealmInfo{
		ID:       realm.GetId(),
		Slug:     realm.GetSlug(),
		Name:     realm.GetName(),
		IsPublic: realm.GetIsPublic(),
	}
	if description := realm.GetDescription(); description != "" {
		info.Description = &description
	}
	return info
}

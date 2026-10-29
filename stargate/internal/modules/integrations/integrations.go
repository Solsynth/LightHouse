// Package integrations contains adapters for the optional Solar Network
// services. Each one is a module: disabling it removes the gRPC target and
// leaves the dependent feature unavailable, without affecting standalone
// operation.
package integrations

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"src.solsynth.dev/sosys/go/pkg/models"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/stargate/internal/auth"
	"src.solsynth.dev/sosys/stargate/internal/config"
	"src.solsynth.dev/sosys/stargate/internal/grpcclient"
	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/module"
	"src.solsynth.dev/sosys/stargate/internal/ports"
)

// Drive provides the files port through DysonFS/Drive.
func Drive() module.Factory {
	return integration("app.solian.drive", []module.PortID{ports.PortFiles})
}

// Wallet provides subscriptions/perks.
func Wallet() module.Factory {
	return integration("app.solian.wallet", []module.PortID{ports.PortPerks})
}

// Pass provides badges, verification and remote NFC token validation.
func Pass() module.Factory {
	return integration("app.solian.pass", []module.PortID{ports.PortBadges, ports.PortVerification, ports.PortNfcTokens})
}

// Ring provides email and push notifications.
func Ring() module.Factory {
	return integration("app.solian.ring", []module.PortID{ports.PortEmail, ports.PortPush})
}

// Blade provides presence (and service discovery is handled by the app).
func Blade() module.Factory {
	return integration("app.solian.blade", []module.PortID{ports.PortPresence})
}

// Develop provides custom OAuth/OIDC client lookup.
func Develop() module.Factory {
	return integration("app.solian.develop", []module.PortID{ports.PortOAuthClients})
}

func integration(id string, provides []module.PortID) module.Factory {
	return module.Factory{
		ID:             id,
		Kind:           module.KindIntegration,
		DefaultEnabled: true,
		Provides:       provides,
		New:            func() module.Instance { return &instance{id: id} },
	}
}

type instance struct {
	id string
}

func (i *instance) Configure(context.Context, *module.Host) error { return nil }
func (i *instance) Stop(context.Context) error                    { return nil }

func (i *instance) ProvidePorts(_ context.Context, host *module.Host) error {
	c := host.Core
	switch i.id {
	case "app.solian.drive":
		if c.Clients != nil && c.Clients.Drive != nil {
			module.Provide[ports.Files](host.Ports, ports.PortFiles, i.id, module.KindIntegration, &filesAdapter{client: c.Clients.Drive})
		}
	case "app.solian.wallet":
		if c.Clients != nil && c.Clients.Wallet != nil {
			module.Provide[ports.Perks](host.Ports, ports.PortPerks, i.id, module.KindIntegration, &perksAdapter{client: c.Clients.Wallet})
		}
	case "app.solian.pass":
		if c.Clients != nil && c.Clients.Pass != nil {
			module.Provide[ports.Badges](host.Ports, ports.PortBadges, i.id, module.KindIntegration, &badgesAdapter{client: c.Clients.Pass})
			module.Provide[ports.Verification](host.Ports, ports.PortVerification, i.id, module.KindIntegration, &verificationAdapter{client: c.Clients.Pass})
		}
		if c.Clients != nil && c.Clients.Nfc != nil {
			module.Provide[ports.NfcTokens](host.Ports, ports.PortNfcTokens, i.id, module.KindIntegration, &nfcAdapter{client: c.Clients.Nfc})
		}
	case "app.solian.ring":
		if c.Clients != nil && c.Clients.Ring != nil {
			module.Provide[ports.Email](host.Ports, ports.PortEmail, i.id, module.KindIntegration, &emailAdapter{client: c.Clients.Ring})
			module.Provide[ports.Push](host.Ports, ports.PortPush, i.id, module.KindIntegration, &pushAdapter{client: c.Clients.Ring})
		}
	case "app.solian.blade":
		if c.Presence != nil {
			module.Provide[ports.Presence](host.Ports, ports.PortPresence, i.id, module.KindIntegration, &presenceAdapter{presence: c.Presence})
		}
	case "app.solian.develop":
		if c.Clients != nil && c.Clients.Develop != nil {
			module.Provide[ports.OAuthClients](host.Ports, ports.PortOAuthClients, i.id, module.KindIntegration, &oauthAdapter{
				client: c.Clients.Develop, cfg: c.Config, log: c.Log,
			})
		}
	}
	return nil
}

// ── files ──────────────────────────────────────────────────────────────────

type filesAdapter struct {
	client gen.DyFileServiceClient
}

func (a *filesAdapter) Available() bool { return a != nil && a.client != nil }

func (a *filesAdapter) Resolve(ctx context.Context, fileID string) (*model.SnCloudFileReferenceObject, error) {
	if !a.Available() {
		return nil, ports.ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	file, err := a.client.GetFile(ctx, &gen.DyGetFileRequest{Id: fileID})
	if err != nil {
		return nil, err
	}
	ref := models.FromProtoValue(file)
	return &ref, nil
}

// ── perks ──────────────────────────────────────────────────────────────────

type perksAdapter struct {
	client gen.DySubscriptionServiceClient
}

func (a *perksAdapter) Available() bool { return a != nil && a.client != nil }

func (a *perksAdapter) Subscription(ctx context.Context, accountID string) (*model.SnSubscriptionReferenceObject, error) {
	if !a.Available() {
		return nil, ports.ErrUnavailable
	}
	provider := &grpcclient.WalletPerkProvider{Client: a.client}
	return provider.GetPerkSubscription(ctx, accountID)
}

// ── badges / verification ──────────────────────────────────────────────────

type badgesAdapter struct {
	client gen.DyProfileServiceClient
}

func (a *badgesAdapter) Available() bool { return a != nil && a.client != nil }

func (a *badgesAdapter) List(ctx context.Context, accountID string) ([]any, error) {
	if !a.Available() {
		return nil, ports.ErrUnavailable
	}
	resp, err := a.client.ListBadges(ctx, &gen.DyListBadgesRequest{AccountId: accountID})
	if err != nil || resp == nil {
		return nil, err
	}
	badges := make([]any, 0, len(resp.Badges))
	for _, badge := range resp.Badges {
		raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(badge)
		if err != nil {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			continue
		}
		if _, ok := m["meta"]; !ok {
			m["meta"] = map[string]any{}
		}
		badges = append(badges, m)
	}
	return badges, nil
}

type verificationAdapter struct {
	client gen.DyProfileServiceClient
}

func (a *verificationAdapter) Available() bool { return a != nil && a.client != nil }

func (a *verificationAdapter) Get(ctx context.Context, accountID string) (*model.SnVerificationMark, error) {
	if !a.Available() {
		return nil, ports.ErrUnavailable
	}
	resp, err := a.client.GetProfile(ctx, &gen.DyGetProfileRequest{AccountId: accountID})
	if err != nil || resp == nil {
		return nil, err
	}
	v := resp.Verification
	if v == nil {
		return nil, nil
	}
	mark := &model.SnVerificationMark{Type: int(v.Type)}
	if v.Title != "" {
		mark.Title = &v.Title
	}
	if v.Description != "" {
		mark.Description = &v.Description
	}
	if v.VerifiedBy != "" {
		mark.VerifiedBy = &v.VerifiedBy
	}
	return mark, nil
}

// ── nfc ────────────────────────────────────────────────────────────────────

type nfcAdapter struct {
	client gen.DyNfcServiceClient
}

func (a *nfcAdapter) Available() bool { return a != nil && a.client != nil }

func (a *nfcAdapter) Validate(ctx context.Context, tagUID, uidHex string) (string, string, error) {
	if !a.Available() {
		return "", "", ports.ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	resp, err := a.client.ValidateNfcToken(ctx, &gen.DyValidateNfcTokenRequest{TagUid: tagUID, UidHex: uidHex})
	if err != nil {
		return "", "", err
	}
	if resp == nil || !resp.IsValid {
		if resp != nil && resp.ErrorCode != "" {
			return "", "", fmt.Errorf("nfc token rejected: %s", resp.ErrorCode)
		}
		return "", "", fmt.Errorf("nfc token rejected")
	}
	return resp.AccountId, resp.TagId, nil
}

// ── email / push ───────────────────────────────────────────────────────────

type emailAdapter struct {
	client gen.DyRingServiceClient
}

func (a *emailAdapter) Available() bool { return a != nil && a.client != nil }

func (a *emailAdapter) Send(ctx context.Context, msg ports.EmailMessage) error {
	if !a.Available() {
		return ports.ErrUnavailable
	}
	body := msg.HTML
	if body == "" {
		body = msg.Text
	}
	_, err := a.client.SendEmail(ctx, &gen.DySendEmailRequest{
		Email: &gen.DyEmailMessage{
			ToAddress: msg.To,
			Subject:   msg.Subject,
			Body:      body,
		},
	})
	return err
}

type pushAdapter struct {
	client gen.DyRingServiceClient
}

func (a *pushAdapter) Available() bool { return a != nil && a.client != nil }

func (a *pushAdapter) SendToUser(ctx context.Context, accountID, title, body string) error {
	if !a.Available() {
		return ports.ErrUnavailable
	}
	_, err := a.client.SendPushNotificationToUser(ctx, &gen.DySendPushNotificationToUserRequest{
		UserId: accountID,
		Notification: &gen.DyPushNotification{
			Title: title,
			Body:  body,
		},
	})
	return err
}

// ── presence ───────────────────────────────────────────────────────────────

type presenceAdapter struct {
	presence *auth.DevicePresence
}

func (a *presenceAdapter) Available() bool { return a != nil && a.presence != nil }

func (a *presenceAdapter) OnlineDevices(ctx context.Context, accountIDs []string, namespace string) (map[string][]ports.OnlineDevice, error) {
	if !a.Available() {
		return nil, ports.ErrUnavailable
	}
	raw, err := a.presence.ForAccounts(ctx, accountIDs, namespace)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]ports.OnlineDevice, len(raw))
	for accountID, devices := range raw {
		list := make([]ports.OnlineDevice, 0, len(devices))
		for _, device := range devices {
			list = append(list, ports.OnlineDevice{
				AccountID: accountID, DeviceID: device.DeviceKey,
				ClientID: device.Client.Id, SessionIDs: device.SessionIDs,
			})
		}
		out[accountID] = list
	}
	return out, nil
}

// ── oauth clients ──────────────────────────────────────────────────────────

// oauthAdapter resolves custom apps from the Develop service. The
// Stargate-owned OAuth app module is the feature provider for this port and
// chains configuration, its own table and then this adapter; selecting
// [ports] oauthClients = "app.solian.develop" bypasses the module.
type oauthAdapter struct {
	client gen.DyCustomAppServiceClient
	cfg    *config.Config
	log    *slog.Logger
}

func (a *oauthAdapter) Available() bool { return a != nil && (a.client != nil || a.cfg != nil) }

func (a *oauthAdapter) Get(ctx context.Context, id string) (*ports.OAuthApp, error) {
	if a == nil {
		return nil, ports.ErrUnavailable
	}
	if a.cfg != nil {
		if client := a.cfg.FindLocalOAuthClientByID(id); client != nil {
			return LocalOAuthApp(client), nil
		}
	}
	if a.client == nil {
		return nil, ports.ErrUnavailable
	}
	resp, err := a.client.GetCustomApp(ctx, &gen.DyGetCustomAppRequest{
		Query: &gen.DyGetCustomAppRequest_Id{Id: id},
	})
	if err != nil || resp == nil || resp.App == nil {
		return nil, err
	}
	return OAuthAppFromProto(resp.App), nil
}

func (a *oauthAdapter) GetBySlug(ctx context.Context, slug string) (*ports.OAuthApp, error) {
	if a == nil {
		return nil, ports.ErrUnavailable
	}
	if a.cfg != nil {
		if client := a.cfg.FindLocalOAuthClient(slug); client != nil {
			return LocalOAuthApp(client), nil
		}
	}
	if a.client == nil {
		return nil, ports.ErrUnavailable
	}
	resp, err := a.client.GetCustomApp(ctx, &gen.DyGetCustomAppRequest{
		Query: &gen.DyGetCustomAppRequest_Slug{Slug: slug},
	})
	if err != nil || resp == nil || resp.App == nil {
		return nil, err
	}
	return OAuthAppFromProto(resp.App), nil
}

func (a *oauthAdapter) CheckSecret(ctx context.Context, appID, secret string) (bool, error) {
	if a == nil {
		return false, ports.ErrUnavailable
	}
	if a.cfg != nil {
		if client := a.cfg.FindLocalOAuthClientByID(appID); client != nil {
			if client.ClientSecret == "" {
				return false, nil
			}
			return subtle.ConstantTimeCompare([]byte(client.ClientSecret), []byte(secret)) == 1, nil
		}
	}
	if a.client == nil {
		return false, nil
	}
	isOidc := true
	resp, err := a.client.CheckCustomAppSecret(ctx, &gen.DyCheckCustomAppSecretRequest{
		SecretIdentifier: &gen.DyCheckCustomAppSecretRequest_AppId{AppId: appID},
		Secret:           secret,
		IsOidc:           &isOidc,
	})
	if err != nil {
		return false, err
	}
	return resp.GetValid(), nil
}

// LocalOAuthApp maps a configured TOML client onto the port shape.
func LocalOAuthApp(client *config.OAuthClient) *ports.OAuthApp {
	id := client.Id
	slug := client.Slug
	if id == "" {
		id = slug
	}
	if slug == "" {
		slug = id
	}
	status := client.Status
	if status == 0 {
		status = 2 // CustomAppStatus.Production
	}
	app := &ports.OAuthApp{
		ID: id, Slug: slug, Name: client.Name,
		ClientSecret: client.ClientSecret, Status: status,
		RedirectURIs: client.RedirectUris, Scopes: client.AllowedScopes,
		IsPublicClient: client.IsPublicClient,
	}
	if client.HomeUri != "" {
		app.HomeUri = &client.HomeUri
	}
	if client.PolicyUri != "" {
		app.PolicyUri = &client.PolicyUri
	}
	if client.TermsOfServiceUri != "" {
		app.TermsOfServiceUri = &client.TermsOfServiceUri
	}
	return app
}

// OAuthAppFromProto maps a Develop custom app onto the port shape.
func OAuthAppFromProto(app *gen.DyCustomApp) *ports.OAuthApp {
	out := &ports.OAuthApp{
		ID: app.GetId(), Slug: app.GetSlug(), Name: app.GetName(), Status: int(app.GetStatus()),
	}
	if f := app.GetPicture(); f != nil {
		ref := models.FromProtoValue(f)
		out.Picture = &ref
	}
	if f := app.GetBackground(); f != nil {
		ref := models.FromProtoValue(f)
		out.Background = &ref
	}
	if l := app.GetLinks(); l != nil {
		if v := l.GetHomePage(); v != "" {
			out.HomeUri = &v
		}
		if v := l.GetPrivacyPolicy(); v != "" {
			out.PolicyUri = &v
		}
		if v := l.GetTermsOfService(); v != "" {
			out.TermsOfServiceUri = &v
		}
	}
	if o := app.GetOauthConfig(); o != nil {
		out.RedirectURIs = o.GetRedirectUris()
		out.PostLogoutRedirectURIs = o.GetPostLogoutRedirectUris()
		out.Scopes = o.GetAllowedScopes()
		out.RequirePkce = o.GetRequirePkce()
		out.AllowOfflineAccess = o.GetAllowOfflineAccess()
		out.IsPublicClient = o.GetIsPublicClient()
	}
	return out
}

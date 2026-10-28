package fediverse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

// discoveryService fetches remote ActivityPub documents through the SSRF-guarded
// client and mirrors actors/instances into the local tables.
type discoveryService struct {
	s *server
}

func (d *discoveryService) fetchDocument(ctx context.Context, target string, signAs *store.PublisherEntity) (map[string]any, error) {
	if err := validateOutboundURL(target); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/activity+json, application/ld+json")
	req.Header.Set("User-Agent", "DysonNetwork/1.0 (Lighthouse)")
	if signAs != nil {
		if err := d.s.signRequestAs(ctx, req, nil, signAs); err != nil {
			d.s.log.Debug("failed to sign discovery request", "url", target, "error", err)
		}
	}
	resp, err := d.s.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fetch %s: %s", target, resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("decode %s: %w", target, err)
	}
	return document, nil
}

func (d *discoveryService) ensureInstance(ctx context.Context, domain string) (*store.FediverseInstanceEntity, error) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	var instance store.FediverseInstanceEntity
	err := d.s.db.WithContext(ctx).Where("domain = ?", domain).First(&instance).Error
	if err == nil {
		return &instance, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	name := domain
	instance = store.FediverseInstanceEntity{ID: uuid.New(), Domain: domain, Name: &name}
	if createErr := d.s.db.WithContext(ctx).Create(&instance).Error; createErr != nil {
		if row, lookupErr := d.instanceByDomain(ctx, domain); lookupErr == nil {
			return row, nil
		}
		return nil, createErr
	}
	d.fetchInstanceMetadata(ctx, &instance)
	return &instance, nil
}

func (d *discoveryService) instanceByDomain(ctx context.Context, domain string) (*store.FediverseInstanceEntity, error) {
	var instance store.FediverseInstanceEntity
	if err := d.s.db.WithContext(ctx).Where("domain = ?", domain).First(&instance).Error; err != nil {
		return nil, err
	}
	return &instance, nil
}

// getOrCreateActor returns the mirror row for a remote actor URI, creating it
// from the actor document when absent.
func (d *discoveryService) getOrCreateActor(ctx context.Context, actorURI, username string, instanceID *uuid.UUID) (*store.PublisherEntity, error) {
	var existing store.PublisherEntity
	err := d.s.db.WithContext(ctx).Unscoped().Where("uri = ?", actorURI).First(&existing).Error
	if err == nil {
		touch := false
		if existing.DeletedAt.Valid {
			existing.DeletedAt = gorm.DeletedAt{}
			touch = true
		}
		if existing.PublicKey == nil || *existing.PublicKey == "" {
			if fetchErr := d.fetchActorData(ctx, &existing); fetchErr == nil {
				touch = false
			}
		}
		if touch {
			_ = d.s.db.WithContext(ctx).Unscoped().Save(&existing).Error
		}
		return &existing, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	parsed, err := url.Parse(actorURI)
	if err != nil || parsed.Host == "" {
		return nil, fmt.Errorf("invalid actor URI %q", actorURI)
	}
	instance, err := d.ensureInstance(ctx, parsed.Host)
	if err != nil {
		return nil, err
	}
	if instanceID != nil {
		var row store.FediverseInstanceEntity
		if err := d.s.db.WithContext(ctx).Where("id = ?", *instanceID).First(&row).Error; err == nil {
			instance = &row
		}
	}

	document, err := d.fetchDocument(ctx, actorURI, nil)
	if err != nil {
		return nil, err
	}
	if username == "" {
		username = getString(document, "preferredUsername")
	}
	if username == "" {
		username = actorURI[strings.LastIndex(actorURI, "/")+1:]
	}
	handle := username
	if parsed.Host != d.s.domain {
		handle = username + "@" + parsed.Host
	}
	nick := getString(document, "name")
	if nick == "" {
		nick = username
	}
	bio := getString(document, "summary")

	var instanceDomain *string
	if parsed.Host != "" {
		instanceDomain = &parsed.Host
	}
	actorType := getString(document, "type")
	if actorType == "" {
		actorType = "Person"
	}
	actor := &store.PublisherEntity{
		ID:             uuid.New(),
		Type:           int(model.PublisherFediverse),
		Name:           handle,
		Nick:           nick,
		Bio:            stringPtr(bio),
		URI:            &actorURI,
		ActorType:      &actorType,
		Username:       &username,
		InstanceID:     &instance.ID,
		InstanceDomain: instanceDomain,
		IsBot:          boolValue(document["bot"]),
		IsLocked:       boolValue(document["manuallyApprovesFollowers"]),
		IsDiscoverable: boolValue(document["discoverable"]),
	}
	if inbox := getString(document, "inbox"); inbox != "" {
		actor.InboxURI = &inbox
	}
	if outbox := getString(document, "outbox"); outbox != "" {
		actor.OutboxURI = &outbox
	}
	if followers := getString(document, "followers"); followers != "" {
		actor.FollowersURI = &followers
	}
	if following := getString(document, "following"); following != "" {
		actor.FollowingURI = &following
	}
	if featured := getString(document, "featured"); featured != "" {
		actor.FeaturedURI = &featured
	}
	if icon := getObject(document, "icon"); icon != nil {
		if raw := getString(icon, "url"); raw != "" {
			actor.Picture = store.ToJSON(externalFileReference(raw))
		}
	}
	if image := getObject(document, "image"); image != nil {
		if raw := getString(image, "url"); raw != "" {
			actor.Background = store.ToJSON(externalFileReference(raw))
		}
	}
	now := time.Now().UTC()
	actor.LastFetchedAt = &now
	actor.LastActivityAt = &now
	if publicKey := getObject(document, "publicKey"); publicKey != nil {
		if pem := getString(publicKey, "publicKeyPem"); pem != "" {
			actor.PublicKey = &pem
		}
		if keyID := getString(publicKey, "id"); keyID != "" {
			actor.PublicKeyID = &keyID
		}
	}

	if err := d.s.db.WithContext(ctx).Create(actor).Error; err != nil {
		if row, lookupErr := d.actorByURI(ctx, actorURI); lookupErr == nil {
			return row, nil
		}
		return nil, err
	}
	if actor.PublicKey != nil && actor.PublicKeyID != nil {
		d.s.keys.storeRemoteKey(ctx, *actor.PublicKeyID, *actor.PublicKey, &actor.ID)
	}
	d.s.log.Info("discovered remote actor", "uri", actorURI, "handle", handle)
	return actor, nil
}

func (d *discoveryService) actorByURI(ctx context.Context, actorURI string) (*store.PublisherEntity, error) {
	var actor store.PublisherEntity
	if err := d.s.db.WithContext(ctx).Unscoped().Where("uri = ?", actorURI).First(&actor).Error; err != nil {
		return nil, err
	}
	return &actor, nil
}

// fetchActorData refreshes a mirror row from its actor document.
func (d *discoveryService) fetchActorData(ctx context.Context, actor *store.PublisherEntity) error {
	if actor.URI == nil || *actor.URI == "" {
		return errors.New("actor has no URI")
	}
	document, err := d.fetchDocument(ctx, *actor.URI, nil)
	if err != nil {
		return err
	}
	if nick := getString(document, "name"); nick != "" {
		actor.Nick = nick
	}
	if summary := getString(document, "summary"); summary != "" {
		actor.Bio = &summary
	}
	if inbox := getString(document, "inbox"); inbox != "" {
		actor.InboxURI = &inbox
	}
	if outbox := getString(document, "outbox"); outbox != "" {
		actor.OutboxURI = &outbox
	}
	if followers := getString(document, "followers"); followers != "" {
		actor.FollowersURI = &followers
	}
	if following := getString(document, "following"); following != "" {
		actor.FollowingURI = &following
	}
	if featured := getString(document, "featured"); featured != "" {
		actor.FeaturedURI = &featured
	}
	if publicKey := getObject(document, "publicKey"); publicKey != nil {
		if pem := getString(publicKey, "publicKeyPem"); pem != "" {
			actor.PublicKey = &pem
		}
		if keyID := getString(publicKey, "id"); keyID != "" {
			actor.PublicKeyID = &keyID
		}
	}
	if icon := getObject(document, "icon"); icon != nil {
		if raw := getString(icon, "url"); raw != "" {
			actor.Picture = store.ToJSON(externalFileReference(raw))
		}
	}
	now := time.Now().UTC()
	actor.LastFetchedAt = &now
	return d.s.db.WithContext(ctx).Save(actor).Error
}

// fetchInstanceMetadata reads the instance's nodeinfo document.
func (d *discoveryService) fetchInstanceMetadata(ctx context.Context, instance *store.FediverseInstanceEntity) {
	if instance == nil || instance.Domain == "" {
		return
	}
	discoveryURL := "https://" + instance.Domain + "/.well-known/nodeinfo"
	discovery, err := d.fetchDocument(ctx, discoveryURL, nil)
	if err != nil {
		return
	}
	links := getObjects(discovery, "links")
	href := ""
	for _, raw := range links {
		link, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		rel := getString(link, "rel")
		if strings.Contains(rel, "schema/2.1") {
			href = getString(link, "href")
			break
		}
		if href == "" && strings.Contains(rel, "schema/2.0") {
			href = getString(link, "href")
		}
	}
	if href == "" {
		return
	}
	document, err := d.fetchDocument(ctx, href, nil)
	if err != nil {
		return
	}
	if software := getObject(document, "software"); software != nil {
		if name := getString(software, "name"); name != "" {
			instance.Software = &name
		}
		if version := getString(software, "version"); version != "" {
			instance.Version = &version
		}
	}
	if usage := getObject(document, "usage"); usage != nil {
		if users := getObject(usage, "users"); users != nil {
			if total, ok := users["total"].(float64); ok {
				count := int(total)
				instance.ActiveUsers = &count
			}
		}
	}
	if name := getString(document, "metadata"); name != "" {
		instance.Metadata = store.ToJSON(document["metadata"])
	}
	now := time.Now().UTC()
	instance.LastFetchedAt = &now
	instance.MetadataFetchedAt = &now
	if err := d.s.db.WithContext(ctx).Save(instance).Error; err != nil {
		d.s.log.Debug("failed to save instance metadata", "domain", instance.Domain, "error", err)
	}
}

// searchActors searches local mirrors, then tries to resolve a full handle or
// actor URL when includeRemote is set.
func (d *discoveryService) searchActors(ctx context.Context, query string, limit int, includeRemote bool) ([]store.PublisherEntity, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 20
	}
	like := "%" + strings.ToLower(query) + "%"
	var actors []store.PublisherEntity
	if err := d.s.db.WithContext(ctx).
		Where("uri IS NOT NULL").
		Where("lower(username) LIKE ? OR lower(name) LIKE ? OR lower(nick) LIKE ?", like, like, like).
		Order("last_activity_at DESC NULLS LAST").
		Limit(limit).
		Find(&actors).Error; err != nil {
		return nil, err
	}
	if !includeRemote || len(actors) > 0 {
		return actors, nil
	}

	if strings.HasPrefix(query, "https://") {
		if actor, err := d.getOrCreateActor(ctx, query, "", nil); err == nil {
			return []store.PublisherEntity{*actor}, nil
		}
		return actors, nil
	}
	if handle, domain, ok := splitHandle(query); ok && !strings.EqualFold(domain, d.s.domain) {
		if uri := d.resolveWebFinger(ctx, handle, domain); uri != "" {
			if actor, err := d.getOrCreateActor(ctx, uri, handle, nil); err == nil {
				return []store.PublisherEntity{*actor}, nil
			}
		}
	}
	return actors, nil
}

func (d *discoveryService) resolveWebFinger(ctx context.Context, username, domain string) string {
	target := "https://" + domain + "/.well-known/webfinger?resource=acct:" +
		url.QueryEscape(username) + "%40" + url.QueryEscape(domain)
	document, err := d.fetchDocument(ctx, target, nil)
	if err != nil {
		return ""
	}
	for _, raw := range getObjects(document, "links") {
		link, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if getString(link, "rel") == "self" && strings.Contains(getString(link, "type"), "activity+json") {
			return getString(link, "href")
		}
	}
	return ""
}

func splitHandle(value string) (username, domain string, ok bool) {
	value = strings.TrimPrefix(value, "@")
	parts := strings.Split(value, "@")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// ---- helpers --------------------------------------------------------------

func externalFileReference(raw string) model.CloudFileReferenceObject {
	return model.CloudFileReferenceObject{Url: raw}
}

func boolValue(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(typed, "true")
	default:
		return false
	}
}

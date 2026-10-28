package fediverse

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/lighthouse/internal/platform/httpx"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

// realmBySlug resolves a community realm through Stargate's realm service.
func (s *server) realmBySlug(c *gin.Context, slug string) (*gen.DyRealm, bool) {
	clients := s.core.Clients
	if clients == nil || clients.Realm == nil {
		httpx.Fail(c, errsServiceUnavailable("The realm service is unavailable."))
		return nil, false
	}
	realm, err := clients.Realm.GetRealm(c.Request.Context(), &gen.DyGetRealmRequest{
		Query: &gen.DyGetRealmRequest_Slug{Slug: slug},
	})
	if err != nil || realm == nil {
		c.Status(http.StatusNotFound)
		return nil, false
	}
	if !realm.GetIsCommunity() {
		c.Status(http.StatusNotFound)
		return nil, false
	}
	return realm, true
}

func cloudFileURL(file *gen.DyCloudFile, base string) string {
	if file == nil {
		return ""
	}
	if file.GetUrl() != "" {
		return file.GetUrl()
	}
	return strings.TrimRight(base, "/") + "/" + file.GetId()
}

func (s *server) handleRealmActor(c *gin.Context) {
	realm, ok := s.realmBySlug(c, c.Param("slug"))
	if !ok {
		return
	}
	slug := c.Param("slug")
	actorURL := s.baseURL + "/activitypub/realms/" + slug
	ctx := c.Request.Context()

	var community store.PublisherEntity
	hasCommunity := s.db.WithContext(ctx).
		Where("realm_id = ? AND is_community = ?", parseRealmID(realm.GetId()), true).
		First(&community).Error == nil

	document := map[string]any{
		"@context":          []any{"https://www.w3.org/ns/activitystreams", "https://w3id.org/security/v1"},
		"id":                actorURL,
		"type":              "Group",
		"name":              realm.GetName(),
		"preferredUsername": slug,
		"summary":           realm.GetDescription(),
		"inbox":             actorURL + "/inbox",
		"outbox":            actorURL + "/outbox",
		"followers":         actorURL + "/followers",
		"url":               s.baseURL + "/realms/" + slug,
	}
	if picture := cloudFileURL(realm.GetPicture(), s.fileBase); picture != "" {
		document["icon"] = map[string]any{
			"type":      "Image",
			"mediaType": realm.GetPicture().GetMimeType(),
			"url":       picture,
		}
	}
	if background := cloudFileURL(realm.GetBackground(), s.fileBase); background != "" {
		document["image"] = map[string]any{
			"type":      "Image",
			"mediaType": realm.GetBackground().GetMimeType(),
			"url":       background,
		}
	}
	if hasCommunity && community.PublicKey != nil && *community.PublicKey != "" {
		keyID := actorURL + "#main-key"
		if community.PublicKeyID != nil && *community.PublicKeyID != "" {
			keyID = *community.PublicKeyID
		}
		document["publicKey"] = map[string]any{
			"id":           keyID,
			"owner":        actorURL,
			"publicKeyPem": *community.PublicKey,
		}
	}
	writeActivityPub(c, document)
}

func (s *server) handleRealmFollowers(c *gin.Context) {
	realm, ok := s.realmBySlug(c, c.Param("slug"))
	if !ok {
		return
	}
	limit := 20
	if parsed, err := strconv.Atoi(c.Query("limit")); err == nil && parsed > 0 {
		limit = parsed
	}
	slug := c.Param("slug")
	actorURL := s.baseURL + "/activitypub/realms/" + slug
	ctx := c.Request.Context()

	var subscriptions []store.PublisherSubscriptionEntity
	_ = s.db.WithContext(ctx).
		Where("realm_id = ? AND state = ?", parseRealmID(realm.GetId()), int(model.SubscriptionAccepted)).
		Limit(limit).Find(&subscriptions).Error

	ids := make([]uuid.UUID, 0, len(subscriptions))
	for _, subscription := range subscriptions {
		if subscription.FollowerPublisherID != nil {
			ids = append(ids, *subscription.FollowerPublisherID)
		}
	}
	rows := s.publishersByIDs(ctx, ids)
	items := make([]any, 0, len(rows))
	for i := range rows {
		if rows[i].URI == nil {
			continue
		}
		name := rows[i].Nick
		if name == "" {
			name = deref(rows[i].Username)
		}
		items = append(items, map[string]any{
			"id":   *rows[i].URI,
			"type": "Person",
			"name": name,
		})
	}
	writeActivityPub(c, map[string]any{
		"@context":     "https://www.w3.org/ns/activitystreams",
		"id":           actorURL + "/followers",
		"type":         "OrderedCollection",
		"totalItems":   len(items),
		"orderedItems": items,
	})
}

func (s *server) handleRealmOutbox(c *gin.Context) {
	if _, ok := s.realmBySlug(c, c.Param("slug")); !ok {
		return
	}
	limit := 20
	if parsed, err := strconv.Atoi(c.Query("limit")); err == nil && parsed > 0 {
		limit = parsed
	}
	slug := c.Param("slug")
	actorURL := s.baseURL + "/activitypub/realms/" + slug
	ctx := c.Request.Context()

	var deliveries []store.ActivityPubDeliveryEntity
	_ = s.db.WithContext(ctx).
		Where("actor_uri = ?", actorURL).
		Order("sent_at DESC NULLS LAST").
		Limit(limit).Find(&deliveries).Error

	items := make([]any, 0, len(deliveries))
	for _, delivery := range deliveries {
		if delivery.ActivityPayload == nil || *delivery.ActivityPayload == "" {
			items = append(items, map[string]any{"id": delivery.ActivityID, "type": delivery.ActivityType})
			continue
		}
		var decoded any
		if err := json.Unmarshal([]byte(*delivery.ActivityPayload), &decoded); err != nil {
			items = append(items, map[string]any{"id": delivery.ActivityID, "type": delivery.ActivityType})
			continue
		}
		items = append(items, decoded)
	}
	writeActivityPub(c, map[string]any{
		"@context":     "https://www.w3.org/ns/activitystreams",
		"id":           actorURL + "/outbox",
		"type":         "OrderedCollection",
		"totalItems":   len(items),
		"orderedItems": items,
	})
}

// handleRealmInbox mirrors ReceiveCommunityActivity. The C# action did not
// verify HTTP signatures here; this port keeps that behaviour for parity.
func (s *server) handleRealmInbox(c *gin.Context) {
	realm, ok := s.realmBySlug(c, c.Param("slug"))
	if !ok {
		return
	}
	if !validActivityPubContentType(c.GetHeader("Content-Type")) {
		httpx.Fail(c, errsNotAcceptable("Content-Type '"+c.GetHeader("Content-Type")+"' not acceptable. Accepted types: application/activity+json, application/ld+json"))
		return
	}
	body, err := readBody(c.Request)
	if err != nil {
		httpx.BadRequest(c, "ACTIVITYPUB_INVALID_JSON", "Invalid JSON")
		return
	}
	var activity map[string]any
	if err := json.Unmarshal(body, &activity); err != nil {
		httpx.BadRequest(c, "ACTIVITYPUB_INVALID_JSON", "Invalid JSON")
		return
	}
	if getString(activity, "type") == "" {
		httpx.BadRequest(c, "ACTIVITYPUB_NO_ACTIVITY_TYPE", "No activity type")
		return
	}
	senderURI := getString(activity, "actor")
	if senderURI == "" {
		httpx.BadRequest(c, "ACTIVITYPUB_NO_ACTOR", "No actor")
		return
	}
	slug := c.Param("slug")
	communityURL := s.baseURL + "/activitypub/realms/" + slug
	realmID := parseRealmID(realm.GetId())
	ctx := c.Request.Context()

	switch getString(activity, "type") {
	case "Follow":
		actor, err := s.resolveOrCreateActor(ctx, senderURI)
		if err != nil {
			httpx.BadRequest(c, "ACTIVITYPUB_NO_ACTOR", "No actor")
			return
		}
		var existing store.PublisherSubscriptionEntity
		if err := s.db.WithContext(ctx).
			Where("publisher_id = ? AND realm_id = ? AND state = ?", actor.ID, realmID, int(model.SubscriptionAccepted)).
			First(&existing).Error; err == nil {
			httpx.OK(c, gin.H{"status": "already_following"})
			return
		}
		now := time.Now().UTC()
		subscription := store.PublisherSubscriptionEntity{
			ID:                  uuid.New(),
			FollowerPublisherID: &actor.ID,
			PublisherID:         actor.ID,
			RealmID:             &realmID,
			State:               int(model.SubscriptionAccepted),
			FollowedAt:          &now,
		}
		if err := s.db.WithContext(ctx).Create(&subscription).Error; err != nil {
			httpx.Internal(c)
			return
		}
		if actor.InboxURI != nil {
			s.delivery.sendAccept(ctx, nil, communityURL, *actor.InboxURI, senderURI)
		}
		httpx.OK(c, gin.H{"status": "accepted"})
	case "Undo":
		object := getObject(activity, "object")
		if object == nil {
			httpx.BadRequest(c, "ACTIVITYPUB_NO_OBJECT_IN_UNDO", "No object in undo")
			return
		}
		if getString(object, "type") != "Follow" || getString(object, "object") != communityURL {
			httpx.OK(c, gin.H{"status": "ignored"})
			return
		}
		actor, err := s.resolveOrCreateActor(ctx, senderURI)
		if err != nil {
			httpx.BadRequest(c, "ACTIVITYPUB_NO_ACTOR", "No actor")
			return
		}
		_ = s.db.WithContext(ctx).
			Where("follower_publisher_id = ? AND publisher_id = ? AND realm_id = ?", actor.ID, actor.ID, realmID).
			Delete(&store.PublisherSubscriptionEntity{}).Error
		httpx.OK(c, gin.H{"status": "undone"})
	case "Create", "Note":
		announceID := communityURL + "/announce/" + uuid.NewString()
		announce := map[string]any{
			"@context": "https://www.w3.org/ns/activitystreams",
			"id":       announceID,
			"type":     "Announce",
			"actor":    communityURL,
			"object":   activity,
			"to":       []any{publicCollection},
			"cc":       []any{communityURL + "/followers"},
		}
		var subscriptions []store.PublisherSubscriptionEntity
		_ = s.db.WithContext(ctx).
			Where("realm_id = ? AND state = ?", realmID, int(model.SubscriptionAccepted)).
			Find(&subscriptions).Error
		delivered := 0
		for _, subscription := range subscriptions {
			if subscription.FollowerPublisherID == nil {
				continue
			}
			var follower store.PublisherEntity
			if err := s.db.WithContext(ctx).Where("id = ?", *subscription.FollowerPublisherID).First(&follower).Error; err != nil ||
				follower.InboxURI == nil {
				continue
			}
			if _, err := s.delivery.enqueue(ctx, "Announce", announce, communityURL, *follower.InboxURI, announceID, nil); err == nil {
				delivered++
			}
		}
		httpx.OK(c, gin.H{"status": "announced", "delivered": delivered})
	default:
		s.log.Warn("unsupported realm activity", "type", getString(activity, "type"), "slug", slug)
		httpx.OK(c, gin.H{"status": "ignored"})
	}
}

// parseRealmID tolerates a non-UUID realm id instead of panicking.
func parseRealmID(value string) uuid.UUID {
	id, err := uuid.Parse(value)
	if err != nil {
		return uuid.Nil
	}
	return id
}

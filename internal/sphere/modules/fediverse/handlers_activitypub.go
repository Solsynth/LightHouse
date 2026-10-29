package fediverse

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"src.solsynth.dev/sosys/go/pkg/errs"

	"src.solsynth.dev/sosys/lighthouse/internal/platform/httpx"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

// ---- content negotiation --------------------------------------------------

func isWebRequest(c *gin.Context) bool {
	accept := strings.ToLower(c.GetHeader("Accept"))
	userAgent := strings.ToLower(c.GetHeader("User-Agent"))
	if strings.Contains(accept, "application/activity+json") || strings.Contains(accept, "application/ld+json") {
		return false
	}
	return strings.Contains(userAgent, "mozilla") || strings.Contains(accept, "text/html")
}

func writeActivityPub(c *gin.Context, body any) {
	c.Header("Content-Type", "application/activity+json; charset=utf-8")
	c.JSON(http.StatusOK, body)
}

func validActivityPubContentType(contentType string) bool {
	normalized := strings.ToLower(strings.TrimSpace(contentType))
	switch {
	case normalized == "application/activity+json", normalized == "application/ld+json":
		return true
	case strings.HasPrefix(normalized, "application/activity+json;"):
		return true
	case strings.HasPrefix(normalized, "application/ld+json;") && strings.Contains(normalized, "profile"):
		return true
	}
	return false
}

// ---- actor routes ---------------------------------------------------------

func (s *server) handleActor(c *gin.Context) {
	username := c.Param("username")
	if isWebRequest(c) {
		c.Redirect(http.StatusFound, s.baseURL+"/publishers/"+urlPathEscape(username))
		return
	}
	publisher, err := s.loadPublisher(c.Request.Context(), username)
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	document, err := s.actorDocument(c.Request.Context(), publisher)
	if err != nil {
		s.log.Error("failed to render actor document", "publisher", publisher.ID, "error", err)
		httpx.Internal(c)
		return
	}
	writeActivityPub(c, document)
}

func (s *server) handleInbox(c *gin.Context) {
	if !validActivityPubContentType(c.GetHeader("Content-Type")) {
		httpx.Fail(c, errs.New("ACTIVITYPUB_INVALID_CONTENT_TYPE",
			"Content-Type '"+c.GetHeader("Content-Type")+"' not acceptable. Accepted types: application/activity+json, application/ld+json",
			http.StatusNotAcceptable))
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
		httpx.BadRequest(c, "ACTIVITYPUB_MISSING_ACTIVITY_TYPE", "Missing activity type")
		return
	}
	result := s.processInbox(c.Request.Context(), c.Request, body, activity, nil)
	s.writeInboxResult(c, result)
}

func (s *server) writeInboxResult(c *gin.Context, result inboxResult) {
	switch result {
	case inboxSuccess:
		c.Status(http.StatusAccepted)
	case inboxRejected:
		c.Status(http.StatusUnauthorized)
	case inboxNotFound:
		c.Status(http.StatusNotFound)
	case inboxBadRequest:
		c.Status(http.StatusBadRequest)
	case inboxNotSupported:
		httpx.Fail(c, errs.New("ACTIVITYPUB_UNSUPPORTED_ACTIVITY_TYPE",
			"Unsupported activity type", http.StatusUnprocessableEntity))
	}
}

func (s *server) handleObject(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	if isWebRequest(c) {
		c.Redirect(http.StatusFound, s.baseURL+"/posts/"+id.String())
		return
	}
	var post store.PostEntity
	err = s.db.WithContext(c.Request.Context()).
		Where("id = ? AND publisher_id <> ? AND fediverse_uri IS NULL AND drafted_at IS NULL AND visibility = ?",
			id, uuid.Nil, int(model.VisibilityPublic)).
		First(&post).Error
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	var publisher store.PublisherEntity
	if err := s.db.WithContext(c.Request.Context()).Where("id = ?", post.PublisherID).First(&publisher).Error; err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	document, err := s.postObject(c.Request.Context(), &post, s.actorURL(publisher.Name))
	if err != nil {
		httpx.Internal(c)
		return
	}
	if _, ok := document["@context"]; !ok {
		document["@context"] = "https://www.w3.org/ns/activitystreams"
	}
	document["url"] = s.baseURL + "/posts/" + post.ID.String()
	writeActivityPub(c, document)
}

func (s *server) handleOutbox(c *gin.Context) {
	username := c.Param("username")
	if isWebRequest(c) {
		c.Redirect(http.StatusFound, s.baseURL+"/publishers/"+urlPathEscape(username))
		return
	}
	page, ok := parseCollectionPage(c.Query("page"))
	if !ok {
		httpx.BadRequest(c, "ACTIVITYPUB_INVALID_PAGE", "Page must be a positive integer or 'true'.")
		return
	}
	publisher, err := s.loadPublisher(c.Request.Context(), username)
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	actorURL := s.actorURL(username)
	outboxURL := actorURL + "/outbox"
	ctx := c.Request.Context()

	var posts []store.PostEntity
	if err := s.db.WithContext(ctx).
		Where("publisher_id = ? AND drafted_at IS NULL AND visibility = ?", publisher.ID, int(model.VisibilityPublic)).
		Order("published_at DESC NULLS LAST, created_at DESC").
		Find(&posts).Error; err != nil {
		httpx.Internal(c)
		return
	}
	var boosts []store.BoostEntity
	_ = s.db.WithContext(ctx).Where("publisher_id = ?", publisher.ID).Find(&boosts).Error

	items := make([]outboxEntry, 0, len(posts)+len(boosts))
	for i := range posts {
		published := timeValue(posts[i].PublishedAt)
		if published.IsZero() {
			published = posts[i].CreatedAt
		}
		items = append(items, outboxEntry{
			id:        s.baseURL + "/activitypub/objects/" + posts[i].ID.String() + "/activity",
			kind:      "Create",
			published: published.Unix(),
			post:      &posts[i],
		})
	}
	for i := range boosts {
		var post store.PostEntity
		if err := s.db.WithContext(ctx).Where("id = ? AND drafted_at IS NULL AND visibility = ?",
			boosts[i].PostID, int(model.VisibilityPublic)).First(&post).Error; err != nil {
			continue
		}
		itemID := s.baseURL + "/activitypub/objects/" + boosts[i].ID.String() + "/activity"
		if boosts[i].ActivityPubURI != nil && *boosts[i].ActivityPubURI != "" {
			itemID = *boosts[i].ActivityPubURI
		}
		items = append(items, outboxEntry{
			id:        itemID,
			kind:      "Announce",
			published: timeValue(boosts[i].BoostedAt).Unix(),
			post:      &post,
			boost:     &boosts[i],
		})
	}

	total := len(items)
	if page == nil {
		writeActivityPub(c, activityCollection(outboxURL, "OrderedCollection", total,
			outboxURL+"?page=1", nil))
		return
	}
	const pageSize = 20
	skip := (*page - 1) * pageSize
	if skip < 0 {
		skip = 0
	}
	sortOutbox(items)
	ordered := make([]any, 0, pageSize)
	for index := skip; index < len(items) && index < skip+pageSize; index++ {
		item := items[index]
		object, err := s.postObject(ctx, item.post, actorURL)
		if err != nil {
			ordered = append(ordered, map[string]any{})
			continue
		}
		object["url"] = s.baseURL + "/posts/" + item.post.ID.String()
		if item.boost != nil && item.boost.BoostedAt != nil {
			object["_boostedAt"] = activityPubTime(*item.boost.BoostedAt)
			object["_boostId"] = item.boost.ID.String()
		}
		ordered = append(ordered, map[string]any{
			"id":        item.id,
			"type":      item.kind,
			"actor":     actorURL,
			"published": activityPubTime(timeValue(item.post.PublishedAt)),
			"to":        []any{publicCollection},
			"cc":        []any{actorURL + "/followers"},
			"object":    object,
		})
	}
	next := ""
	if skip+pageSize < total {
		next = outboxURL + "?page=" + strconv.Itoa(*page+1)
	}
	prev := ""
	if *page > 1 {
		prev = outboxURL + "?page=" + strconv.Itoa(*page-1)
	}
	writeActivityPub(c, activityCollectionPage(outboxURL+"?page="+strconv.Itoa(*page), outboxURL, total, ordered, next, prev))
}

func (s *server) handleFollowers(c *gin.Context) {
	s.collectionHandler(c, true)
}

func (s *server) handleFollowing(c *gin.Context) {
	s.collectionHandler(c, false)
}

func (s *server) collectionHandler(c *gin.Context, followers bool) {
	username := c.Param("username")
	if isWebRequest(c) {
		c.Redirect(http.StatusFound, s.baseURL+"/publishers/"+urlPathEscape(username))
		return
	}
	publisher, err := s.loadPublisher(c.Request.Context(), username)
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	page := parsePageNumber(c.Query("page"))
	actorURL := s.actorURL(username)
	collectionURL := actorURL + "/followers"
	if !followers {
		collectionURL = actorURL + "/following"
	}
	ctx := c.Request.Context()

	query := s.db.WithContext(ctx).Model(&store.PublisherSubscriptionEntity{}).
		Where("state = ? AND ended_at IS NULL AND is_blocking = ?", int(model.SubscriptionAccepted), false)
	if followers {
		query = query.Where("publisher_id = ?", publisher.ID)
	} else {
		query = query.Where("follower_publisher_id = ?", publisher.ID)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		httpx.Internal(c)
		return
	}
	if page == nil {
		writeActivityPub(c, activityCollection(collectionURL, "OrderedCollection", int(total),
			collectionURL+"?page=1", nil))
		return
	}
	const pageSize = 40
	skip := (*page - 1) * pageSize
	var subscriptions []store.PublisherSubscriptionEntity
	if err := query.Order("followed_at DESC NULLS LAST, created_at DESC").
		Limit(pageSize).Offset(skip).Find(&subscriptions).Error; err != nil {
		httpx.Internal(c)
		return
	}
	actorIDs := make([]uuid.UUID, 0, len(subscriptions))
	for _, subscription := range subscriptions {
		if followers {
			if subscription.FollowerPublisherID != nil {
				actorIDs = append(actorIDs, *subscription.FollowerPublisherID)
			}
		} else {
			actorIDs = append(actorIDs, subscription.PublisherID)
		}
	}
	items := make([]any, 0, len(actorIDs))
	if len(actorIDs) > 0 {
		var actors []store.PublisherEntity
		if err := s.db.WithContext(ctx).Where("id IN ?", actorIDs).Find(&actors).Error; err == nil {
			byID := make(map[uuid.UUID]store.PublisherEntity, len(actors))
			for _, actor := range actors {
				byID[actor.ID] = actor
			}
			for _, id := range actorIDs {
				if actor, ok := byID[id]; ok && actor.URI != nil {
					items = append(items, *actor.URI)
				}
			}
		}
	}
	writeActivityPub(c, activityCollectionPage(collectionURL+"?page="+strconv.Itoa(*page), collectionURL, int(total), items, "", ""))
}

func (s *server) handleFeatured(c *gin.Context) {
	username := c.Param("username")
	if isWebRequest(c) {
		c.Redirect(http.StatusFound, s.baseURL+"/publishers/"+urlPathEscape(username))
		return
	}
	publisher, err := s.loadPublisher(c.Request.Context(), username)
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	page := parsePageNumber(c.Query("page"))
	featuredURL := s.actorURL(username) + "/featured"
	ctx := c.Request.Context()

	var posts []store.PostEntity
	if err := s.db.WithContext(ctx).
		Where("publisher_id = ? AND pin_mode = ? AND drafted_at IS NULL AND visibility = ?",
			publisher.ID, int(model.PinModePublisherPage), int(model.VisibilityPublic)).
		Order("published_at DESC NULLS LAST, created_at DESC").
		Find(&posts).Error; err != nil {
		httpx.Internal(c)
		return
	}
	urls := make([]any, 0, len(posts))
	for i := range posts {
		urls = append(urls, s.objectURL(posts[i].ID))
	}
	if page == nil {
		writeActivityPub(c, activityCollection(featuredURL, "OrderedCollection", len(urls), "", urls))
		return
	}
	const pageSize = 20
	skip := (*page - 1) * pageSize
	end := skip + pageSize
	if skip > len(urls) {
		skip = len(urls)
	}
	if end > len(urls) {
		end = len(urls)
	}
	next := ""
	if end < len(urls) {
		next = featuredURL + "?page=" + strconv.Itoa(*page+1)
	}
	prev := ""
	if *page > 1 {
		prev = featuredURL + "?page=" + strconv.Itoa(*page-1)
	}
	writeActivityPub(c, activityCollectionPage(featuredURL+"?page="+strconv.Itoa(*page), featuredURL, len(urls), urls[skip:end], next, prev))
}

func (s *server) handleMainKey(c *gin.Context) {
	publisher, err := s.loadPublisher(c.Request.Context(), c.Param("username"))
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	key, err := s.keys.getKeyForActor(c.Request.Context(), publisher.ID)
	if err != nil || key.KeyPEM == "" {
		httpx.Fail(c, errs.New("FEDIVERSE_KEY_NOT_FOUND", "No key found for this actor", http.StatusNotFound))
		return
	}
	actorURL := s.actorURL(publisher.Name)
	writeActivityPub(c, map[string]any{
		"@context": []any{
			"https://w3id.org/security/v1",
			"https://www.w3.org/ns/activitystreams",
		},
		"id":           actorURL + "#main-key",
		"owner":        actorURL,
		"publicKeyPem": key.KeyPEM,
		"type":         "RsaSignature2017",
	})
}

// ---- server actor ---------------------------------------------------------

func (s *server) handleServerActor(c *gin.Context) {
	key, err := s.keys.getServerKey()
	if err != nil {
		httpx.Fail(c, errs.New("ACTOR_KEY_NOT_INITIALIZED", "Server key not initialized.", http.StatusNotFound))
		return
	}
	actorURL := s.baseURL + "/activitypub/actor"
	writeActivityPub(c, map[string]any{
		"@context":          []any{"https://www.w3.org/ns/activitystreams", "https://w3id.org/security/v1"},
		"id":                actorURL,
		"type":              "Application",
		"name":              "server",
		"preferredUsername": "solar-network",
		"summary":           "The server node for " + s.domain,
		"url":               s.baseURL,
		"inbox":             actorURL + "/inbox",
		"outbox":            actorURL + "/outbox",
		"followers":         actorURL + "/followers",
		"publicKey": map[string]any{
			"id":           key.KeyID,
			"owner":        actorURL,
			"publicKeyPem": key.PublicKeyPEM,
		},
		"alsoKnownAs": []any{s.baseURL + "/"},
	})
}

func (s *server) handleServerOutbox(c *gin.Context) {
	actorURL := s.baseURL + "/activitypub/actor"
	writeActivityPub(c, map[string]any{
		"id":    actorURL + "/outbox",
		"type":  "OrderedCollection",
		"first": actorURL + "/outbox?page=true",
	})
}

func (s *server) handleServerFollowers(c *gin.Context) {
	actorURL := s.baseURL + "/activitypub/actor"
	writeActivityPub(c, map[string]any{
		"id":    actorURL + "/followers",
		"type":  "OrderedCollection",
		"first": actorURL + "/followers?page=true",
	})
}

func (s *server) handleServerFollowing(c *gin.Context) {
	actorURL := s.baseURL + "/activitypub/actor"
	writeActivityPub(c, map[string]any{
		"id":    actorURL + "/following",
		"type":  "OrderedCollection",
		"first": actorURL + "/following?page=true",
	})
}

func (s *server) handleServerMainKey(c *gin.Context) {
	key, err := s.keys.getServerKey()
	if err != nil {
		httpx.Fail(c, errs.New("ACTOR_KEY_NOT_INITIALIZED", "Server key not initialized.", http.StatusNotFound))
		return
	}
	writeActivityPub(c, map[string]any{
		"id":           key.KeyID,
		"owner":        s.baseURL + "/activitypub/actor",
		"publicKeyPem": key.PublicKeyPEM,
	})
}

// ---- helpers --------------------------------------------------------------

// parseCollectionPage mirrors TryParseCollectionPage.
func parseCollectionPage(raw string) (*int, bool) {
	if raw == "" {
		return nil, true
	}
	if strings.EqualFold(raw, "true") {
		page := 1
		return &page, true
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return nil, false
	}
	return &value, true
}

// parsePageNumber treats an unparsable ?page as absent (the C# int? binder
// rejects the request; the collection is the safer answer here).
func parsePageNumber(raw string) *int {
	if raw == "" {
		return nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return nil
	}
	return &value
}

func urlPathEscape(value string) string {
	return strings.ReplaceAll(value, " ", "%20")
}

func sortOutbox(items []outboxEntry) {
	// Insertion sort keeps the helper dependency-free; outboxes are small.
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && items[j].published > items[j-1].published; j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
}

type outboxEntry struct {
	id        string
	kind      string
	published int64
	post      *store.PostEntity
	boost     *store.BoostEntity
}

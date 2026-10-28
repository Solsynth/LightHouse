package fediverse

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

type inboxResult int

const (
	inboxSuccess inboxResult = iota
	inboxRejected
	inboxNotFound
	inboxBadRequest
	inboxNotSupported
)

// processInbox applies one inbound activity. localActor is used for realm
// inboxes where the target is a community actor rather than a publisher row.
func (s *server) processInbox(ctx context.Context, r *http.Request, body []byte, activity map[string]any, localActor *store.PublisherEntity) inboxResult {
	activityType := getString(activity, "type")
	activityID := getString(activity, "id")
	if activityType == "" {
		return inboxBadRequest
	}

	valid, actorURI, err := s.verifyIncoming(r, body)
	if err != nil || !valid || actorURI == "" {
		s.log.Warn("inbox signature rejected", "type", activityType, "error", err)
		return inboxRejected
	}

	objectContent := ""
	if object := getObject(activity, "object"); object != nil {
		objectContent = getString(object, "content")
	}
	moderation := s.moderation.checkActor(ctx, actorURI, objectContent, hostOfURL(actorURI))
	if moderation.IsBlocked {
		s.log.Warn("inbox blocked by moderation rule", "actor", actorURI, "rule", deref(moderation.MatchedRuleName))
		return inboxRejected
	}
	if rule := s.moderation.checkActorRule(ctx, actorURI); rule.IsSuspended {
		s.log.Warn("inbox rejected for suspended actor", "actor", actorURI, "rule", deref(rule.MatchedRuleName))
		return inboxRejected
	}

	// Idempotency: the same activity delivered twice is a success but is only
	// applied once.
	idempotencyKey := ""
	if activityID != "" && s.core.Redis != nil {
		idempotencyKey = "activitypub:inbox:" + activityID
		var seen bool
		if found, getErr := s.core.Redis.Get(ctx, idempotencyKey, &seen); getErr == nil && found && seen {
			return inboxSuccess
		}
	}

	var result inboxResult
	switch activityType {
	case "Follow":
		result = s.handleFollow(ctx, actorURI, activity, localActor)
	case "Accept":
		result = s.handleAccept(ctx, actorURI, activity)
	case "Reject":
		result = s.handleReject(ctx, actorURI, activity)
	case "QuoteRequest":
		result = s.handleQuoteRequest(ctx, actorURI, activity)
	case "Undo":
		result = s.handleUndo(ctx, actorURI, activity)
	case "Create":
		result = s.handleCreate(ctx, actorURI, activity)
	case "Like", "EmojiReact":
		result = s.handleLike(ctx, actorURI, activity)
	case "Announce":
		result = s.handleAnnounce(ctx, actorURI, activity)
	case "Delete":
		result = s.handleDelete(ctx, actorURI, activity)
	case "Update":
		result = s.handleUpdate(ctx, actorURI, activity)
	case "Add":
		result = s.handlePin(ctx, actorURI, activity, true)
	case "Remove":
		result = s.handlePin(ctx, actorURI, activity, false)
	case "Block":
		result = s.handleBlock(ctx, actorURI, activity)
	case "Move":
		s.log.Info("Move activity received", "actor", actorURI, "object", getString(activity, "object"), "target", getString(activity, "target"))
		result = inboxSuccess
	case "Flag":
		s.log.Info("Flag activity received", "actor", actorURI, "content", getString(activity, "content"))
		result = inboxSuccess
	default:
		s.log.Warn("unsupported activity type", "type", activityType)
		result = inboxNotSupported
	}

	if idempotencyKey != "" && (result == inboxSuccess || result == inboxNotFound) {
		seen := true
		_ = s.core.Redis.Set(ctx, idempotencyKey, seen, 24*time.Hour)
	}
	s.log.Info("inbox activity processed", "type", activityType, "actor", actorURI, "result", result)
	return result
}

func (s *server) handleFollow(ctx context.Context, actorURI string, activity map[string]any, localActor *store.PublisherEntity) inboxResult {
	objectURI := getString(activity, "object")
	if objectURI == "" {
		return inboxBadRequest
	}
	actor, err := s.resolveOrCreateActor(ctx, actorURI)
	if err != nil {
		return inboxBadRequest
	}
	target, err := s.resolveTargetActor(ctx, objectURI, localActor)
	if err != nil {
		return inboxNotFound
	}

	var existing store.PublisherSubscriptionEntity
	err = s.db.WithContext(ctx).
		Where("follower_publisher_id = ? AND publisher_id = ?", actor.ID, target.ID).
		First(&existing).Error

	needsApproval := target.IsLocked || (target.ModerateSubscription != nil && *target.ModerateSubscription)
	if needsApproval {
		if err == nil && existing.State == int(model.PublisherSubscriptionStateAccepted) {
			return inboxSuccess
		}
		now := time.Now().UTC()
		if errors.Is(err, gorm.ErrRecordNotFound) {
			subscription := store.PublisherSubscriptionEntity{
				ID:                  uuid.New(),
				FollowerPublisherID: &actor.ID,
				PublisherID:         target.ID,
				State:               int(model.PublisherSubscriptionStatePending),
			}
			if createErr := s.db.WithContext(ctx).Create(&subscription).Error; createErr != nil {
				return inboxBadRequest
			}
		} else {
			existing.State = int(model.PublisherSubscriptionStatePending)
			existing.EndedAt = nil
			existing.RejectReason = nil
			existing.FollowedAt = &now
			_ = s.db.WithContext(ctx).Save(&existing).Error
		}
		return inboxSuccess
	}

	now := time.Now().UTC()
	switch {
	case err == nil && existing.State == int(model.PublisherSubscriptionStateAccepted):
		return inboxSuccess
	case errors.Is(err, gorm.ErrRecordNotFound):
		subscription := store.PublisherSubscriptionEntity{
			ID:                  uuid.New(),
			FollowerPublisherID: &actor.ID,
			PublisherID:         target.ID,
			State:               int(model.PublisherSubscriptionStateAccepted),
			FollowedAt:          &now,
		}
		if createErr := s.db.WithContext(ctx).Create(&subscription).Error; createErr != nil {
			return inboxBadRequest
		}
	default:
		existing.State = int(model.PublisherSubscriptionStateAccepted)
		existing.EndedAt = nil
		existing.FollowedAt = &now
		if saveErr := s.db.WithContext(ctx).Save(&existing).Error; saveErr != nil {
			return inboxBadRequest
		}
	}

	if actor.InboxURI != nil && target.URI != nil {
		s.delivery.sendAccept(ctx, target, *target.URI, *actor.InboxURI, actorURI)
	}
	return inboxSuccess
}

func (s *server) handleAccept(ctx context.Context, actorURI string, activity map[string]any) inboxResult {
	object := getObject(activity, "object")
	if object == nil {
		return inboxBadRequest
	}
	if getString(object, "type") != "Follow" {
		return inboxSuccess
	}
	followObjectURI := getString(object, "object")
	if followObjectURI == "" {
		return inboxBadRequest
	}
	actor, err := s.resolveOrCreateActor(ctx, actorURI)
	if err != nil {
		return inboxBadRequest
	}
	var local store.PublisherEntity
	if err := s.db.WithContext(ctx).Where("uri = ?", followObjectURI).First(&local).Error; err != nil {
		return inboxNotFound
	}
	now := time.Now().UTC()
	var relationship store.PublisherSubscriptionEntity
	err = s.db.WithContext(ctx).
		Where("follower_publisher_id = ? AND publisher_id = ?", local.ID, actor.ID).
		First(&relationship).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		relationship = store.PublisherSubscriptionEntity{
			ID:                  uuid.New(),
			AccountID:           local.AccountID,
			FollowerPublisherID: &local.ID,
			PublisherID:         actor.ID,
			State:               int(model.PublisherSubscriptionStateAccepted),
			FollowedAt:          &now,
		}
		if createErr := s.db.WithContext(ctx).Create(&relationship).Error; createErr != nil {
			return inboxBadRequest
		}
	} else if err == nil {
		relationship.State = int(model.PublisherSubscriptionStateAccepted)
		relationship.EndedAt = nil
		relationship.FollowedAt = &now
		if saveErr := s.db.WithContext(ctx).Save(&relationship).Error; saveErr != nil {
			return inboxBadRequest
		}
	} else {
		return inboxBadRequest
	}
	return inboxSuccess
}

func (s *server) handleReject(ctx context.Context, actorURI string, _ map[string]any) inboxResult {
	actor, err := s.resolveOrCreateActor(ctx, actorURI)
	if err != nil {
		return inboxBadRequest
	}
	if err := s.db.WithContext(ctx).Model(&store.PublisherSubscriptionEntity{}).
		Where("publisher_id = ? AND state = ?", actor.ID, int(model.PublisherSubscriptionStatePending)).
		Updates(map[string]any{
			"state":         int(model.PublisherSubscriptionStateRejected),
			"reject_reason": "Remote rejected follow",
		}).Error; err != nil {
		return inboxBadRequest
	}
	return inboxSuccess
}

func (s *server) handleQuoteRequest(ctx context.Context, actorURI string, activity map[string]any) inboxResult {
	objectURI := getString(activity, "object")
	if objectURI == "" {
		return inboxBadRequest
	}
	instrument := getString(activity, "instrument")
	if _, err := s.resolveOrCreateActor(ctx, actorURI); err != nil {
		return inboxBadRequest
	}
	var target store.PostEntity
	if err := s.db.WithContext(ctx).Where("fediverse_uri = ?", objectURI).First(&target).Error; err != nil {
		return inboxNotFound
	}
	var author store.PublisherEntity
	if err := s.db.WithContext(ctx).Where("id = ?", target.PublisherID).First(&author).Error; err != nil {
		return inboxNotFound
	}
	if instrument == "" {
		instrument = getString(activity, "id")
	}
	authorization := &store.QuoteAuthorizationEntity{
		ID:                   uuid.New(),
		FediverseURI:         stringPtr(s.baseURL + "/quote-authorizations/" + uuid.NewString()),
		PublisherID:          author.ID,
		InteractingObjectURI: instrument,
		InteractionTargetURI: objectURI,
		TargetPostID:         &target.ID,
		IsValid:              true,
	}
	if err := s.db.WithContext(ctx).Create(authorization).Error; err != nil {
		return inboxBadRequest
	}
	return inboxSuccess
}

func (s *server) handleUndo(ctx context.Context, actorURI string, activity map[string]any) inboxResult {
	object := getObject(activity, "object")
	if object == nil {
		return inboxBadRequest
	}
	objectType := getString(object, "type")
	objectURI := getString(object, "object")
	actor, err := s.resolveOrCreateActor(ctx, actorURI)
	if err != nil {
		return inboxBadRequest
	}
	switch objectType {
	case "Follow":
		if objectURI == "" {
			return inboxBadRequest
		}
		target, err := s.resolveOrCreateActor(ctx, objectURI)
		if err != nil {
			return inboxSuccess
		}
		_ = s.db.WithContext(ctx).
			Where("follower_publisher_id = ? AND publisher_id = ?", actor.ID, target.ID).
			Delete(&store.PublisherSubscriptionEntity{}).Error
		return inboxSuccess
	case "Like":
		post, err := s.resolvePost(ctx, objectURI)
		if err != nil {
			return inboxNotFound
		}
		var reaction store.PostReactionEntity
		if err := s.db.WithContext(ctx).
			Where("publisher_id = ? AND post_id = ?", actor.ID, post.ID).
			First(&reaction).Error; err == nil {
			switch reaction.Attitude {
			case int(model.ReactionPositive):
				post.Upvotes--
			case int(model.ReactionNegative):
				post.Downvotes--
			}
			_ = s.db.WithContext(ctx).Delete(&reaction).Error
			_ = s.db.WithContext(ctx).Save(post).Error
		}
		return inboxSuccess
	case "Announce":
		post, err := s.resolvePost(ctx, objectURI)
		if err != nil {
			return inboxNotFound
		}
		var boost store.BoostEntity
		if err := s.db.WithContext(ctx).
			Where("post_id = ? AND publisher_id = ?", post.ID, actor.ID).
			First(&boost).Error; err == nil {
			_ = s.db.WithContext(ctx).Delete(&boost).Error
			if post.BoostCount > 0 {
				post.BoostCount--
			}
			_ = s.db.WithContext(ctx).Save(post).Error
		}
		return inboxSuccess
	default:
		return inboxNotSupported
	}
}

func (s *server) handleCreate(ctx context.Context, actorURI string, activity map[string]any) inboxResult {
	object := getObject(activity, "object")
	if object == nil {
		return inboxBadRequest
	}
	objectType := getString(object, "type")
	if objectType != "Note" && objectType != "Article" {
		return inboxSuccess
	}
	objectID := getString(object, "id")
	if objectID == "" {
		return inboxBadRequest
	}
	var existing store.PostEntity
	if err := s.db.WithContext(ctx).Where("fediverse_uri = ?", objectID).First(&existing).Error; err == nil {
		return inboxSuccess
	}
	actor, err := s.resolveOrCreateActor(ctx, actorURI)
	if err != nil {
		return inboxBadRequest
	}
	if _, err := s.createFederatedPost(ctx, object, actor); err != nil {
		s.log.Warn("failed to store federated post", "uri", objectID, "error", err)
		return inboxBadRequest
	}
	return inboxSuccess
}

func (s *server) createFederatedPost(ctx context.Context, object map[string]any, actor *store.PublisherEntity) (*store.PostEntity, error) {
	objectID := getString(object, "id")
	objectType := getString(object, "type")
	post := &store.PostEntity{
		ID:           uuid.New(),
		FediverseURI: &objectID,
		PublisherID:  actor.ID,
		Visibility:   int(model.VisibilityPublic),
		ContentType:  int(model.ContentTypeHTML),
	}
	if objectType == "Article" {
		post.FediverseType = new(2)
		post.Type = int(model.PostTypeArticle)
	} else {
		post.FediverseType = new(1)
		post.Type = int(model.PostTypeMoment)
	}
	if name := getString(object, "name"); name != "" {
		post.Title = &name
	}
	if content := getString(object, "content"); content != "" {
		post.Content = &content
	}
	if language := getString(object, "language"); language != "" {
		post.Language = &language
	}
	if published := parseInstant(object["published"]); published != nil {
		post.PublishedAt = published
	}
	if updated := parseInstant(object["updated"]); updated != nil {
		post.EditedAt = updated
	}
	if attachments := convertAttachments(object["attachment"]); len(attachments) > 0 {
		post.Attachments = store.ToJSON(attachments)
	}
	if inReplyTo := getString(object, "inReplyTo"); inReplyTo != "" {
		var replied store.PostEntity
		if err := s.db.WithContext(ctx).Where("fediverse_uri = ?", inReplyTo).First(&replied).Error; err == nil {
			post.RepliedPostID = &replied.ID
		}
	}
	if err := s.db.WithContext(ctx).Create(post).Error; err != nil {
		return nil, err
	}
	return post, nil
}

func (s *server) handleLike(ctx context.Context, actorURI string, activity map[string]any) inboxResult {
	objectURI := getString(activity, "object")
	if objectURI == "" {
		return inboxBadRequest
	}
	actor, err := s.resolveOrCreateActor(ctx, actorURI)
	if err != nil {
		return inboxBadRequest
	}
	post, err := s.resolvePost(ctx, objectURI)
	if err != nil {
		return inboxNotFound
	}
	var existing store.PostReactionEntity
	if err := s.db.WithContext(ctx).
		Where("publisher_id = ? AND post_id = ?", actor.ID, post.ID).
		First(&existing).Error; err == nil {
		return inboxSuccess
	}
	symbol, attitude := reactionFromEmoji(getString(activity, "content"))
	now := time.Now().UTC()
	reactionID := getString(activity, "id")
	if reactionID == "" {
		reactionID = uuid.NewString()
	}
	reaction := &store.PostReactionEntity{
		ID:           uuid.New(),
		EntityBase:   store.EntityBase{CreatedAt: now, UpdatedAt: now},
		Symbol:       symbol,
		Attitude:     attitude,
		PostID:       post.ID,
		FediverseURI: &reactionID,
		PublisherID:  &actor.ID,
		IsLocal:      false,
	}
	if err := s.db.WithContext(ctx).Create(reaction).Error; err != nil {
		return inboxBadRequest
	}
	switch attitude {
	case int(model.ReactionPositive):
		post.Upvotes++
	case int(model.ReactionNegative):
		post.Downvotes++
	}
	_ = s.db.WithContext(ctx).Save(post).Error
	return inboxSuccess
}

func (s *server) handleAnnounce(ctx context.Context, actorURI string, activity map[string]any) inboxResult {
	objectURI := getString(activity, "object")
	if objectURI == "" {
		return inboxBadRequest
	}
	actor, err := s.resolveOrCreateActor(ctx, actorURI)
	if err != nil {
		return inboxBadRequest
	}
	post, err := s.resolvePost(ctx, objectURI)
	if err != nil {
		post, err = s.fetchAndCreatePost(ctx, objectURI, actorURI)
		if err != nil {
			return inboxNotFound
		}
	}
	var existing store.BoostEntity
	if err := s.db.WithContext(ctx).
		Where("post_id = ? AND publisher_id = ?", post.ID, actor.ID).
		First(&existing).Error; err == nil {
		return inboxSuccess
	}
	activityID := getString(activity, "id")
	now := time.Now().UTC()
	boost := &store.BoostEntity{
		ID:             uuid.New(),
		PostID:         post.ID,
		PublisherID:    actor.ID,
		BoostedAt:      &now,
		ActivityPubURI: stringPtr(activityID),
	}
	if err := s.db.WithContext(ctx).Create(boost).Error; err != nil {
		return inboxBadRequest
	}
	post.BoostCount++
	_ = s.db.WithContext(ctx).Save(post).Error
	return inboxSuccess
}

func (s *server) handleDelete(ctx context.Context, actorURI string, activity map[string]any) inboxResult {
	objectURI := getString(activity, "object")
	if objectURI == "" {
		return inboxBadRequest
	}
	post, err := s.resolvePost(ctx, objectURI)
	if err != nil {
		return inboxSuccess
	}
	if err := s.db.WithContext(ctx).Delete(post).Error; err != nil {
		return inboxBadRequest
	}
	return inboxSuccess
}

func (s *server) handleUpdate(ctx context.Context, actorURI string, activity map[string]any) inboxResult {
	object := getObject(activity, "object")
	if object == nil {
		return inboxBadRequest
	}
	objectURI := getString(object, "id")
	if objectURI == "" {
		return inboxBadRequest
	}
	actor, err := s.resolveOrCreateActor(ctx, actorURI)
	if err != nil {
		return inboxBadRequest
	}
	post, err := s.resolvePost(ctx, objectURI)
	if err != nil {
		created, createErr := s.createFederatedPost(ctx, object, actor)
		if createErr != nil {
			return inboxBadRequest
		}
		post = created
	}
	if name := getString(object, "name"); name != "" {
		post.Title = &name
	}
	if content := getString(object, "content"); content != "" {
		post.Content = &content
	}
	now := time.Now().UTC()
	if updated := parseInstant(object["updated"]); updated != nil {
		post.EditedAt = updated
	} else {
		post.EditedAt = &now
	}
	if _, ok := object["attachment"]; ok {
		post.Attachments = store.ToJSON(convertAttachments(object["attachment"]))
	}
	if err := s.db.WithContext(ctx).Save(post).Error; err != nil {
		return inboxBadRequest
	}
	return inboxSuccess
}

func (s *server) handlePin(ctx context.Context, _ string, activity map[string]any, pin bool) inboxResult {
	target := getObject(activity, "target")
	if target == nil {
		return inboxBadRequest
	}
	collection := getString(target, "id")
	if !strings.Contains(collection, "/featured") {
		return inboxSuccess
	}
	object := getObject(activity, "object")
	if object == nil {
		return inboxBadRequest
	}
	objectURI := getString(object, "id")
	if objectURI == "" {
		return inboxBadRequest
	}
	post, err := s.resolvePost(ctx, objectURI)
	if err != nil {
		if pin {
			return inboxNotFound
		}
		return inboxSuccess
	}
	if pin && (post.PinMode == nil || *post.PinMode != int(model.PinModePublisherPage)) {
		mode := int(model.PinModePublisherPage)
		post.PinMode = &mode
		_ = s.db.WithContext(ctx).Save(post).Error
	}
	if !pin && post.PinMode != nil && *post.PinMode == int(model.PinModePublisherPage) {
		post.PinMode = nil
		_ = s.db.WithContext(ctx).Save(post).Error
	}
	return inboxSuccess
}

func (s *server) handleBlock(ctx context.Context, actorURI string, activity map[string]any) inboxResult {
	targetURI := getString(activity, "object")
	if targetURI == "" {
		return inboxBadRequest
	}
	domain := hostOfURL(targetURI)
	if domain == "" {
		return inboxBadRequest
	}
	var existing store.FediverseModerationRuleEntity
	err := s.db.WithContext(ctx).Where("domain = ?", domain).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		name := "Block: " + domain
		rule := &store.FediverseModerationRuleEntity{
			ID:        uuid.New(),
			Name:      name,
			Type:      ruleDomainBlock,
			Action:    actionBlock,
			Domain:    &domain,
			IsEnabled: true,
		}
		if createErr := s.db.WithContext(ctx).Create(rule).Error; createErr != nil {
			return inboxBadRequest
		}
		s.moderation.invalidate()
	}
	s.log.Info("Block activity stored a domain rule", "actor", actorURI, "domain", domain)
	return inboxSuccess
}

// ---- resolution -----------------------------------------------------------

func (s *server) resolveOrCreateActor(ctx context.Context, actorURI string) (*store.PublisherEntity, error) {
	if actorURI == "" {
		return nil, errors.New("missing actor URI")
	}
	var actor store.PublisherEntity
	err := s.db.WithContext(ctx).Unscoped().Where("uri = ?", actorURI).First(&actor).Error
	if err == nil {
		if actor.DeletedAt.Valid {
			actor.DeletedAt = gorm.DeletedAt{}
			_ = s.db.WithContext(ctx).Unscoped().Save(&actor).Error
		}
		return &actor, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if hostOfURL(actorURI) == s.domain {
		return nil, errors.New("local actor not found for " + actorURI)
	}
	return s.discovery.getOrCreateActor(ctx, actorURI, "", nil)
}

func (s *server) resolveTargetActor(ctx context.Context, objectURI string, localActor *store.PublisherEntity) (*store.PublisherEntity, error) {
	if localActor != nil && localActor.URI != nil && *localActor.URI == objectURI {
		return localActor, nil
	}
	var actor store.PublisherEntity
	if err := s.db.WithContext(ctx).Where("uri = ?", objectURI).First(&actor).Error; err == nil {
		return &actor, nil
	}
	return s.resolveOrCreateActor(ctx, objectURI)
}

func (s *server) resolvePost(ctx context.Context, objectURI string) (*store.PostEntity, error) {
	if objectURI == "" {
		return nil, errors.New("missing object URI")
	}
	parsed, err := url.Parse(objectURI)
	if err == nil && parsed.Host == s.domain {
		segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		if len(segments) > 0 {
			if id, parseErr := uuid.Parse(segments[len(segments)-1]); parseErr == nil {
				var post store.PostEntity
				if err := s.db.WithContext(ctx).Where("id = ?", id).First(&post).Error; err == nil {
					return &post, nil
				}
			}
		}
	}
	var post store.PostEntity
	if err := s.db.WithContext(ctx).Where("fediverse_uri = ?", objectURI).First(&post).Error; err != nil {
		return nil, err
	}
	return &post, nil
}

func (s *server) fetchAndCreatePost(ctx context.Context, postURI, actorURI string) (*store.PostEntity, error) {
	actor, err := s.resolveOrCreateActor(ctx, actorURI)
	if err != nil {
		return nil, err
	}
	document, err := s.discovery.fetchDocument(ctx, postURI, nil)
	if err != nil {
		return nil, err
	}
	object := getObject(document, "object")
	if object == nil {
		object = document
	}
	objectType := getString(object, "type")
	if objectType != "Note" && objectType != "Article" {
		return nil, errors.New("object is not a Note or Article")
	}
	if objectID := getString(object, "id"); objectID != "" {
		var existing store.PostEntity
		if err := s.db.WithContext(ctx).Where("fediverse_uri = ?", objectID).First(&existing).Error; err == nil {
			return &existing, nil
		}
	}
	return s.createFederatedPost(ctx, object, actor)
}

// ---- conversions ----------------------------------------------------------

func convertAttachments(raw any) []model.CloudFileReferenceObject {
	result := make([]model.CloudFileReferenceObject, 0)
	switch typed := raw.(type) {
	case []any:
		for _, item := range typed {
			if attachment, ok := attachmentReference(item); ok {
				result = append(result, attachment)
			}
		}
	case map[string]any:
		if attachment, ok := attachmentReference(typed); ok {
			result = append(result, attachment)
		}
	}
	return result
}

func attachmentReference(raw any) (model.CloudFileReferenceObject, bool) {
	object, ok := raw.(map[string]any)
	if !ok {
		return model.CloudFileReferenceObject{}, false
	}
	target := getString(object, "url")
	if target == "" {
		if urls := getObjects(object, "url"); len(urls) > 0 {
			target = anyToString(urls[0])
		}
	}
	if target == "" {
		return model.CloudFileReferenceObject{}, false
	}
	reference := model.CloudFileReferenceObject{
		Url:      target,
		MimeType: getString(object, "mediaType"),
	}
	if width, ok := object["width"].(float64); ok {
		value := int32(width)
		reference.Width = &value
	}
	if height, ok := object["height"].(float64); ok {
		value := int32(height)
		reference.Height = &value
	}
	if blurhash := getString(object, "blurhash"); blurhash != "" {
		reference.Blurhash = blurhash
	}
	if size, ok := object["size"].(float64); ok {
		reference.Size = int64(size)
	}
	return reference, true
}

func reactionFromEmoji(emoji string) (string, int) {
	switch emoji {
	case "👍", "👍🏻", "👍🏼", "👍🏽", "👍🏾", "👍🏿":
		return "thumb_up", int(model.ReactionPositive)
	case "👎", "👎🏻", "👎🏼", "👎🏽", "👎🏾", "👎🏿":
		return "thumb_down", int(model.ReactionNegative)
	case "❤️", "♥️":
		return "heart", int(model.ReactionPositive)
	case "😂", "🤣":
		return "laugh", int(model.ReactionPositive)
	case "🎉":
		return "party", int(model.ReactionPositive)
	case "🙏", "🤗":
		return "pray", int(model.ReactionPositive)
	case "👋":
		return "hello", int(model.ReactionNeutral)
	case "😱":
		return "shock", int(model.ReactionNeutral)
	case "😶":
		return "speechless", int(model.ReactionNeutral)
	case "🫡":
		return "salute", int(model.ReactionPositive)
	case "😕":
		return "confuse", int(model.ReactionNeutral)
	case "😡":
		return "angry", int(model.ReactionNegative)
	default:
		return "heart", int(model.ReactionPositive)
	}
}


package fediverse

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/core"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/ports"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

// Post lifecycle events accepted by PublishPost.
const (
	federationEventCreated = "created"
	federationEventUpdated = "updated"
	federationEventDeleted = "deleted"
)

// ProvidePorts publishes the federation port so sibling modules can deliver
// post activities without importing this package. The port resolves the
// module's server lazily because Configure runs after ProvidePorts.
func (i *instance) ProvidePorts(_ context.Context, host *module.Host[core.Services]) error {
	module.Provide[ports.Federation](host.Ports, ports.PortFederation, ModuleID,
		module.KindFeature, &federationPort{instance: i})
	return nil
}

type federationPort struct {
	instance *instance
}

func (p *federationPort) server() (*server, error) {
	if p.instance == nil || p.instance.srv == nil {
		return nil, ports.ErrUnavailable
	}
	return p.instance.srv, nil
}

// PublishPost fans the post's Create/Update/Delete activity out to the
// publisher's remote followers through the module's delivery queue.
func (p *federationPort) PublishPost(ctx context.Context, postID string, event string) error {
	var activityType string
	switch event {
	case federationEventCreated:
		activityType = "Create"
	case federationEventUpdated:
		activityType = "Update"
	case federationEventDeleted:
		activityType = "Delete"
	default:
		return fmt.Errorf("fediverse: unknown post event %q", event)
	}

	srv, err := p.server()
	if err != nil {
		return err
	}
	id, err := uuid.Parse(postID)
	if err != nil {
		return fmt.Errorf("fediverse: invalid post id %q: %w", postID, err)
	}
	post, publisher, err := srv.federationPost(ctx, id, event == federationEventDeleted)
	if err != nil {
		return err
	}
	// Posts mirrored from the fediverse are never re-delivered.
	if post.FediverseURI != nil && *post.FediverseURI != "" {
		return nil
	}
	if !federatesPublisher(publisher) {
		return nil
	}
	// The rendered object is addressed to the public collection, so drafts and
	// non-public posts are not delivered.
	if post.DraftedAt != nil || post.Visibility != int(model.VisibilityPublic) {
		return nil
	}

	actorURI := *publisher.URI
	objectURL := srv.objectURL(post.ID)
	activityID := objectURL + "/activity"
	var object any
	switch event {
	case federationEventDeleted:
		object = map[string]any{"id": objectURL, "type": "Tombstone"}
	case federationEventUpdated:
		document, renderErr := srv.postObject(ctx, post, actorURI)
		if renderErr != nil {
			return renderErr
		}
		document["url"] = srv.baseURL + "/posts/" + post.ID.String()
		object = document
		activityID = fmt.Sprintf("%s/activity#updates/%d", objectURL, time.Now().UTC().UnixNano())
	default:
		document, renderErr := srv.postObject(ctx, post, actorURI)
		if renderErr != nil {
			return renderErr
		}
		document["url"] = srv.baseURL + "/posts/" + post.ID.String()
		object = document
	}
	activity := map[string]any{
		"@context": "https://www.w3.org/ns/activitystreams",
		"id":       activityID,
		"type":     activityType,
		"actor":    actorURI,
		"object":   object,
		"to":       []any{publicCollection},
		"cc":       []any{actorURI + "/followers"},
	}

	inboxes, err := srv.followerInboxes(ctx, publisher.ID)
	if err != nil {
		return err
	}
	var deliveryErrs []error
	for _, inbox := range inboxes {
		if _, err := srv.delivery.enqueue(ctx, activityType, activity, actorURI, inbox, activityID, publisher); err != nil {
			deliveryErrs = append(deliveryErrs, fmt.Errorf("fediverse: enqueue %s for %s: %w", activityType, inbox, err))
		}
	}
	return errors.Join(deliveryErrs...)
}

// Enabled reports whether the post's publisher federates.
func (p *federationPort) Enabled(ctx context.Context, postID string) (bool, error) {
	srv, err := p.server()
	if err != nil {
		return false, err
	}
	id, err := uuid.Parse(postID)
	if err != nil {
		return false, fmt.Errorf("fediverse: invalid post id %q: %w", postID, err)
	}
	post, publisher, err := srv.federationPost(ctx, id, false)
	if err != nil {
		return false, err
	}
	if post.FediverseURI != nil && *post.FediverseURI != "" {
		return false, nil
	}
	return federatesPublisher(publisher), nil
}

// federationPost loads the post (including soft-deleted rows for the deleted
// event) and its publisher.
func (s *server) federationPost(ctx context.Context, id uuid.UUID, includeDeleted bool) (*store.PostEntity, *store.PublisherEntity, error) {
	query := s.db.WithContext(ctx)
	if includeDeleted {
		query = query.Unscoped()
	}
	var post store.PostEntity
	if err := query.Where("id = ?", id).First(&post).Error; err != nil {
		return nil, nil, err
	}
	var publisher store.PublisherEntity
	if err := s.db.WithContext(ctx).Where("id = ?", post.PublisherID).First(&publisher).Error; err != nil {
		return nil, nil, err
	}
	return &post, &publisher, nil
}

// followerInboxes returns the deduplicated remote inboxes of a publisher's
// accepted followers. Local followers have no inbox URI and are skipped.
func (s *server) followerInboxes(ctx context.Context, publisherID uuid.UUID) ([]string, error) {
	var subscriptions []store.PublisherSubscriptionEntity
	if err := s.db.WithContext(ctx).
		Where("publisher_id = ? AND state = ? AND ended_at IS NULL AND is_blocking = ?",
			publisherID, int(model.SubscriptionAccepted), false).
		Find(&subscriptions).Error; err != nil {
		return nil, err
	}
	followerIDs := make([]uuid.UUID, 0, len(subscriptions))
	seen := make(map[uuid.UUID]struct{}, len(subscriptions))
	for _, subscription := range subscriptions {
		if subscription.FollowerPublisherID == nil || *subscription.FollowerPublisherID == publisherID {
			continue
		}
		if _, ok := seen[*subscription.FollowerPublisherID]; ok {
			continue
		}
		seen[*subscription.FollowerPublisherID] = struct{}{}
		followerIDs = append(followerIDs, *subscription.FollowerPublisherID)
	}
	if len(followerIDs) == 0 {
		return nil, nil
	}
	var followers []store.PublisherEntity
	if err := s.db.WithContext(ctx).
		Where("id IN ? AND inbox_uri IS NOT NULL AND inbox_uri <> ''", followerIDs).
		Find(&followers).Error; err != nil {
		return nil, err
	}
	inboxes := make([]string, 0, len(followers))
	for i := range followers {
		if followers[i].InboxURI != nil && *followers[i].InboxURI != "" {
			inboxes = append(inboxes, *followers[i].InboxURI)
		}
	}
	return inboxes, nil
}

// federatesPublisher reports whether the publisher is a local actor that owns
// an ActivityPub identity. Remote mirrors never carry an account id.
func federatesPublisher(publisher *store.PublisherEntity) bool {
	return publisher != nil && publisher.AccountID != nil &&
		publisher.URI != nil && *publisher.URI != ""
}

package posts

import (
	"context"
	"encoding/json"
	"regexp"
	"sort"
	"strings"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/lighthouse/internal/platform/module"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/ports"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

// realtimeRecipientLimit caps the follower fan-out of one post event.
const realtimeRecipientLimit = 1000

var postMentionPattern = regexp.MustCompile(`@(?:u/)?([A-Za-z0-9_-]+)`)

// afterPostCreated dispatches the create side effects. Every step is
// best-effort: a missing port or a failing backend never fails the request.
func (h *handler) afterPostCreated(ctx context.Context, ent *store.PostEntity, post *model.Post, actor uuid.UUID) {
	h.dispatchPostEvent(ctx, ent, post, "created", actor)
}

func (h *handler) afterPostUpdated(ctx context.Context, ent *store.PostEntity, post *model.Post, actor uuid.UUID) {
	h.dispatchPostEvent(ctx, ent, post, "updated", actor)
}

func (h *handler) afterPostDeleted(ctx context.Context, ent *store.PostEntity) {
	h.dispatchPostEvent(ctx, ent, nil, "deleted", uuid.Nil)
}

func (h *handler) dispatchPostEvent(ctx context.Context, ent *store.PostEntity, post *model.Post, action string, actor uuid.UUID) {
	if ent == nil {
		return
	}
	h.pushPostEvent(ctx, ent, post, action)
	h.publishPostFederation(ctx, ent, action)
	if action != "deleted" {
		h.notifyPostAudience(ctx, ent, post, action, actor)
	}
}

// ---------------------------------------------------------------------------
// Realtime
// ---------------------------------------------------------------------------

func (h *handler) pushPostEvent(ctx context.Context, ent *store.PostEntity, post *model.Post, action string) {
	realtime, ok := module.Get[ports.Realtime](h.ports, ports.PortRealtime)
	if !ok {
		return
	}
	recipients := h.followerAccountIDs(ctx, ent.PublisherID)
	if len(recipients) == 0 {
		return
	}
	event := "posts." + map[string]string{
		"created": "new",
		"updated": "update",
		"deleted": "delete",
	}[action]
	var payload any
	if post != nil {
		payload = post
	} else {
		payload = map[string]any{"id": ent.ID.String()}
	}
	if err := realtime.Push(ctx, recipients, event, payload); err != nil {
		h.log.Warn("posts: realtime push failed", "event", event, "error", err)
	}
}

// followerAccountIDs returns the accepted followers of a publisher, including
// follower publishers' owning accounts, capped for a single fan-out.
func (h *handler) followerAccountIDs(ctx context.Context, publisherID uuid.UUID) []string {
	if publisherID == uuid.Nil {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, 32)
	add := func(id *uuid.UUID) {
		if id == nil || *id == uuid.Nil || len(out) >= realtimeRecipientLimit {
			return
		}
		key := id.String()
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}

	var accountIDs []uuid.UUID
	if err := h.db.WithContext(ctx).Model(&store.PublisherSubscriptionEntity{}).
		Where("publisher_id = ? AND state = ? AND ended_at IS NULL AND account_id IS NOT NULL",
			publisherID, int(model.SubscriptionAccepted)).
		Order("id ASC").
		Limit(realtimeRecipientLimit).
		Pluck("account_id", &accountIDs).Error; err != nil {
		h.log.Warn("posts: load followers failed", "publisher", publisherID, "error", err)
		return nil
	}
	for i := range accountIDs {
		add(&accountIDs[i])
	}

	if len(out) < realtimeRecipientLimit {
		var followerPublisherIDs []uuid.UUID
		if err := h.db.WithContext(ctx).Model(&store.PublisherSubscriptionEntity{}).
			Where("publisher_id = ? AND state = ? AND ended_at IS NULL AND follower_publisher_id IS NOT NULL",
				publisherID, int(model.SubscriptionAccepted)).
			Order("id ASC").
			Limit(realtimeRecipientLimit).
			Pluck("follower_publisher_id", &followerPublisherIDs).Error; err == nil && len(followerPublisherIDs) > 0 {
			var ownerIDs []uuid.UUID
			if err := h.db.WithContext(ctx).Model(&store.PublisherEntity{}).
				Where("id IN ? AND account_id IS NOT NULL", followerPublisherIDs).
				Pluck("account_id", &ownerIDs).Error; err == nil {
				for i := range ownerIDs {
					add(&ownerIDs[i])
				}
			}
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Federation
// ---------------------------------------------------------------------------

func (h *handler) publishPostFederation(ctx context.Context, ent *store.PostEntity, action string) {
	federation, ok := module.Get[ports.Federation](h.ports, ports.PortFederation)
	if !ok {
		return
	}
	if err := federation.PublishPost(ctx, ent.ID.String(), action); err != nil {
		h.log.Warn("posts: federation publish failed", "post", ent.ID, "action", action, "error", err)
	}
}

// ---------------------------------------------------------------------------
// Notifications
// ---------------------------------------------------------------------------

func (h *handler) notifyPostAudience(ctx context.Context, ent *store.PostEntity, post *model.Post, action string, actor uuid.UUID) {
	notify, ok := module.Get[ports.Notify](h.ports, ports.PortNotify)
	if !ok {
		return
	}
	recipients := map[string]struct{}{}
	add := func(id uuid.UUID) {
		if id == uuid.Nil || id == actor {
			return
		}
		recipients[id.String()] = struct{}{}
	}

	for _, id := range h.mentionedAccountIDs(ctx, ent) {
		add(id)
	}

	if ent.RepliedPostID != nil {
		if author, ok := h.postAuthorAccountID(ctx, *ent.RepliedPostID); ok {
			if h.replyNotificationsEnabled(ctx, author) {
				add(author)
			}
		}
	}
	if len(recipients) == 0 {
		return
	}
	ids := make([]string, 0, len(recipients))
	for id := range recipients {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	title := "New post"
	body := "A post you subscribed to was published."
	if action == "updated" {
		title = "Post updated"
		body = "A post you subscribed to was updated."
	}
	if post != nil && post.Publisher != nil && post.Publisher.Name != "" {
		body = post.Publisher.Name + " published a post."
	}
	meta := map[string]string{"post_id": ent.ID.String(), "event": "post." + action}
	if err := notify.SendPushToAccounts(ctx, ids, title, body, meta); err != nil {
		h.log.Warn("posts: notify post audience failed", "post", ent.ID, "error", err)
	}
}

// postAuthorAccountID resolves the owning account of a post's publisher.
func (h *handler) postAuthorAccountID(ctx context.Context, postID uuid.UUID) (uuid.UUID, bool) {
	var ent store.PostEntity
	if err := h.db.WithContext(ctx).Where("id = ?", postID).First(&ent).Error; err != nil {
		return uuid.Nil, false
	}
	publisher, err := h.publisherByID(ctx, ent.PublisherID)
	if err != nil || publisher.AccountID == nil {
		return uuid.Nil, false
	}
	return *publisher.AccountID, true
}

// replyNotificationsEnabled honours post_watch_preferences.notify_replies when
// a preference row exists for the account.
func (h *handler) replyNotificationsEnabled(ctx context.Context, accountID uuid.UUID) bool {
	var preference store.PostWatchPreferenceEntity
	err := h.db.WithContext(ctx).
		Where("account_id = ? AND source = ?", accountID, watchSourceReply).
		First(&preference).Error
	if err != nil {
		return true
	}
	return preference.NotifyReplies
}

// mentionedAccountIDs resolves @mentions from the stored mention list and the
// post body through the accounts port. Unresolved names are skipped.
func (h *handler) mentionedAccountIDs(ctx context.Context, ent *store.PostEntity) []uuid.UUID {
	names := map[string]struct{}{}
	addName := func(name string) {
		name = strings.TrimSpace(strings.TrimPrefix(strings.ToLower(name), "@"))
		name = strings.TrimPrefix(name, "u/")
		if name != "" {
			names[name] = struct{}{}
		}
	}
	if len(ent.Mentions) > 0 {
		var mentions []model.ContentMention
		if err := json.Unmarshal(ent.Mentions, &mentions); err == nil {
			for _, mention := range mentions {
				if mention.Username != nil {
					addName(*mention.Username)
				}
			}
		}
	}
	if ent.Content != nil {
		for _, match := range postMentionPattern.FindAllStringSubmatch(*ent.Content, -1) {
			if len(match) > 1 {
				addName(match[1])
			}
		}
	}
	if len(names) == 0 {
		return nil
	}
	accounts, ok := module.Get[ports.Accounts](h.ports, ports.PortAccounts)
	if !ok {
		return nil
	}
	seen := map[uuid.UUID]struct{}{}
	var out []uuid.UUID
	for name := range names {
		found, err := accounts.SearchAccounts(ctx, name, 5)
		if err != nil {
			continue
		}
		for _, account := range found {
			if !strings.EqualFold(account.GetName(), name) {
				continue
			}
			id, err := uuid.Parse(account.GetId())
			if err != nil || id == uuid.Nil {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
			break
		}
	}
	return out
}

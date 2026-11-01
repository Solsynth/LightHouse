package posts

import (
	"context"
	"encoding/json"
	"regexp"
	"sort"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/filejson"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"

	"src.solsynth.dev/sosys/lighthouse/internal/sphere/localization"
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
	if !ok || ent == nil {
		return
	}
	// Drafted posts do not dispatch push notifications.
	if ent.DraftedAt != nil {
		return
	}

	publisher, _ := h.publisherByID(ctx, ent.PublisherID)
	publisherName := publisherDisplayName(publisher, post)
	// Every audience push here is about the post itself, so the subject avatar
	// is the publisher's.
	avatarID := publisherAvatarID(publisher)
	postedFrom := "/posts/" + ent.ID.String()

	notified := make(map[uuid.UUID]struct{})

	// 1. Reply notification: if this post is a reply to another post.
	if ent.RepliedPostID != nil {
		if author, ok := h.postAuthorAccountID(ctx, *ent.RepliedPostID); ok {
			if author != actor && h.replyNotificationsEnabled(ctx, author) && !h.isBlockedOrMuted(ctx, author, actor) {
				lang := h.accountLanguage(ctx, author)
				actorName := publisherName
				if actorName == "" {
					actorName = "Someone"
				}
				title := localization.Localize(lang, "postReplyTitle", map[string]string{
					"user": actorName,
				})
				_, body := chopPostForNotification(ent, lang)
				meta := h.postNotificationMeta(ctx, ent, post, publisher, lang, avatarID, map[string]any{
					"notification_type": "post_watch",
					"watch_event_type":  "reply",
					"topic":             "posts.watch.replies",
					"action_uri":        postedFrom,
				})
				if err := notify.SendPush(ctx, author.String(), title, body, meta); err == nil {
					notified[author] = struct{}{}
				}
			}
		}
	} else if action == "created" {
		// 2. Subscriber / Audience notification (only for top-level posts on publish).
		var pubAccountID *uuid.UUID
		if id, ok := h.publisherAccountID(ctx, ent.PublisherID); ok {
			pubAccountID = &id
		}

		if ent.Visibility == int(model.VisibilityCloseFriendsOnly) {
			// Close friends visibility: notify accounts author considers close friends.
			if pubAccountID != nil && h.core != nil && h.core.Clients != nil && h.core.Clients.Profile != nil {
				if resp, err := h.core.Clients.Profile.ListCloseFriends(ctx, &gen.DyListRelationshipSimpleRequest{
					RelationIdentifier: &gen.DyListRelationshipSimpleRequest_AccountId{AccountId: pubAccountID.String()},
				}); err == nil && resp != nil {
					var rawIDs []uuid.UUID
					for _, raw := range resp.GetAccountsId() {
						if id, err := uuid.Parse(raw); err == nil && id != actor && id != uuid.Nil {
							rawIDs = append(rawIDs, id)
						}
					}
					rawIDs = h.filterBlockedAndMuted(ctx, *pubAccountID, rawIDs)
					if len(rawIDs) > 0 {
						byLang := h.accountLanguages(ctx, rawIDs)
						for lang, userIDs := range byLang {
							for _, uidStr := range userIDs {
								if uid, err := uuid.Parse(uidStr); err == nil {
									notified[uid] = struct{}{}
								}
							}
							title, body := chopPostForNotification(ent, lang)
							if publisherName != "" {
								title = localization.Localize(lang, "postCloseFriendTitle", map[string]string{
									"publisher": publisherName,
								})
							}
							meta := h.postNotificationMeta(ctx, ent, post, publisher, lang, avatarID, map[string]any{
								"notification_type": "close_friends",
								"topic":             "posts.close_friends",
								"action_uri":        postedFrom,
							})
							_ = notify.SendPushToAccounts(ctx, userIDs, title, body, meta)
						}
					}
				}
			}
		} else if ent.Visibility == int(model.VisibilityPublic) || ent.Visibility == int(model.VisibilityQuietPublic) {
			// Public visibility: gather publisher subscribers & topic subscribers.
			var subAccountIDs []uuid.UUID
			h.db.WithContext(ctx).Model(&store.PublisherSubscriptionEntity{}).
				Where("publisher_id = ? AND state = ? AND ended_at IS NULL AND is_blocking = false AND notify = true AND account_id IS NOT NULL",
					ent.PublisherID, int(model.SubscriptionAccepted)).
				Pluck("account_id", &subAccountIDs)

			// Topic subscribers (categories, tags, collections):
			var categoryIDs []uuid.UUID
			_ = h.db.WithContext(ctx).Table("post_category_links").
				Where("posts_id = ?", ent.ID).Pluck("categories_id", &categoryIDs).Error

			var tagIDs []uuid.UUID
			_ = h.db.WithContext(ctx).Table("post_tag_links").
				Where("posts_id = ?", ent.ID).Pluck("tags_id", &tagIDs).Error

			var collectionIDs []uuid.UUID
			_ = h.db.WithContext(ctx).Model(&store.PostCollectionItemEntity{}).
				Where("post_id = ?", ent.ID).Pluck("collection_id", &collectionIDs).Error

			if len(categoryIDs) > 0 || len(tagIDs) > 0 || len(collectionIDs) > 0 {
				var topicConditions []string
				var topicArgs []any
				if len(categoryIDs) > 0 {
					topicConditions = append(topicConditions, "category_id IN ?")
					topicArgs = append(topicArgs, categoryIDs)
				}
				if len(tagIDs) > 0 {
					topicConditions = append(topicConditions, "tag_id IN ?")
					topicArgs = append(topicArgs, tagIDs)
				}
				if len(collectionIDs) > 0 {
					topicConditions = append(topicConditions, "collection_id IN ?")
					topicArgs = append(topicArgs, collectionIDs)
				}
				var topicSubs []uuid.UUID
				_ = h.db.WithContext(ctx).Model(&store.PostCategorySubscriptionEntity{}).
					Where(strings.Join(topicConditions, " OR "), topicArgs...).
					Pluck("account_id", &topicSubs).Error
				subAccountIDs = append(subAccountIDs, topicSubs...)
			}

			// Deduplicate and exclude actor / nil
			unique := make(map[uuid.UUID]struct{}, len(subAccountIDs))
			cleanIDs := make([]uuid.UUID, 0, len(subAccountIDs))
			for _, id := range subAccountIDs {
				if id == uuid.Nil || id == actor {
					continue
				}
				if _, seen := unique[id]; !seen {
					unique[id] = struct{}{}
					cleanIDs = append(cleanIDs, id)
				}
			}

			if pubAccountID != nil {
				cleanIDs = h.filterBlockedAndMuted(ctx, *pubAccountID, cleanIDs)
			}

			if len(cleanIDs) > 0 {
				byLang := h.accountLanguages(ctx, cleanIDs)
				for lang, userIDs := range byLang {
					for _, uidStr := range userIDs {
						if uid, err := uuid.Parse(uidStr); err == nil {
							notified[uid] = struct{}{}
						}
					}
					sort.Strings(userIDs)
					title, body := chopPostForNotification(ent, lang)
					if publisherName != "" {
						title = localization.Localize(lang, "postSubscriptionTitle", map[string]string{
							"publisher": publisherName,
						})
					}
					meta := h.postNotificationMeta(ctx, ent, post, publisher, lang, avatarID, map[string]any{
						"notification_type": "subscription",
						"topic":             "posts.new",
						"action_uri":        postedFrom,
					})
					_ = notify.SendPushToAccounts(ctx, userIDs, title, body, meta)
				}
			}
		}
	}

	// 3. Mentions: notify accounts mentioned in the post that were not already notified.
	var mentionRecipients []uuid.UUID
	for _, id := range h.mentionedAccountIDs(ctx, ent) {
		if id == uuid.Nil || id == actor {
			continue
		}
		if _, already := notified[id]; !already {
			if !h.isBlockedOrMuted(ctx, id, actor) {
				mentionRecipients = append(mentionRecipients, id)
				notified[id] = struct{}{}
			}
		}
	}
	if len(mentionRecipients) > 0 {
		byLang := h.accountLanguages(ctx, mentionRecipients)
		for lang, userIDs := range byLang {
			sort.Strings(userIDs)
			actorName := publisherName
			if actorName == "" {
				actorName = "Someone"
			}
			title := localization.Localize(lang, "postMentionTitle", map[string]string{
				"user": actorName,
			})
			_, content := chopPostForNotification(ent, lang)
			body := localization.Localize(lang, "postMentionBody", map[string]string{
				"user":    actorName,
				"content": content,
			})
			meta := h.postNotificationMeta(ctx, ent, post, publisher, lang, avatarID, map[string]any{
				"notification_type": "mention",
				"topic":             "posts.mention",
				"action_uri":        postedFrom,
			})
			_ = notify.SendPushToAccounts(ctx, userIDs, title, body, meta)
		}
	}
}

func chopPostForNotification(ent *store.PostEntity, locale string) (title, body string) {
	if ent.Title != nil && strings.TrimSpace(*ent.Title) != "" {
		title = strings.TrimSpace(*ent.Title)
	}
	if ent.Description != nil && strings.TrimSpace(*ent.Description) != "" {
		d := strings.TrimSpace(*ent.Description)
		if len(d) > 40 {
			body = d[:37] + "..."
		} else {
			body = d
		}
		if title == "" {
			if len(d) > 10 {
				title = d[:10] + "..."
			} else {
				title = d
			}
		}
	} else if ent.Content != nil && strings.TrimSpace(*ent.Content) != "" {
		c := strings.TrimSpace(*ent.Content)
		if len(c) > 100 {
			body = c[:97] + "..."
		} else {
			body = c
		}
		if title == "" {
			if len(c) > 20 {
				title = c[:17] + "..."
			} else {
				title = c
			}
		}
	}
	if title == "" {
		title = localization.Localize(locale, "postSharedMedia", map[string]string{"count": "1"})
		if title == "postSharedMedia" {
			title = "New post"
		}
	}
	if body == "" {
		body = title
	}
	return title, body
}

// publisherDisplayName is the publisher's nick with the name as fallback. The
// hydrated post carries the same identity; it only bridges a failed row load.
func publisherDisplayName(publisher *store.PublisherEntity, post *model.Post) string {
	if publisher != nil {
		if nick := strings.TrimSpace(publisher.Nick); nick != "" {
			return nick
		}
		if name := strings.TrimSpace(publisher.Name); name != "" {
			return name
		}
	}
	if post != nil && post.Publisher != nil {
		if nick := strings.TrimSpace(post.Publisher.Nick); nick != "" {
			return nick
		}
		if name := strings.TrimSpace(post.Publisher.Name); name != "" {
			return name
		}
	}
	return ""
}

// publisherAvatarID is the publisher's avatar file id, empty when unset.
func publisherAvatarID(publisher *store.PublisherEntity) string {
	if publisher == nil {
		return ""
	}
	if file := filejson.Reference(publisher.Picture); file != nil {
		return file.Id
	}
	return ""
}

// accountAvatarID resolves an account avatar file id. Profile RPC accounts
// carry it on the nested profile, gateway-shaped accounts on the top-level
// picture.
func accountAvatarID(account *gen.DyAccount) string {
	if account == nil {
		return ""
	}
	if file := account.GetProfile().GetPicture(); file != nil && file.GetId() != "" {
		return file.GetId()
	}
	return account.GetPicture().GetId()
}

// postNotificationMeta mirrors the C# BuildPostNotificationMeta payload: the
// chopped post header, publisher identity, subject avatar, image attachments,
// tags, categories and replied/forwarded ids, plus the caller's extra keys.
//
// avatarID is the avatar of the notification's subject (the acting account or
// the post's publisher). It is deliberately never backfilled from the post
// author: a subject without an avatar ships no pfp, so clients keep their own
// placeholder instead of showing another account's picture.
func (h *handler) postNotificationMeta(ctx context.Context, ent *store.PostEntity, post *model.Post, publisher *store.PublisherEntity, locale, avatarID string, extra map[string]any) map[string]any {
	title, content := chopPostForNotification(ent, locale)
	data := map[string]any{
		"post_id":      ent.ID.String(),
		"post_title":   title,
		"post_content": content,
	}
	if ent.PublisherID != uuid.Nil {
		data["publisher_id"] = ent.PublisherID.String()
	}
	if ent.RepliedPostID != nil {
		data["replied_post_id"] = ent.RepliedPostID.String()
	}
	if ent.ForwardedPostID != nil {
		data["forwarded_post_id"] = ent.ForwardedPostID.String()
	}
	if publisher != nil {
		if publisher.Name != "" {
			data["publisher_name"] = publisher.Name
		}
		if publisher.Nick != "" {
			data["publisher_nick"] = publisher.Nick
		}
	} else if post != nil && post.Publisher != nil {
		if post.Publisher.Name != "" {
			data["publisher_name"] = post.Publisher.Name
		}
		if post.Publisher.Nick != "" {
			data["publisher_nick"] = post.Publisher.Nick
		}
	}
	if avatarID != "" {
		data["pfp"] = avatarID
		data["avatar"] = avatarID
	}
	attachments := h.postNotificationAttachments(ent, post)
	images := make([]string, 0, len(attachments))
	for _, attachment := range attachments {
		if !strings.HasPrefix(attachment.MimeType, "image/") {
			continue
		}
		if reference := attachment.Url; reference != "" {
			images = append(images, reference)
		} else if attachment.Id != "" {
			images = append(images, attachment.Id)
		}
	}
	if len(images) > 0 {
		data["images"] = images
		data["image"] = images[0]
	}
	if tags := h.postTagSlugs(ctx, ent.ID); len(tags) > 0 {
		data["tags"] = tags
	}
	if categories := h.postCategorySlugs(ctx, ent.ID); len(categories) > 0 {
		data["categories"] = categories
	}
	for key, value := range extra {
		data[key] = value
	}
	return data
}

// postNotificationAttachments prefers the hydrated post's attachments and
// falls back to the row's jsonb column.
func (h *handler) postNotificationAttachments(ent *store.PostEntity, post *model.Post) []model.CloudFileReferenceObject {
	if post != nil && len(post.Attachments) > 0 {
		return post.Attachments
	}
	return filejson.References(ent.Attachments)
}

func (h *handler) postTagSlugs(ctx context.Context, postID uuid.UUID) []string {
	var links []store.PostTagLink
	if err := h.db.WithContext(ctx).Where("posts_id = ?", postID).Find(&links).Error; err != nil || len(links) == 0 {
		return nil
	}
	tagIDs := make([]uuid.UUID, 0, len(links))
	for _, link := range links {
		tagIDs = append(tagIDs, link.TagID)
	}
	var tags []store.PostTagEntity
	if err := h.db.WithContext(ctx).Where("id IN ?", tagIDs).Find(&tags).Error; err != nil {
		return nil
	}
	slugs := make([]string, 0, len(tags))
	for _, tag := range tags {
		if tag.Slug != "" {
			slugs = append(slugs, tag.Slug)
		}
	}
	return slugs
}

func (h *handler) postCategorySlugs(ctx context.Context, postID uuid.UUID) []string {
	var links []store.PostCategoryLink
	if err := h.db.WithContext(ctx).Where("posts_id = ?", postID).Find(&links).Error; err != nil || len(links) == 0 {
		return nil
	}
	categoryIDs := make([]uuid.UUID, 0, len(links))
	for _, link := range links {
		categoryIDs = append(categoryIDs, link.CategoryID)
	}
	var categories []store.PostCategoryEntity
	if err := h.db.WithContext(ctx).Where("id IN ?", categoryIDs).Find(&categories).Error; err != nil {
		return nil
	}
	slugs := make([]string, 0, len(categories))
	for _, category := range categories {
		if category.Slug != "" {
			slugs = append(slugs, category.Slug)
		}
	}
	return slugs
}

// publisherAccountID resolves the owning account of a publisher.
func (h *handler) publisherAccountID(ctx context.Context, publisherID uuid.UUID) (uuid.UUID, bool) {
	publisher, err := h.publisherByID(ctx, publisherID)
	if err != nil || publisher == nil || publisher.AccountID == nil {
		return uuid.Nil, false
	}
	return *publisher.AccountID, true
}

// postAuthorAccountID resolves the owning account of a post's publisher.
func (h *handler) postAuthorAccountID(ctx context.Context, postID uuid.UUID) (uuid.UUID, bool) {
	var ent store.PostEntity
	if err := h.db.WithContext(ctx).Where("id = ?", postID).First(&ent).Error; err != nil {
		return uuid.Nil, false
	}
	return h.publisherAccountID(ctx, ent.PublisherID)
}

func (h *handler) accountLanguages(ctx context.Context, accountIDs []uuid.UUID) map[string][]string {
	byLang := make(map[string][]string)
	if len(accountIDs) == 0 {
		return byLang
	}
	idStrings := make([]string, len(accountIDs))
	for i, id := range accountIDs {
		idStrings[i] = id.String()
	}
	seen := make(map[string]struct{}, len(idStrings))
	if accounts, ok := module.Get[ports.Accounts](h.ports, ports.PortAccounts); ok {
		if list, err := accounts.GetAccounts(ctx, idStrings); err == nil {
			for _, acc := range list {
				if acc == nil {
					continue
				}
				lang := acc.GetLanguage()
				if lang == "" {
					lang = "en"
				}
				byLang[lang] = append(byLang[lang], acc.GetId())
				seen[acc.GetId()] = struct{}{}
			}
		}
	}
	for _, idStr := range idStrings {
		if _, ok := seen[idStr]; !ok {
			byLang["en"] = append(byLang["en"], idStr)
		}
	}
	return byLang
}

func (h *handler) accountLanguage(ctx context.Context, accountID uuid.UUID) string {
	if accounts, ok := module.Get[ports.Accounts](h.ports, ports.PortAccounts); ok {
		if acc, err := accounts.GetAccount(ctx, accountID.String()); err == nil && acc != nil && acc.GetLanguage() != "" {
			return acc.GetLanguage()
		}
	}
	return "en"
}

func (h *handler) isBlockedOrMuted(ctx context.Context, accountID, otherID uuid.UUID) bool {
	if h.core == nil || h.core.Clients == nil || h.core.Clients.Profile == nil {
		return false
	}
	hasBlocked, err := h.core.Clients.Profile.HasRelationship(ctx, &gen.DyGetRelationshipRequest{
		AccountId:       accountID.String(),
		RelatedId:       otherID.String(),
		Status:          proto.Int32(-100),
		EitherDirection: true,
	})
	if err == nil && hasBlocked != nil && hasBlocked.GetValue() {
		return true
	}
	hasMuted, err := h.core.Clients.Profile.HasRelationship(ctx, &gen.DyGetRelationshipRequest{
		AccountId: accountID.String(),
		RelatedId: otherID.String(),
		Status:    proto.Int32(-50),
	})
	if err == nil && hasMuted != nil && hasMuted.GetValue() {
		return true
	}
	return false
}

func (h *handler) filterBlockedAndMuted(ctx context.Context, publisherAccountID uuid.UUID, accountIDs []uuid.UUID) []uuid.UUID {
	if len(accountIDs) == 0 || h.core == nil || h.core.Clients == nil || h.core.Clients.Profile == nil {
		return accountIDs
	}
	hidden := make(map[uuid.UUID]struct{})
	// Blocked in either direction:
	if blockedResp, err := h.core.Clients.Profile.ListBlocked(ctx, &gen.DyListRelationshipSimpleRequest{
		RelationIdentifier: &gen.DyListRelationshipSimpleRequest_AccountId{AccountId: publisherAccountID.String()},
	}); err == nil && blockedResp != nil {
		for _, raw := range blockedResp.GetAccountsId() {
			if id, err := uuid.Parse(raw); err == nil {
				hidden[id] = struct{}{}
			}
		}
	}
	if blockedByResp, err := h.core.Clients.Profile.ListBlocked(ctx, &gen.DyListRelationshipSimpleRequest{
		RelationIdentifier: &gen.DyListRelationshipSimpleRequest_RelatedId{RelatedId: publisherAccountID.String()},
	}); err == nil && blockedByResp != nil {
		for _, raw := range blockedByResp.GetAccountsId() {
			if id, err := uuid.Parse(raw); err == nil {
				hidden[id] = struct{}{}
			}
		}
	}
	// Muted by publisher:
	if mutedResp, err := h.core.Clients.Profile.ListMuted(ctx, &gen.DyListRelationshipSimpleRequest{
		RelationIdentifier: &gen.DyListRelationshipSimpleRequest_AccountId{AccountId: publisherAccountID.String()},
	}); err == nil && mutedResp != nil {
		for _, raw := range mutedResp.GetAccountsId() {
			if id, err := uuid.Parse(raw); err == nil {
				hidden[id] = struct{}{}
			}
		}
	}
	if len(hidden) == 0 {
		return accountIDs
	}
	filtered := make([]uuid.UUID, 0, len(accountIDs))
	for _, id := range accountIDs {
		if _, ok := hidden[id]; !ok {
			filtered = append(filtered, id)
		}
	}
	return filtered
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

// reactionNotificationsEnabled honours post_watch_preferences.notify_reactions when
// a preference row exists for the account.
func (h *handler) reactionNotificationsEnabled(ctx context.Context, accountID uuid.UUID) bool {
	var preference store.PostWatchPreferenceEntity
	err := h.db.WithContext(ctx).
		Where("account_id = ? AND source = ?", accountID, watchSourceReaction).
		First(&preference).Error
	if err != nil {
		return true
	}
	return preference.NotifyReactions
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

package fediverse

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/google/uuid"

	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

// publicCollection is the ActivityStreams public addressee.
const publicCollection = "https://www.w3.org/ns/activitystreams#Public"

// actorDocument renders the actor document ActivityPubController.GetActor
// returns for a local publisher.
func (s *server) actorDocument(ctx context.Context, publisher *store.PublisherEntity) (map[string]any, error) {
	publicKey, err := s.keys.publicKeyForActor(ctx, publisher)
	if err != nil {
		return nil, err
	}
	actorURL := s.actorURL(publisher.Name)
	actorType := "Person"
	if publisher.ActorType != nil && *publisher.ActorType != "" {
		actorType = *publisher.ActorType
	}
	document := map[string]any{
		"@context": []any{
			"https://www.w3.org/ns/activitystreams",
			"https://w3id.org/security/v1",
			map[string]any{
				"toot": "http://joinmastodon.org/ns#",
				"featured": map[string]any{
					"@id":   "toot:featured",
					"@type": "@id",
				},
			},
		},
		"id":                        actorURL,
		"type":                      actorType,
		"name":                      publisher.Nick,
		"preferredUsername":         publisher.Name,
		"summary":                   deref(publisher.Bio),
		"inbox":                     actorURL + "/inbox",
		"outbox":                    actorURL + "/outbox",
		"featured":                  actorURL + "/featured",
		"followers":                 actorURL + "/followers",
		"following":                 actorURL + "/following",
		"published":                 activityPubTime(publisher.CreatedAt),
		"url":                       s.baseURL + "/publishers/" + publisher.Name,
		"manuallyApprovesFollowers": publisher.IsLocked,
		"discoverable":              publisher.IsDiscoverable,
		"bot":                       publisher.IsBot,
		"publicKey": map[string]any{
			"id":           actorURL + "#main-key",
			"owner":        actorURL,
			"publicKeyPem": publicKey,
		},
	}
	if icon := fileReferenceURL(publisher.Picture, s.fileBase); icon != nil {
		document["icon"] = map[string]any{
			"type":      "Image",
			"mediaType": fileReferenceMime(publisher.Picture),
			"url":       *icon,
		}
	}
	if image := fileReferenceURL(publisher.Background, s.fileBase); image != nil {
		document["image"] = map[string]any{
			"type":      "Image",
			"mediaType": fileReferenceMime(publisher.Background),
			"url":       *image,
		}
	}
	return document, nil
}

// postObject renders the object document for a local post.
func (s *server) postObject(ctx context.Context, post *store.PostEntity, actorURL string) (map[string]any, error) {
	objectURL := s.objectURL(post.ID)
	objectType := "Note"
	if post.Type == int(model.PostTypeArticle) || post.Type == int(model.PostTypeBlog) {
		objectType = "Article"
	}

	var builder strings.Builder
	if post.Title != nil && strings.TrimSpace(*post.Title) != "" {
		builder.WriteString("# ")
		builder.WriteString(*post.Title)
		builder.WriteString("\n\n")
	}
	if post.Description != nil && strings.TrimSpace(*post.Description) != "" {
		builder.WriteString(*post.Description)
		builder.WriteString("\n\n")
	}
	if post.Content != nil && *post.Content != "" {
		builder.WriteString(*post.Content)
	}
	if builder.Len() == 0 {
		builder.WriteString("Shared media")
	}
	if tags := s.postTagSlugs(ctx, post.ID); len(tags) > 0 {
		builder.WriteString("\n\n")
		for i, tag := range tags {
			if i > 0 {
				builder.WriteString(" ")
			}
			builder.WriteString("#" + tag)
		}
	}

	published := timeValue(post.PublishedAt)
	if published.IsZero() {
		published = post.CreatedAt
	}
	document := map[string]any{
		"id":           objectURL,
		"type":         objectType,
		"published":    activityPubTime(published),
		"attributedTo": actorURL,
		"content":      builder.String(),
		"to":           []any{publicCollection},
		"cc":           []any{actorURL + "/followers"},
		"attachment":   s.attachmentObjects(post),
	}

	if post.RepliedPostID != nil {
		var replied store.PostEntity
		if err := s.db.WithContext(ctx).Where("id = ?", *post.RepliedPostID).First(&replied).Error; err == nil {
			if replied.FediverseURI != nil && *replied.FediverseURI != "" {
				document["inReplyTo"] = *replied.FediverseURI
			} else {
				document["inReplyTo"] = s.objectURL(replied.ID)
			}
		}
	}
	if post.ForwardedPostID != nil {
		var forwarded store.PostEntity
		if err := s.db.WithContext(ctx).Where("id = ?", *post.ForwardedPostID).First(&forwarded).Error; err == nil {
			quote := s.objectURL(forwarded.ID)
			if forwarded.FediverseURI != nil && *forwarded.FediverseURI != "" {
				quote = *forwarded.FediverseURI
			}
			document["quote"] = quote
			document["@context"] = []any{
				"https://www.w3.org/ns/activitystreams",
				map[string]any{
					"gts":      "https://gotosocial.org/ns#",
					"canQuote": "https://gotosocial.org/ns#canQuote",
				},
			}
			document["interactionPolicy"] = map[string]any{
				"canQuote": map[string]any{"automaticApproval": publicCollection},
			}
		}
	}
	if post.QuoteAuthorizationID != nil {
		var authorization store.QuoteAuthorizationEntity
		if err := s.db.WithContext(ctx).
			Where("id = ? AND is_valid = ?", *post.QuoteAuthorizationID, true).
			First(&authorization).Error; err == nil {
			document["quoteAuthorization"] = s.baseURL + "/quote-authorizations/" + authorization.ID.String()
		}
	}
	if post.EditedAt != nil {
		document["updated"] = activityPubTime(*post.EditedAt)
	}
	return document, nil
}

func (s *server) postTagSlugs(ctx context.Context, postID uuid.UUID) []string {
	var slugs []string
	err := s.db.WithContext(ctx).
		Table("post_tags").
		Select("post_tags.slug").
		Joins("JOIN post_tag_links ON post_tag_links.tags_id = post_tags.id").
		Where("post_tag_links.post_id = ? AND post_tags.deleted_at IS NULL", postID).
		Scan(&slugs).Error
	if err != nil {
		return nil
	}
	return slugs
}

func (s *server) attachmentObjects(post *store.PostEntity) []any {
	result := make([]any, 0)
	if len(post.Attachments) == 0 {
		return result
	}
	var attachments []model.CloudFileReferenceObject
	store.JSONArray(post.Attachments, &attachments)
	for _, attachment := range attachments {
		mediaType := attachment.MimeType
		if mediaType == "" {
			mediaType = "application/octet-stream"
		}
		typeName := "Document"
		switch {
		case strings.HasPrefix(mediaType, "image/"):
			typeName = "Image"
		case strings.HasPrefix(mediaType, "video/"):
			typeName = "Video"
		case strings.HasPrefix(mediaType, "audio/"):
			typeName = "Audio"
		}
		target := attachment.Url
		if target == "" {
			target = strings.TrimRight(s.fileBase, "/") + "/" + attachment.Id
		}
		entry := map[string]any{
			"type":      typeName,
			"mediaType": mediaType,
			"url":       target,
		}
		if attachment.Width != nil {
			entry["width"] = *attachment.Width
		}
		if attachment.Height != nil {
			entry["height"] = *attachment.Height
		}
		if attachment.Size > 0 {
			entry["size"] = attachment.Size
		}
		if attachment.Blurhash != "" {
			entry["blurhash"] = attachment.Blurhash
		}
		result = append(result, entry)
	}
	return result
}

// ---- file reference helpers ----------------------------------------------

func decodeFileReference(raw []byte) (model.CloudFileReferenceObject, bool) {
	if len(raw) == 0 {
		return model.CloudFileReferenceObject{}, false
	}
	var reference model.CloudFileReferenceObject
	if err := json.Unmarshal(raw, &reference); err != nil {
		return model.CloudFileReferenceObject{}, false
	}
	if reference.Id == "" && reference.Url == "" {
		return model.CloudFileReferenceObject{}, false
	}
	return reference, true
}

func fileReferenceURL(raw []byte, base string) *string {
	reference, ok := decodeFileReference(raw)
	if !ok {
		return nil
	}
	value := reference.Url
	if value == "" {
		value = strings.TrimRight(base, "/") + "/" + reference.Id
	}
	return &value
}

func fileReferenceMime(raw []byte) string {
	if reference, ok := decodeFileReference(raw); ok && reference.MimeType != "" {
		return reference.MimeType
	}
	return "image/png"
}

// ---- collections ----------------------------------------------------------

func activityCollection(id, collectionType string, total int, first string, items []any) map[string]any {
	return map[string]any{
		"@context":     []any{"https://www.w3.org/ns/activitystreams"},
		"id":           id,
		"type":         collectionType,
		"totalItems":   total,
		"first":        first,
		"orderedItems": items,
	}
}

func activityCollectionPage(id, partOf string, total int, items []any, next, prev string) map[string]any {
	page := map[string]any{
		"@context":     []any{"https://www.w3.org/ns/activitystreams"},
		"id":           id,
		"type":         "OrderedCollectionPage",
		"totalItems":   total,
		"partOf":       partOf,
		"orderedItems": items,
		"next":         nil,
		"prev":         nil,
	}
	if next != "" {
		page["next"] = next
	}
	if prev != "" {
		page["prev"] = prev
	}
	return page
}

// loadPublisher resolves a local publisher by name, case-insensitively.
func (s *server) loadPublisher(ctx context.Context, username string) (*store.PublisherEntity, error) {
	var publisher store.PublisherEntity
	err := s.db.WithContext(ctx).Where("lower(name) = ?", strings.ToLower(username)).First(&publisher).Error
	if err != nil {
		return nil, err
	}
	return &publisher, nil
}

// localActor returns the publisher row when it is a federated actor.
func (s *server) localActor(ctx context.Context, publisherID uuid.UUID) (*store.PublisherEntity, error) {
	var publisher store.PublisherEntity
	err := s.db.WithContext(ctx).Where("id = ? AND uri IS NOT NULL", publisherID).First(&publisher).Error
	if err != nil {
		return nil, err
	}
	return &publisher, nil
}

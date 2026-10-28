package posts

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

type grpcService struct {
	gen.UnimplementedDyPostServiceServer
	h *handler
}

func (s *grpcService) GetPost(ctx context.Context, request *gen.DyGetPostRequest) (*gen.DyPost, error) {
	query := anonymousVisible(s.h.db.WithContext(ctx).Model(&store.PostEntity{}).Where("drafted_at IS NULL"))
	switch identifier := request.Identifier.(type) {
	case *gen.DyGetPostRequest_Id:
		id, err := uuid.Parse(identifier.Id)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid post id")
		}
		query = query.Where("id = ?", id)
	case *gen.DyGetPostRequest_Slug:
		query = query.Where("lower(slug) = ?", strings.ToLower(identifier.Slug))
	default:
		return nil, status.Error(codes.InvalidArgument, "invalid identifier case")
	}
	if request.PublisherId != nil && request.PublisherId.Value != "" {
		if id, err := uuid.Parse(request.PublisherId.Value); err == nil {
			query = query.Where("publisher_id = ?", id)
		}
	}
	var ent store.PostEntity
	if err := query.First(&ent).Error; err != nil {
		return nil, status.Error(codes.NotFound, "post not found")
	}
	post, err := s.h.hydrateOne(ctx, &ent, nil, false, false)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return postToProto(&post), nil
}

func (s *grpcService) GetPostBatch(ctx context.Context, request *gen.DyGetPostBatchRequest) (*gen.DyGetPostBatchResponse, error) {
	ids := make([]uuid.UUID, 0, len(request.Ids))
	for _, raw := range request.Ids {
		if id, err := uuid.Parse(raw); err == nil {
			ids = append(ids, id)
		}
	}
	response := &gen.DyGetPostBatchResponse{}
	if len(ids) == 0 {
		return response, nil
	}
	var rows []store.PostEntity
	if err := anonymousVisible(s.h.db.WithContext(ctx).Model(&store.PostEntity{}).
		Where("drafted_at IS NULL")).Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	posts, err := s.h.hydrate(ctx, rows, nil, true, false)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	for i := range posts {
		response.Posts = append(response.Posts, postToProto(&posts[i]))
	}
	return response, nil
}

func (s *grpcService) SearchPosts(ctx context.Context, request *gen.DySearchPostsRequest) (*gen.DySearchPostsResponse, error) {
	query := anonymousVisible(s.h.db.WithContext(ctx).Model(&store.PostEntity{}).Where("drafted_at IS NULL"))
	if request.PublisherId != "" {
		if id, err := uuid.Parse(request.PublisherId); err == nil {
			query = query.Where("publisher_id = ?", id)
		}
	}
	if request.RealmId != "" {
		if id, err := uuid.Parse(request.RealmId); err == nil {
			query = query.Where("realm_id = ?", id)
		}
	}
	if strings.TrimSpace(request.Query) != "" {
		pattern := "%" + request.Query + "%"
		query = query.Where("(title ILIKE ? OR content ILIKE ? OR description ILIKE ?)", pattern, pattern, pattern)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	offset, pageSize := grpcPage(request.PageToken, request.PageSize)
	order := strings.TrimSpace(request.OrderBy)
	if order == "popularity" {
		query = query.Order("(upvotes * 10 - downvotes * 10 + awarded_score) DESC")
	} else {
		query = query.Order("COALESCE(published_at, created_at) DESC")
	}
	var rows []store.PostEntity
	if err := query.Offset(offset).Limit(pageSize).Find(&rows).Error; err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	posts, err := s.h.hydrate(ctx, rows, nil, true, false)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	response := &gen.DySearchPostsResponse{TotalSize: int32(total)}
	for i := range posts {
		response.Posts = append(response.Posts, postToProto(&posts[i]))
	}
	response.NextPageToken = nextPageToken(offset, pageSize, total)
	return response, nil
}

func (s *grpcService) ListPosts(ctx context.Context, request *gen.DyListPostsRequest) (*gen.DyListPostsResponse, error) {
	query := anonymousVisible(s.h.db.WithContext(ctx).Model(&store.PostEntity{}).Where("drafted_at IS NULL"))
	if request.PublisherId != nil && request.PublisherId.Value != "" {
		if id, err := uuid.Parse(request.PublisherId.Value); err == nil {
			query = query.Where("publisher_id = ?", id)
		}
	}
	if request.RealmId != nil && request.RealmId.Value != "" {
		if id, err := uuid.Parse(request.RealmId.Value); err == nil {
			query = query.Where("realm_id = ?", id)
		}
	}
	if len(request.Categories) > 0 {
		query = query.Where("id IN (SELECT posts_id FROM post_category_links l JOIN post_categories c ON c.id = l.categories_id WHERE lower(c.slug) IN ? AND c.deleted_at IS NULL)", lowerAll(request.Categories))
	}
	if len(request.Tags) > 0 {
		query = query.Where("id IN (SELECT posts_id FROM post_tag_links l JOIN post_tags t ON t.id = l.tags_id WHERE lower(t.slug) IN ? AND t.deleted_at IS NULL)", lowerAll(request.Tags))
	}
	if len(request.Types) > 0 {
		types := make([]int, 0, len(request.Types))
		for _, typ := range request.Types {
			switch typ {
			case gen.DyPostType_DY_ARTICLE:
				types = append(types, int(model.PostTypeArticle))
			case gen.DyPostType_DY_BLOG:
				types = append(types, int(model.PostTypeBlog))
			default:
				types = append(types, int(model.PostTypeMoment))
			}
		}
		query = query.Where("type IN ?", types)
	}
	if request.OnlyMedia {
		query = query.Where("COALESCE(jsonb_array_length(attachments), 0) > 0")
	}
	if request.Pinned != nil {
		switch *request.Pinned {
		case gen.DyPostPinMode_DY_REALM_PAGE:
			query = query.Where("pin_mode = ?", int(model.PinModeRealmPage))
		case gen.DyPostPinMode_DY_PUBLISHER_PAGE:
			query = query.Where("pin_mode = ?", int(model.PinModePublisherPage))
		case gen.DyPostPinMode_DY_REPLY_PAGE:
			query = query.Where("pin_mode = ?", int(model.PinModeReplyPage))
		}
	}
	if !request.IncludeReplies {
		query = query.Where("replied_post_id IS NULL").Where("chained_post_id IS NULL")
	}
	if request.After != nil {
		after := request.After.AsTime()
		query = query.Where("(created_at >= ? OR published_at >= ?)", after, after)
	}
	if request.Before != nil {
		before := request.Before.AsTime()
		query = query.Where("(created_at <= ? OR published_at <= ?)", before, before)
	}
	if request.Query != nil && strings.TrimSpace(request.Query.Value) != "" {
		pattern := "%" + request.Query.Value + "%"
		query = query.Where("(title ILIKE ? OR content ILIKE ? OR description ILIKE ?)", pattern, pattern, pattern)
	}
	if request.Shuffle {
		query = query.Order("random()")
	} else {
		order := ""
		if request.OrderBy != nil {
			order = request.OrderBy.Value
		}
		if order == "popularity" {
			if request.OrderDesc {
				query = query.Order("(upvotes * 10 - downvotes * 10 + awarded_score) DESC")
			} else {
				query = query.Order("(upvotes * 10 - downvotes * 10 + awarded_score) ASC")
			}
		} else if request.OrderDesc {
			query = query.Order("COALESCE(published_at, created_at) DESC")
		} else {
			query = query.Order("COALESCE(published_at, created_at) ASC")
		}
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	offset, pageSize := grpcPage(request.PageToken, request.PageSize)
	var rows []store.PostEntity
	if err := query.Offset(offset).Limit(pageSize).Find(&rows).Error; err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	posts, err := s.h.hydrate(ctx, rows, nil, true, false)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	response := &gen.DyListPostsResponse{TotalSize: int32(total)}
	for i := range posts {
		response.Posts = append(response.Posts, postToProto(&posts[i]))
	}
	response.NextPageToken = nextPageToken(offset, pageSize, total)
	return response, nil
}

// anonymousVisible mirrors FilterWithVisibility(null, [], []): only published
// public posts are reachable over the fleet gRPC surface.
func anonymousVisible(query *gorm.DB) *gorm.DB {
	return query.
		Where("published_at IS NOT NULL AND published_at <= ?", time.Now().UTC()).
		Where("visibility = ?", int(model.VisibilityPublic))
}

func grpcPage(token string, size int32) (int, int) {
	pageSize := int(size)
	if pageSize <= 0 {
		pageSize = 20
	}
	offset := 0
	if token != "" {
		if parsed, err := strconv.Atoi(token); err == nil && parsed > 0 {
			offset = parsed
		}
	}
	return offset, pageSize
}

func nextPageToken(offset, pageSize int, total int64) string {
	if int64(offset+pageSize) < total {
		return strconv.Itoa(offset + pageSize)
	}
	return ""
}

// ---------------------------------------------------------------------------
// Proto mapping
// ---------------------------------------------------------------------------

func postToProto(post *model.Post) *gen.DyPost { return postToProtoDepth(post, 0) }

func postToProtoDepth(post *model.Post, depth int) *gen.DyPost {
	proto := &gen.DyPost{
		Id:             post.ID,
		Title:          stringValue(post.Title),
		Description:    stringValue(post.Description),
		Slug:           stringValue(post.Slug),
		EditedAt:       timestampOf(post.EditedAt),
		PublishedAt:    timestampOf(post.PublishedAt),
		Visibility:     gen.DyPostVisibility(post.Visibility + 1),
		Type:           gen.DyPostType(post.Type + 1),
		ViewsUnique:    int32(post.ViewsUnique),
		ViewsTotal:     int32(post.ViewsTotal),
		Upvotes:        int32(post.Upvotes),
		Downvotes:      int32(post.Downvotes),
		AwardedScore:   post.AwardedScore,
		ReactionsCount: map[string]int32{},
		ReactionsMade:  map[string]bool{},
		RepliesCount:   int32(post.RepliesCount),
		ThreadRepliesCount: int32(post.ThreadRepliesCount),
		RepliedGone:    post.RepliedGone,
		ForwardedGone:  post.ForwardedGone,
		RepliedPostId:  post.RepliedPostID,
		ForwardedPostId: post.ForwardedPostID,
		RealmId:        post.RealmID,
		PublisherId:    post.PublisherID,
		FediverseUri:   post.FediverseURI,
		Language:       post.Language,
		BoostCount:     int32(post.BoostCount),
		CreatedAt:      timestampOf(post.CreatedAt),
		UpdatedAt:      timestampOf(post.UpdatedAt),
		DeletedAt:      timestampOf(post.DeletedAt),
	}
	if post.Content != nil {
		content := *post.Content
		proto.Content = &content
	}
	if post.PinMode != nil {
		mode := gen.DyPostPinMode(*post.PinMode + 1)
		proto.PinMode = &mode
	}
	if post.ContentType >= 0 {
		contentType := gen.DyPostContentType(post.ContentType + 1)
		proto.ContentType = &contentType
	}
	if post.FediverseType != nil {
		fediverseType := gen.DyFediverseContentType(*post.FediverseType + 1)
		proto.FediverseType = &fediverseType
	}
	if post.Meta != nil {
		if raw, err := json.Marshal(post.Meta); err == nil {
			proto.Meta = raw
		}
	}
	if post.SensitiveMarks != nil {
		if raw, err := json.Marshal(post.SensitiveMarks); err == nil {
			proto.SensitiveMarks = raw
		}
	}
	if post.EmbedView != nil {
		proto.EmbedView = embedViewToProto(post.EmbedView)
	}
	for key, value := range post.ReactionsCount {
		proto.ReactionsCount[key] = int32(value)
	}
	for key, value := range post.ReactionsMade {
		proto.ReactionsMade[key] = value
	}
	if post.Publisher != nil {
		proto.Publisher = publisherToProto(post.Publisher)
	}
	if post.RepliedPost != nil && depth < 8 {
		proto.RepliedPost = postToProtoDepth(post.RepliedPost, depth+1)
	}
	if post.ForwardedPost != nil && depth < 8 {
		proto.ForwardedPost = postToProtoDepth(post.ForwardedPost, depth+1)
	}
	for _, attachment := range post.Attachments {
		proto.Attachments = append(proto.Attachments, fileToProto(&attachment))
	}
	for _, mention := range post.Mentions {
		proto.Mentions = append(proto.Mentions, &gen.DyContentMention{
			Username: mention.Username,
			Url:      mention.URL,
			ActorUri: mention.ActorURI,
		})
	}
	for _, tag := range post.Tags {
		mapped := &gen.DyPostTag{
			Id:               tag.ID,
			Slug:             tag.Slug,
			Name:             stringValue(tag.Name),
			Description:      tag.Description,
			OwnerPublisherId: tag.OwnerPublisherID,
			IsProtected:      tag.IsProtected,
			IsEvent:          tag.IsEvent,
			EventEndsAt:      timestampOf(tag.EventEndsAt),
			CreatedAt:        timestampOf(tag.CreatedAt),
			UpdatedAt:        timestampOf(tag.UpdatedAt),
		}
		proto.Tags = append(proto.Tags, mapped)
	}
	for _, category := range post.Categories {
		proto.Categories = append(proto.Categories, &gen.DyPostCategory{
			Id:        category.ID,
			Slug:      category.Slug,
			Name:      stringValue(category.Name),
			CreatedAt: timestampOf(category.CreatedAt),
			UpdatedAt: timestampOf(category.UpdatedAt),
		})
	}
	for _, award := range post.Awards {
		mapped := &gen.DyPostAward{
			Id:        award.ID,
			Amount:    award.Amount,
			Attitude:  gen.DyPostReactionAttitude(award.Attitude + 1),
			PostId:    award.PostID,
			AccountId: award.AccountID,
			CreatedAt: timestampOf(award.CreatedAt),
			UpdatedAt: timestampOf(award.UpdatedAt),
		}
		if award.Message != nil {
			mapped.Message = wrapperspb.String(*award.Message)
		}
		proto.Awards = append(proto.Awards, mapped)
	}
	for _, record := range post.FeaturedRecords {
		proto.FeaturedRecords = append(proto.FeaturedRecords, &gen.DyPostFeaturedRecord{
			Id:            record.ID,
			PostId:        record.PostID,
			FeaturedAt:    timestampOf(record.FeaturedAt),
			SocialCredits: int32(record.SocialCredits),
			CreatedAt:     timestampOf(record.CreatedAt),
			UpdatedAt:     timestampOf(record.UpdatedAt),
		})
	}
	return proto
}

func publisherToProto(publisher *model.Publisher) *gen.DyPublisher {
	mapped := &gen.DyPublisher{
		Id:        publisher.ID,
		Type:      gen.DyPublisherType(publisher.Type + 1),
		Name:      publisher.Name,
		Nick:      publisher.Nick,
		Rating:    publisher.Rating,
		CreatedAt: timestampOf(publisher.CreatedAt),
		UpdatedAt: timestampOf(publisher.UpdatedAt),
	}
	if publisher.Bio != nil {
		mapped.Bio = wrapperspb.String(*publisher.Bio)
	}
	if publisher.AccountID != nil {
		mapped.AccountId = *publisher.AccountID
	}
	if publisher.RealmID != nil {
		realmID := *publisher.RealmID
		mapped.RealmId = &realmID
	}
	return mapped
}

func fileToProto(file *model.CloudFileReferenceObject) *gen.DyCloudFile {
	return &gen.DyCloudFile{
		Id:         file.Id,
		Name:       file.Name,
		MimeType:   file.MimeType,
		Hash:       file.Hash,
		Size:       file.Size,
		Url:        file.Url,
		Width:      file.Width,
		Height:     file.Height,
		Blurhash:   stringPtrOrNil(file.Blurhash),
		CreatedAt:  timestampOf(file.CreatedAt),
		UpdatedAt:  timestampOf(file.UpdatedAt),
		UploadedAt: timestampOf(file.CreatedAt),
	}
}

func embedViewToProto(value any) *gen.DyPostEmbedView {
	mapped := &gen.DyPostEmbedView{}
	raw, err := json.Marshal(value)
	if err != nil {
		return mapped
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return mapped
	}
	if uri, ok := decoded["uri"].(string); ok {
		mapped.Uri = uri
	} else if uri, ok := decoded["Uri"].(string); ok {
		mapped.Uri = uri
	}
	if ratio, ok := decoded["aspect_ratio"].(float64); ok {
		mapped.AspectRatio = &ratio
	} else if ratio, ok := decoded["aspectRatio"].(float64); ok {
		mapped.AspectRatio = &ratio
	}
	if renderer, ok := decoded["renderer"].(float64); ok {
		mapped.Renderer = gen.DyPostEmbedViewRenderer(int(renderer) + 1)
	}
	return mapped
}

func timestampOf(value *model.Time) *timestamppb.Timestamp {
	if value == nil {
		return nil
	}
	return timestamppb.New(value.Time().UTC())
}

func stringPtrOrNil(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}


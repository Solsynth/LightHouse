package publishers

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/lighthouse/internal/platform/filejson"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/core"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

type publisherServer struct {
	gen.UnimplementedDyPublisherServiceServer
	core  *core.Services
	store *store.Store
}

type ratingServer struct {
	gen.UnimplementedDyPublisherRatingServiceServer
	core  *core.Services
	store *store.Store
}

func (s *publisherServer) GetPublisher(ctx context.Context, request *gen.DyGetPublisherRequest) (*gen.DyGetPublisherResponse, error) {
	var publisher *store.PublisherEntity
	switch {
	case request.GetId() != "":
		id, err := uuid.Parse(request.GetId())
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid publisher id")
		}
		entity, err := s.store.GetPublisher(ctx, id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil, status.Error(codes.NotFound, "Publisher not found")
			}
			return nil, status.Error(codes.Internal, err.Error())
		}
		publisher = entity
	case request.GetName() != "":
		entity, err := s.store.PublisherByNameCI(ctx, request.GetName())
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil, status.Error(codes.NotFound, "Publisher not found")
			}
			return nil, status.Error(codes.Internal, err.Error())
		}
		publisher = entity
	default:
		return nil, status.Error(codes.InvalidArgument, "query is required")
	}
	return &gen.DyGetPublisherResponse{Publisher: publisherToProto(publisher)}, nil
}

func (s *publisherServer) GetPublisherBatch(ctx context.Context, request *gen.DyGetPublisherBatchRequest) (*gen.DyListPublishersResponse, error) {
	ids := make([]uuid.UUID, 0, len(request.GetIds()))
	for _, raw := range request.GetIds() {
		id, err := uuid.Parse(raw)
		if err == nil {
			ids = append(ids, id)
		}
	}
	response := &gen.DyListPublishersResponse{}
	if len(ids) == 0 {
		return response, nil
	}
	var entities []store.PublisherEntity
	if err := s.store.DB.WithContext(ctx).Where("id IN ?", ids).Find(&entities).Error; err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	for index := range entities {
		response.Publishers = append(response.Publishers, publisherToProto(&entities[index]))
	}
	return response, nil
}

func (s *publisherServer) ListPublishers(ctx context.Context, request *gen.DyListPublishersRequest) (*gen.DyListPublishersResponse, error) {
	query := s.store.DB.WithContext(ctx).Model(&store.PublisherEntity{}).
		Where("type <> ?", publisherFediverse)
	if request.GetAccountId() != "" {
		accountID, err := uuid.Parse(request.GetAccountId())
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid account_id")
		}
		var ids []uuid.UUID
		if err := s.store.DB.WithContext(ctx).Model(&store.PublisherMemberRow{}).
			Where("account_id = ?", accountID).
			Where("deleted_at IS NULL").
			Pluck("publisher_id", &ids).Error; err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
		if len(ids) == 0 {
			return &gen.DyListPublishersResponse{}, nil
		}
		query = query.Where("id IN ?", ids)
	}
	if request.GetRealmId() != "" {
		realmID, err := uuid.Parse(request.GetRealmId())
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid realm_id")
		}
		query = query.Where("realm_id = ?", realmID)
	}
	var entities []store.PublisherEntity
	if err := query.Find(&entities).Error; err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	response := &gen.DyListPublishersResponse{}
	for index := range entities {
		response.Publishers = append(response.Publishers, publisherToProto(&entities[index]))
	}
	return response, nil
}

func (s *publisherServer) ListPublisherMembers(ctx context.Context, request *gen.DyListPublisherMembersRequest) (*gen.DyListPublisherMembersResponse, error) {
	publisherID, err := uuid.Parse(request.GetPublisherId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid publisher_id")
	}
	rows, _, err := s.store.PublisherMemberRows(ctx, publisherID, true, 0, 10000)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	response := &gen.DyListPublisherMembersResponse{}
	for _, row := range rows {
		member := &gen.DyPublisherMember{
			PublisherId: publisherID.String(),
			AccountId:   row.AccountID.String(),
			Role:        gen.DyPublisherMemberRole(row.Role),
		}
		if row.JoinedAt != nil {
			member.JoinedAt = timestamppb.New(*row.JoinedAt)
		}
		response.Members = append(response.Members, member)
	}
	return response, nil
}

func (s *publisherServer) SetPublisherFeatureFlag(ctx context.Context, request *gen.DySetPublisherFeatureFlagRequest) (*wrapperspb.StringValue, error) {
	publisherID, err := uuid.Parse(request.GetPublisherId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid publisher_id")
	}
	var entity store.PublisherFeatureEntity
	err = s.store.DB.WithContext(ctx).
		Where("publisher_id = ? AND flag = ?", publisherID, request.GetFlag()).
		First(&entity).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		entity = store.PublisherFeatureEntity{
			ID:          uuid.New(),
			PublisherID: publisherID,
			Flag:        request.GetFlag(),
		}
		if err := s.store.DB.WithContext(ctx).Create(&entity).Error; err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
	} else if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	} else {
		now := time.Now().UTC()
		entity.ExpiredAt = &now
		if err := s.store.DB.WithContext(ctx).Save(&entity).Error; err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
	}
	return wrapperspb.String(request.GetFlag()), nil
}

func (s *publisherServer) HasPublisherFeature(ctx context.Context, request *gen.DyHasPublisherFeatureRequest) (*gen.DyHasPublisherFeatureResponse, error) {
	publisherID, err := uuid.Parse(request.GetPublisherId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid publisher_id")
	}
	var count int64
	if err := s.store.DB.WithContext(ctx).Model(&store.PublisherFeatureEntity{}).
		Where("publisher_id = ? AND flag = ?", publisherID, request.GetFlag()).
		Where("expired_at IS NULL OR expired_at > ?", time.Now().UTC()).
		Count(&count).Error; err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &gen.DyHasPublisherFeatureResponse{Enabled: count > 0}, nil
}

func (s *publisherServer) IsPublisherMember(ctx context.Context, request *gen.DyIsPublisherMemberRequest) (*gen.DyIsPublisherMemberResponse, error) {
	publisherID, err := uuid.Parse(request.GetPublisherId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid publisher_id")
	}
	accountID, err := uuid.Parse(request.GetAccountId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid account_id")
	}
	required := roleViewer
	switch request.GetRole() {
	case gen.DyPublisherMemberRole_DY_OWNER:
		required = roleOwner
	case gen.DyPublisherMemberRole_DY_MANAGER:
		required = roleManager
	case gen.DyPublisherMemberRole_DY_EDITOR:
		required = roleEditor
	}
	role, ok, err := s.store.PublisherMemberRoleFor(ctx, publisherID, accountID)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &gen.DyIsPublisherMemberResponse{Valid: ok && role >= required}, nil
}

func (s *publisherServer) RenamePublisher(ctx context.Context, request *gen.DyRenamePublisherRequest) (*gen.DyRenamePublisherResponse, error) {
	publisherID, err := uuid.Parse(request.GetPublisherId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid publisher or account id")
	}
	accountID, err := uuid.Parse(request.GetAccountId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid publisher or account id")
	}
	publisher, err := s.store.GetPublisher(ctx, publisherID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "Publisher not found")
		}
		return nil, status.Error(codes.Internal, err.Error())
	}
	role, ok, err := s.store.PublisherMemberRoleFor(ctx, publisherID, accountID)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if !ok || role < roleManager {
		return nil, status.Error(codes.PermissionDenied, "You need at least be the manager to rename the publisher.")
	}
	newName := strings.TrimSpace(request.GetNewName())
	if newName == "" || len([]rune(newName)) > 256 {
		return nil, status.Error(codes.InvalidArgument, "Invalid publisher name.")
	}
	if !strings.EqualFold(newName, publisher.Name) {
		taken, err := s.store.PublisherNamesTaken(ctx, newName, publisher.ID)
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
		if taken {
			return nil, status.Error(codes.AlreadyExists, "The name you requested has already been taken.")
		}
	}
	publisher.Name = newName
	if err := s.store.DB.WithContext(ctx).Save(publisher).Error; err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &gen.DyRenamePublisherResponse{Publisher: publisherToProto(publisher)}, nil
}

func publisherToProto(entity *store.PublisherEntity) *gen.DyPublisher {
	if entity == nil {
		return nil
	}
	publisher := &gen.DyPublisher{
		Id:        entity.ID.String(),
		Type:      gen.DyPublisherType(entity.Type + 1),
		Name:      entity.Name,
		Nick:      entity.Nick,
		Rating:    entity.Rating,
		CreatedAt: timestamppb.New(entity.CreatedAt),
		UpdatedAt: timestamppb.New(entity.UpdatedAt),
	}
	if entity.Bio != nil {
		publisher.Bio = wrapperspb.String(*entity.Bio)
	}
	if entity.AccountID != nil {
		publisher.AccountId = entity.AccountID.String()
	}
	if entity.RealmID != nil {
		realmID := entity.RealmID.String()
		publisher.RealmId = &realmID
	}
	if entity.Picture != nil {
		publisher.Picture = cloudFileProto(entity.Picture)
	}
	if entity.Background != nil {
		publisher.Background = cloudFileProto(entity.Background)
	}
	if len(entity.Verification) > 0 {
		var mark model.VerificationMark
		if err := json.Unmarshal(entity.Verification, &mark); err == nil {
			publisher.VerificationMark = &gen.DyVerificationMark{
				Type:        gen.DyVerificationMarkType(mark.Type + 1),
				Title:       stringValue(mark.Title),
				Description: stringValue(mark.Description),
				VerifiedBy:  stringValue(mark.VerifiedBy),
			}
		}
	}
	return publisher
}

func cloudFileProto(raw datatypes.JSON) *gen.DyCloudFile {
	reference := filejson.Reference(raw)
	if reference == nil {
		return nil
	}
	file := &gen.DyCloudFile{
		Id:             reference.Id,
		Name:           reference.Name,
		MimeType:       reference.MimeType,
		Hash:           reference.Hash,
		Size:           reference.Size,
		HasCompression: reference.HasCompression,
		Url:            reference.Url,
	}
	if reference.Width != nil {
		file.Width = reference.Width
	}
	if reference.Height != nil {
		file.Height = reference.Height
	}
	if reference.Blurhash != "" {
		value := reference.Blurhash
		file.Blurhash = &value
	}
	if reference.Usage != "" {
		value := reference.Usage
		file.Usage = &value
	}
	if reference.ApplicationType != "" {
		value := reference.ApplicationType
		file.ApplicationType = &value
	}
	if raw, err := json.Marshal(reference.FileMeta); err == nil {
		file.FileMeta = raw
	}
	if raw, err := json.Marshal(reference.UserMeta); err == nil {
		file.UserMeta = raw
	}
	return file
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (s *ratingServer) AddRecord(ctx context.Context, request *gen.DyAddPublisherRatingRecordRequest) (*gen.DyPublisherRatingRecord, error) {
	publisherID, err := uuid.Parse(request.GetPublisherId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid publisher_id")
	}
	entity := &store.PublisherRatingRecordEntity{
		ID:          uuid.New(),
		ReasonType:  request.GetReasonType(),
		Reason:      request.GetReason(),
		Delta:       request.GetDelta(),
		PublisherID: publisherID,
	}
	if err := s.store.DB.WithContext(ctx).Create(entity).Error; err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	total, err := s.store.PublisherRatingTotal(ctx, publisherID)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if err := s.store.SetPublisherRating(ctx, publisherID, total+baseRating); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &gen.DyPublisherRatingRecord{
		Id:          entity.ID.String(),
		ReasonType:  entity.ReasonType,
		Reason:      entity.Reason,
		Delta:       entity.Delta,
		PublisherId: publisherID.String(),
		CreatedAt:   timestamppb.New(entity.CreatedAt),
		UpdatedAt:   timestamppb.New(entity.UpdatedAt),
	}, nil
}

func (s *ratingServer) GetRating(ctx context.Context, request *gen.DyGetPublisherRatingRequest) (*gen.DyPublisherRatingResponse, error) {
	publisherID, err := uuid.Parse(request.GetPublisherId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid publisher_id")
	}
	total, err := s.store.PublisherRatingTotal(ctx, publisherID)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &gen.DyPublisherRatingResponse{Amount: total + baseRating}, nil
}

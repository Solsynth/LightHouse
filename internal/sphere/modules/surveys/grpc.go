package surveys

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"
	gen "src.solsynth.dev/sosys/go/proto"

	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

type surveyServer struct {
	gen.UnimplementedDySurveyServiceServer
	svc *service
}

func (s *surveyServer) GetSurvey(ctx context.Context, request *gen.DyGetSurveyRequest) (*gen.DySurvey, error) {
	id, err := uuid.Parse(request.GetId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid survey id")
	}
	survey, err := s.svc.loadSurvey(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "survey not found")
		}
		return nil, status.Error(codes.Internal, err.Error())
	}
	questions, err := s.svc.loadQuestions(ctx, id)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	publisher, _ := s.svc.loadPublisher(ctx, survey.PublisherID)
	return surveyToProto(survey, questions, publisher), nil
}

func (s *surveyServer) GetSurveyBatch(ctx context.Context, request *gen.DyGetSurveyBatchRequest) (*gen.DyGetSurveyBatchResponse, error) {
	ids := make([]uuid.UUID, 0, len(request.GetIds()))
	for _, raw := range request.GetIds() {
		if raw == "" {
			continue
		}
		id, err := uuid.Parse(raw)
		if err != nil {
			continue
		}
		ids = append(ids, id)
	}
	response := &gen.DyGetSurveyBatchResponse{}
	if len(ids) == 0 {
		return response, nil
	}
	var surveys []store.SurveyEntity
	if err := s.svc.db.WithContext(ctx).Where("id IN ?", ids).Find(&surveys).Error; err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	questionsBySurvey, err := s.svc.questionsFor(ctx, surveyIDs(surveys))
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	for i := range surveys {
		publisher, _ := s.svc.loadPublisher(ctx, surveys[i].PublisherID)
		response.Surveys = append(response.Surveys, surveyToProto(&surveys[i], questionsBySurvey[surveys[i].ID], publisher))
	}
	return response, nil
}

func (s *surveyServer) ListSurveys(ctx context.Context, request *gen.DyListSurveysRequest) (*gen.DyListSurveysResponse, error) {
	query := s.svc.db.WithContext(ctx).Model(&store.SurveyEntity{})
	if publisherID := request.GetPublisherId(); publisherID != nil && publisherID.GetValue() != "" {
		if id, err := uuid.Parse(publisherID.GetValue()); err == nil {
			query = query.Where("publisher_id = ?", id)
		}
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	pageSize := int(request.GetPageSize())
	if pageSize <= 0 {
		pageSize = 20
	}
	offset := 0
	if token := request.GetPageToken(); token != "" {
		parsed, err := strconv.Atoi(token)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid page token")
		}
		offset = parsed
	}

	direction := "ASC"
	if request.GetOrderDesc() {
		direction = "DESC"
	}
	orderBy := "created_at"
	switch request.GetOrderBy().GetValue() {
	case "title":
		orderBy = "COALESCE(title, '')"
	case "ended_at":
		orderBy = "ended_at"
	}
	query = query.Order(orderBy + " " + direction)

	var surveys []store.SurveyEntity
	if err := query.Offset(offset).Limit(pageSize).Find(&surveys).Error; err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	questionsBySurvey, err := s.svc.questionsFor(ctx, surveyIDs(surveys))
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	response := &gen.DyListSurveysResponse{TotalSize: int32(total)}
	for i := range surveys {
		publisher, _ := s.svc.loadPublisher(ctx, surveys[i].PublisherID)
		response.Surveys = append(response.Surveys, surveyToProto(&surveys[i], questionsBySurvey[surveys[i].ID], publisher))
	}
	if offset+pageSize < int(total) {
		response.NextPageToken = strconv.Itoa(offset + pageSize)
	}
	return response, nil
}

func (s *surveyServer) GetSurveyAnswer(ctx context.Context, request *gen.DyGetSurveyAnswerRequest) (*gen.DySurveyAnswer, error) {
	surveyID, err := uuid.Parse(request.GetSurveyId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid survey id")
	}
	accountID, err := uuid.Parse(request.GetAccountId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid account id")
	}
	answer, err := s.svc.getAnswer(ctx, surveyID, accountID)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if answer == nil {
		return nil, status.Error(codes.NotFound, "answer not found")
	}
	payload, _ := json.Marshal(answer.Answer)
	proto := &gen.DySurveyAnswer{
		Id:        answer.ID,
		Answer:    payload,
		AccountId: answer.AccountID,
		SurveyId:  answer.SurveyID,
	}
	if answer.CreatedAt != nil {
		proto.CreatedAt = timestamppb.New(answer.CreatedAt.Time())
	}
	if answer.UpdatedAt != nil {
		proto.UpdatedAt = timestamppb.New(answer.UpdatedAt.Time())
	}
	if answer.DeletedAt != nil {
		proto.DeletedAt = timestamppb.New(answer.DeletedAt.Time())
	}
	return proto, nil
}

func (s *surveyServer) GetSurveyStats(ctx context.Context, request *gen.DyGetSurveyStatsRequest) (*gen.DyGetSurveyStatsResponse, error) {
	surveyID, err := uuid.Parse(request.GetSurveyId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid survey id")
	}
	stats, err := s.svc.surveyStats(ctx, surveyID)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	response := &gen.DyGetSurveyStatsResponse{Stats: map[string]string{}}
	for questionID, value := range stats {
		encoded, err := json.Marshal(value)
		if err != nil {
			continue
		}
		response.Stats[questionID] = string(encoded)
	}
	return response, nil
}

func (s *surveyServer) GetSurveyQuestionStats(ctx context.Context, request *gen.DyGetSurveyQuestionStatsRequest) (*gen.DyGetSurveyQuestionStatsResponse, error) {
	questionID, err := uuid.Parse(request.GetQuestionId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid question id")
	}
	stats, err := s.svc.questionStats(ctx, questionID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "question not found")
		}
		return nil, status.Error(codes.Internal, err.Error())
	}
	response := &gen.DyGetSurveyQuestionStatsResponse{Stats: map[string]int32{}}
	for key, value := range stats {
		response.Stats[key] = int32(value)
	}
	return response, nil
}

func (s *service) loadPublisher(ctx context.Context, id uuid.UUID) (*store.PublisherEntity, error) {
	var publisher store.PublisherEntity
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&publisher).Error; err != nil {
		return nil, err
	}
	return &publisher, nil
}

func surveyToProto(survey *store.SurveyEntity, questions []store.SurveyQuestionEntity, publisher *store.PublisherEntity) *gen.DySurvey {
	proto := &gen.DySurvey{
		Id:                survey.ID.String(),
		Title:             stringValue(survey.Title),
		Description:       stringValue(survey.Description),
		EndedAt:           timestampValue(survey.EndedAt),
		IsAnonymous:       survey.IsAnonymous,
		PublisherId:       survey.PublisherID.String(),
		Publisher:         publisherToProto(publisher),
		CreatedAt:         timestamppb.New(survey.CreatedAt),
		UpdatedAt:         timestamppb.New(survey.UpdatedAt),
		Status:            gen.DySurveyStatus(survey.Status + 1),
		PublishedAt:       timestampValue(survey.PublishedAt),
		NotifySubscribers: survey.NotifySubscribers,
		HideResults:       survey.HideResults,
	}
	if survey.DeletedAt.Valid {
		proto.DeletedAt = timestamppb.New(survey.DeletedAt.Time)
	}
	var attachments []model.CloudFileReferenceObject
	store.JSONArray(survey.Attachments, &attachments)
	for _, attachment := range attachments {
		proto.Attachments = append(proto.Attachments, cloudFileToProto(&attachment))
	}
	for i := range questions {
		proto.Questions = append(proto.Questions, questionToProto(&questions[i]))
	}
	return proto
}

func questionToProto(question *store.SurveyQuestionEntity) *gen.DySurveyQuestion {
	proto := &gen.DySurveyQuestion{
		Id:            question.ID.String(),
		Type:          gen.DySurveyQuestionType(question.Type + 1),
		Title:         question.Title,
		Description:   stringValue(question.Description),
		Order:         int32(question.Order),
		IsRequired:    question.IsRequired,
		MaxSelections: int32Value(question.MaxSelections),
		MaxLength:     int32Value(question.MaxLength),
		MinValue:      doubleValue(question.MinValue),
		MaxValue:      doubleValue(question.MaxValue),
	}
	var options []model.SurveyOption
	store.JSONArray(question.Options, &options)
	for _, option := range options {
		proto.Options = append(proto.Options, &gen.DySurveyOption{
			Id:          option.ID,
			Label:       option.Label,
			Description: stringValue(option.Description),
			Order:       int32(option.Order),
		})
	}
	var attachments []model.CloudFileReferenceObject
	store.JSONArray(question.Attachments, &attachments)
	for _, attachment := range attachments {
		proto.Attachments = append(proto.Attachments, cloudFileToProto(&attachment))
	}
	return proto
}

func publisherToProto(publisher *store.PublisherEntity) *gen.DyPublisher {
	if publisher == nil {
		return nil
	}
	proto := &gen.DyPublisher{
		Id:        publisher.ID.String(),
		Type:      gen.DyPublisherType(publisher.Type + 1),
		Name:      publisher.Name,
		Nick:      publisher.Nick,
		Bio:       stringValue(publisher.Bio),
		AccountId: publisher.AccountID.String(),
		Rating:    publisher.Rating,
		CreatedAt: timestamppb.New(publisher.CreatedAt),
		UpdatedAt: timestamppb.New(publisher.UpdatedAt),
	}
	var picture, background model.CloudFileReferenceObject
	store.JSONMap(publisher.Picture, &picture)
	if picture.Id != "" {
		proto.Picture = cloudFileToProto(&picture)
	}
	store.JSONMap(publisher.Background, &background)
	if background.Id != "" {
		proto.Background = cloudFileToProto(&background)
	}
	return proto
}

func cloudFileToProto(file *model.CloudFileReferenceObject) *gen.DyCloudFile {
	if file == nil {
		return nil
	}
	proto := &gen.DyCloudFile{
		Id:             file.Id,
		Name:           file.Name,
		MimeType:       file.MimeType,
		Hash:           file.Hash,
		Size:           file.Size,
		HasCompression: file.HasCompression,
		Url:            file.Url,
		Width:          file.Width,
		Height:         file.Height,
	}
	if file.Blurhash != "" {
		blurhash := file.Blurhash
		proto.Blurhash = &blurhash
	}
	if file.FileMeta != nil {
		if encoded, err := json.Marshal(file.FileMeta); err == nil {
			proto.FileMeta = encoded
		}
	}
	if file.UserMeta != nil {
		if encoded, err := json.Marshal(file.UserMeta); err == nil {
			proto.UserMeta = encoded
		}
	}
	if len(file.SensitiveMarks) > 0 {
		if encoded, err := json.Marshal(file.SensitiveMarks); err == nil {
			proto.SensitiveMarks = encoded
		}
	}
	return proto
}

func stringValue(value *string) *wrapperspb.StringValue {
	if value == nil {
		return nil
	}
	return wrapperspb.String(*value)
}

func timestampValue(value *time.Time) *timestamppb.Timestamp {
	if value == nil {
		return nil
	}
	return timestamppb.New(*value)
}

func int32Value(value *int) *wrapperspb.Int32Value {
	if value == nil {
		return nil
	}
	return wrapperspb.Int32(int32(*value))
}

func doubleValue(value *float64) *wrapperspb.DoubleValue {
	if value == nil {
		return nil
	}
	return wrapperspb.Double(*value)
}

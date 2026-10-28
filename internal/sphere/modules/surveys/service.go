package surveys

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/lighthouse/internal/platform/module"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/core"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/ports"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

const (
	answerCachePrefix = "survey:answer:"
	statsCachePrefix  = "survey:stats:"
	statsCacheGroup   = "survey:"
	answerCacheTTL    = 30 * time.Minute
	statsCacheTTL     = time.Hour
)

// surveyValidationError mirrors SurveyValidationException: field-keyed errors
// serialized through the fleet's validation envelope (422).
type surveyValidationError struct {
	fields map[string][]string
}

func (e *surveyValidationError) Error() string {
	return "one or more validation errors occurred"
}

// stateError mirrors InvalidOperationException, which the controller maps to
// 409 INVALID_STATE.
type stateError struct{ message string }

func (e *stateError) Error() string { return e.message }

func newStateError(format string, args ...any) *stateError {
	return &stateError{message: fmt.Sprintf(format, args...)}
}

type service struct {
	core  *core.Services
	db    *gorm.DB
	ports *module.PortRegistry
}

func newService(c *core.Services, registry *module.PortRegistry) *service {
	return &service{core: c, db: c.DB, ports: registry}
}

// ---- publisher membership -------------------------------------------------

func (s *service) memberRole(ctx context.Context, publisherID, accountID uuid.UUID) (int, bool) {
	var member store.PublisherMemberEntity
	err := s.db.WithContext(ctx).
		Where("publisher_id = ? AND account_id = ?", publisherID, accountID).
		First(&member).Error
	if err != nil {
		return 0, false
	}
	return member.Role, true
}

func (s *service) hasRole(ctx context.Context, publisherID, accountID uuid.UUID, required int) bool {
	role, ok := s.memberRole(ctx, publisherID, accountID)
	return ok && role >= required
}

// ---- attachments ----------------------------------------------------------

// resolveAttachments mirrors SurveyService.ResolveAttachmentsAsync: it resolves
// cloud-file ids into denormalized snapshots, preserves the request order and
// drops ids the file service cannot resolve.
func (s *service) resolveAttachments(ctx context.Context, ids []string) ([]model.CloudFileReferenceObject, error) {
	if len(ids) == 0 {
		return []model.CloudFileReferenceObject{}, nil
	}
	files, ok := module.Get[ports.Files](s.ports, ports.PortFiles)
	if !ok || files == nil {
		return []model.CloudFileReferenceObject{}, nil
	}
	unique := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	resolved, err := files.GetFiles(ctx, unique)
	if err != nil {
		return nil, err
	}
	out := make([]model.CloudFileReferenceObject, 0, len(unique))
	for _, id := range unique {
		if file, ok := resolved[id]; ok {
			out = append(out, file)
		}
	}
	return out, nil
}

// ---- cache ----------------------------------------------------------------

func (s *service) cacheGet(ctx context.Context, key string, dest any) bool {
	if s.core.Redis == nil {
		return false
	}
	found, err := s.core.Redis.Get(ctx, key, dest)
	return err == nil && found
}

func (s *service) cacheSet(ctx context.Context, key string, value any, ttl time.Duration, groups []string) {
	if s.core.Redis == nil {
		return
	}
	if len(groups) > 0 {
		_ = s.core.Redis.SetWithGroups(ctx, key, value, groups, ttl)
		return
	}
	_ = s.core.Redis.Set(ctx, key, value, ttl)
}

func (s *service) cacheRemove(ctx context.Context, keys ...string) {
	if s.core.Redis == nil {
		return
	}
	for _, key := range keys {
		_ = s.core.Redis.Remove(ctx, key)
	}
}

// invalidateStats drops the per-question stats cache entries of a survey. The
// C# service removed the whole "survey:<id>" cache group; the Lighthouse cache
// interface has no group removal, so the keys are removed individually.
func (s *service) invalidateStats(ctx context.Context, surveyID uuid.UUID) {
	if s.core.Redis == nil {
		return
	}
	var ids []uuid.UUID
	s.db.WithContext(ctx).Model(&store.SurveyQuestionEntity{}).
		Where("survey_id = ?", surveyID).
		Pluck("id", &ids)
	for _, id := range ids {
		_ = s.core.Redis.Remove(ctx, statsCachePrefix+id.String())
	}
}

// ---- loading --------------------------------------------------------------

func (s *service) loadSurvey(ctx context.Context, id uuid.UUID) (*store.SurveyEntity, error) {
	var survey store.SurveyEntity
	err := s.db.WithContext(ctx).Where("id = ?", id).First(&survey).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	return &survey, nil
}

func (s *service) loadQuestions(ctx context.Context, surveyID uuid.UUID) ([]store.SurveyQuestionEntity, error) {
	var questions []store.SurveyQuestionEntity
	err := s.db.WithContext(ctx).
		Where("survey_id = ?", surveyID).
		Order(`"order" ASC, created_at ASC`).
		Find(&questions).Error
	return questions, err
}


func surveyWire(entity *store.SurveyEntity, questions []store.SurveyQuestionEntity) model.Survey {
	survey := store.SurveyToModel(entity)
	for i := range questions {
		survey.Questions = append(survey.Questions, store.SurveyQuestionToModel(&questions[i]))
	}
	return survey
}

func answerWire(entity *store.SurveyAnswerEntity) model.SurveyAnswer {
	answer := model.SurveyAnswer{
		ID:        entity.ID.String(),
		AccountID: entity.AccountID.String(),
		SurveyID:  entity.SurveyID.String(),
		CreatedAt: model.NewTime(entity.CreatedAt),
		UpdatedAt: model.NewTime(entity.UpdatedAt),
		DeletedAt: store.DeletedPtr(entity.DeletedAt),
	}
	store.JSONMap(entity.Answer, &answer.Answer)
	return answer
}

func subscriptionWire(entity *store.SurveySubscriptionEntity) model.SurveySubscription {
	return model.SurveySubscription{
		ID:        entity.ID.String(),
		AccountID: entity.AccountID.String(),
		SurveyID:  entity.SurveyID.String(),
		CreatedAt: model.NewTime(entity.CreatedAt),
		UpdatedAt: model.NewTime(entity.UpdatedAt),
		DeletedAt: store.DeletedPtr(entity.DeletedAt),
	}
}

func (s *service) countAnswers(ctx context.Context, surveyID uuid.UUID) int64 {
	var total int64
	s.db.WithContext(ctx).Model(&store.SurveyAnswerEntity{}).
		Where("survey_id = ?", surveyID).Count(&total)
	return total
}

// ---- validation -----------------------------------------------------------

func validateSurvey(questions []model.SurveyQuestion) error {
	errs := map[string][]string{}
	add := func(field, message string) {
		errs[field] = append(errs[field], message)
	}

	if len(questions) == 0 {
		add("questions", "Survey must have at least one question")
	}

	for _, question := range questions {
		field := fmt.Sprintf("questions[%s]", question.ID)
		if strings.TrimSpace(question.Title) == "" {
			add(field+".title", "Question title is required")
		}
		switch model.SurveyQuestionType(question.Type) {
		case model.QuestionSingleChoice, model.QuestionMultipleChoice:
			if len(question.Options) < 2 {
				add(field+".options", "Choice question must have at least two options")
			} else {
				for i, option := range question.Options {
					if strings.TrimSpace(option.Label) == "" {
						add(fmt.Sprintf("%s.options[%d].label", field, i), "Option label cannot be empty")
					}
				}
			}
			if question.MaxSelections != nil {
				if *question.MaxSelections < 1 {
					add(field+".max_selections", "max_selections must be at least 1")
				} else if len(question.Options) > 0 && *question.MaxSelections > len(question.Options) {
					add(field+".max_selections", "max_selections cannot exceed the number of options")
				}
			}
		case model.QuestionFreeText:
			if question.MaxLength != nil && *question.MaxLength <= 0 {
				add(field+".max_length", "max_length must be greater than 0")
			}
		case model.QuestionRating:
			if question.MinValue != nil && question.MaxValue != nil && *question.MinValue >= *question.MaxValue {
				add(field+".range", "min_value must be less than max_value")
			}
		}
	}

	if len(errs) > 0 {
		return &surveyValidationError{fields: errs}
	}
	return nil
}

func validateAnswer(questions []store.SurveyQuestionEntity, answer map[string]json.RawMessage) error {
	errs := map[string][]string{}
	add := func(field, message string) {
		errs[field] = append(errs[field], message)
	}

	for _, question := range questions {
		questionID := question.ID.String()
		field := fmt.Sprintf("answers[%s]", questionID)
		raw, present := answer[questionID]
		if question.IsRequired && (!present || !hasAnsweredValue(question.Type, raw)) {
			add(field, "Missing required answer for: "+question.Title)
			continue
		}
		if !present || isJSONNull(raw) {
			continue
		}
		var options []model.SurveyOption
		store.JSONArray(question.Options, &options)

		switch model.SurveyQuestionType(question.Type) {
		case model.QuestionRating:
			var rating float64
			if err := json.Unmarshal(raw, &rating); err != nil {
				add(field, fmt.Sprintf("Answer for question '%s' must be a number", question.Title))
				continue
			}
			if question.MinValue != nil && rating < *question.MinValue {
				add(field, fmt.Sprintf("Answer for question '%s' is below the minimum (%v)", question.Title, *question.MinValue))
			}
			if question.MaxValue != nil && rating > *question.MaxValue {
				add(field, fmt.Sprintf("Answer for question '%s' exceeds the maximum (%v)", question.Title, *question.MaxValue))
			}
		case model.QuestionFreeText:
			var text string
			if err := json.Unmarshal(raw, &text); err != nil {
				add(field, fmt.Sprintf("Answer for question '%s' must be a string", question.Title))
				continue
			}
			if question.MaxLength != nil && utf16Length(text) > *question.MaxLength {
				add(field, fmt.Sprintf("Answer for question '%s' exceeds max_length (%d)", question.Title, *question.MaxLength))
			}
		case model.QuestionSingleChoice:
			var selected string
			if err := json.Unmarshal(raw, &selected); err != nil {
				add(field, fmt.Sprintf("Answer for question '%s' must be a string (option id)", question.Title))
				continue
			}
			if !optionExists(options, selected) {
				add(field, fmt.Sprintf("Answer for question '%s' references an unknown option", question.Title))
			}
		case model.QuestionMultipleChoice:
			var selections []string
			if err := json.Unmarshal(raw, &selections); err != nil {
				add(field, fmt.Sprintf("Answer for question '%s' must be an array of option ids", question.Title))
				continue
			}
			if question.MaxSelections != nil && len(selections) > *question.MaxSelections {
				add(field, fmt.Sprintf("Answer for question '%s' selects too many options (max %d)", question.Title, *question.MaxSelections))
			}
			for _, selection := range selections {
				if !optionExists(options, selection) {
					add(field, fmt.Sprintf("Answer for question '%s' references an unknown option", question.Title))
					break
				}
			}
		case model.QuestionYesNo:
			var value bool
			if err := json.Unmarshal(raw, &value); err != nil {
				add(field, fmt.Sprintf("Answer for question '%s' must be a boolean", question.Title))
			}
		}
	}

	if len(errs) > 0 {
		return &surveyValidationError{fields: errs}
	}
	return nil
}

func isJSONNull(raw json.RawMessage) bool {
	return len(raw) == 0 || string(raw) == "null"
}

func hasAnsweredValue(questionType int, raw json.RawMessage) bool {
	if isJSONNull(raw) {
		return false
	}
	switch model.SurveyQuestionType(questionType) {
	case model.QuestionSingleChoice, model.QuestionFreeText:
		var value string
		return json.Unmarshal(raw, &value) == nil && strings.TrimSpace(value) != ""
	case model.QuestionMultipleChoice:
		var values []json.RawMessage
		return json.Unmarshal(raw, &values) == nil && len(values) > 0
	case model.QuestionYesNo:
		var value bool
		return json.Unmarshal(raw, &value) == nil
	case model.QuestionRating:
		var value float64
		return json.Unmarshal(raw, &value) == nil
	default:
		return true
	}
}

func optionExists(options []model.SurveyOption, id string) bool {
	for _, option := range options {
		if option.ID == id {
			return true
		}
	}
	return false
}

// utf16Length mirrors C# string.Length (UTF-16 code units).
func utf16Length(value string) int {
	return len(utf16.Encode([]rune(value)))
}

// ---- answers --------------------------------------------------------------

func (s *service) getAnswer(ctx context.Context, surveyID, accountID uuid.UUID) (*model.SurveyAnswer, error) {
	cacheKey := fmt.Sprintf("%s%s:%s", answerCachePrefix, surveyID, accountID)
	var cached struct {
		Answer *model.SurveyAnswer `json:"answer"`
	}
	if s.cacheGet(ctx, cacheKey, &cached) {
		return cached.Answer, nil
	}

	var entity store.SurveyAnswerEntity
	err := s.db.WithContext(ctx).
		Where("survey_id = ? AND account_id = ?", surveyID, accountID).
		First(&entity).Error
	var answer *model.SurveyAnswer
	if err == nil {
		wire := answerWire(&entity)
		answer = &wire
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	s.cacheSet(ctx, cacheKey, map[string]any{"answer": answer}, answerCacheTTL, nil)
	return answer, nil
}

func (s *service) submitAnswer(ctx context.Context, surveyID, accountID uuid.UUID, answer map[string]json.RawMessage) (*model.SurveyAnswer, error) {
	survey, err := s.loadSurvey(ctx, surveyID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, newStateError("Survey not found")
		}
		return nil, err
	}
	if survey.Status != int(model.SurveyPublished) {
		return nil, newStateError("Survey is not accepting responses (status: %s).", surveyStatusName(survey.Status))
	}
	if survey.EndedAt != nil && survey.EndedAt.Before(time.Now().UTC()) {
		return nil, newStateError("Survey has ended")
	}

	questions, err := s.loadQuestions(ctx, surveyID)
	if err != nil {
		return nil, err
	}
	if err := validateAnswer(questions, answer); err != nil {
		return nil, err
	}

	if err := s.unAnswer(ctx, surveyID, accountID); err != nil {
		return nil, err
	}

	entity := store.SurveyAnswerEntity{
		ID:        uuid.New(),
		Answer:    store.ToJSON(answer),
		AccountID: accountID,
		SurveyID:  surveyID,
	}
	if err := s.db.WithContext(ctx).Create(&entity).Error; err != nil {
		return nil, err
	}

	wire := answerWire(&entity)
	cacheKey := fmt.Sprintf("%s%s:%s", answerCachePrefix, surveyID, accountID)
	s.cacheSet(ctx, cacheKey, map[string]any{"answer": &wire}, answerCacheTTL, nil)
	s.invalidateStats(ctx, surveyID)

	if survey.NotifySubscribers {
		go s.notifySubscribers(survey, accountID)
	}
	return &wire, nil
}

// notifySubscribers pushes a "new survey answer" notification to every active
// subscriber except the answerer. Failures are swallowed, mirroring the C#
// fire-and-forget path.
func (s *service) notifySubscribers(survey *store.SurveyEntity, answererID uuid.UUID) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var subscribers []store.SurveySubscriptionEntity
	if err := s.db.WithContext(ctx).
		Where("survey_id = ? AND account_id <> ?", survey.ID, answererID).
		Find(&subscribers).Error; err != nil {
		return
	}
	if len(subscribers) == 0 {
		return
	}
	notify, ok := module.Get[ports.Notify](s.ports, ports.PortNotify)
	if !ok || notify == nil {
		return
	}
	ids := make([]string, 0, len(subscribers))
	seen := map[string]struct{}{}
	for _, subscriber := range subscribers {
		id := subscriber.AccountID.String()
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	title := "a survey"
	if survey.Title != nil && strings.TrimSpace(*survey.Title) != "" {
		title = *survey.Title
	}
	_ = notify.SendPushToAccounts(ctx, ids, title, "Someone just answered your survey.", map[string]string{
		"topic":        "surveys.answer",
		"action_uri":   "/surveys/" + survey.ID.String(),
		"survey_id":    survey.ID.String(),
		"publisher_id": survey.PublisherID.String(),
		"answerer_id":  answererID.String(),
	})
}

func (s *service) unAnswer(ctx context.Context, surveyID, accountID uuid.UUID) error {
	result := s.db.WithContext(ctx).
		Where("survey_id = ? AND account_id = ?", surveyID, accountID).
		Delete(&store.SurveyAnswerEntity{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return nil
	}
	s.cacheRemove(ctx, fmt.Sprintf("%s%s:%s", answerCachePrefix, surveyID, accountID))
	s.invalidateStats(ctx, surveyID)
	return nil
}

// ---- stats ----------------------------------------------------------------

func (s *service) questionStats(ctx context.Context, questionID uuid.UUID) (map[string]int, error) {
	cacheKey := statsCachePrefix + questionID.String()
	cached := map[string]int{}
	if s.cacheGet(ctx, cacheKey, &cached) {
		return cached, nil
	}

	var question store.SurveyQuestionEntity
	err := s.db.WithContext(ctx).Where("id = ?", questionID).First(&question).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}

	var answers []store.SurveyAnswerEntity
	if err := s.db.WithContext(ctx).
		Where("survey_id = ?", question.SurveyID).
		Find(&answers).Error; err != nil {
		return nil, err
	}

	stats := map[string]int{}
	questionIDString := questionID.String()

	if model.SurveyQuestionType(question.Type) == model.QuestionRating {
		var sum float64
		var count int
		for _, answer := range answers {
			value, ok := answerValue(answer, questionIDString)
			if !ok {
				continue
			}
			number, ok := value.(float64)
			if !ok {
				continue
			}
			sum += number
			count++
		}
		if count > 0 {
			stats["rating"] = int(math.RoundToEven(sum / float64(count)))
		}
	} else {
		for _, answer := range answers {
			value, ok := answerValue(answer, questionIDString)
			if !ok {
				continue
			}
			switch model.SurveyQuestionType(question.Type) {
			case model.QuestionSingleChoice:
				text, ok := value.(string)
				if !ok {
					continue
				}
				selected, err := uuid.Parse(text)
				if err != nil {
					continue
				}
				stats[selected.String()]++
			case model.QuestionMultipleChoice:
				values, ok := value.([]any)
				if !ok {
					continue
				}
				for _, entry := range values {
					text, ok := entry.(string)
					if !ok {
						continue
					}
					option, err := uuid.Parse(text)
					if err != nil {
						continue
					}
					stats[option.String()]++
				}
			case model.QuestionYesNo:
				switch value.(type) {
				case bool:
					if value.(bool) {
						stats["true"]++
					} else {
						stats["false"]++
					}
				default:
					stats["neither"]++
				}
			}
		}
	}

	s.cacheSet(ctx, cacheKey, stats, statsCacheTTL, []string{statsCacheGroup + question.SurveyID.String()})
	return stats, nil
}

func answerValue(answer store.SurveyAnswerEntity, questionID string) (any, bool) {
	var decoded map[string]any
	store.JSONMap(answer.Answer, &decoded)
	if decoded == nil {
		return nil, false
	}
	value, ok := decoded[questionID]
	if !ok || value == nil {
		return nil, false
	}
	return value, true
}

func (s *service) surveyStats(ctx context.Context, surveyID uuid.UUID) (map[string]any, error) {
	questions, err := s.loadQuestions(ctx, surveyID)
	if err != nil {
		return nil, err
	}
	result := map[string]any{}
	for _, question := range questions {
		stats, err := s.questionStats(ctx, question.ID)
		if err != nil {
			return nil, err
		}
		result[question.ID.String()] = stats
	}
	return result, nil
}

// ---- lifecycle ------------------------------------------------------------

func (s *service) publish(ctx context.Context, surveyID uuid.UUID) (*model.Survey, error) {
	survey, err := s.loadSurvey(ctx, surveyID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, newStateError("Survey not found")
		}
		return nil, err
	}
	if survey.Status != int(model.SurveyDraft) {
		return nil, newStateError("Cannot publish a survey in %s status; clone a new draft to revise.", surveyStatusName(survey.Status))
	}
	questions, err := s.loadQuestions(ctx, surveyID)
	if err != nil {
		return nil, err
	}
	wireQuestions := make([]model.SurveyQuestion, 0, len(questions))
	for i := range questions {
		wireQuestions = append(wireQuestions, store.SurveyQuestionToModel(&questions[i]))
	}
	if err := validateSurvey(wireQuestions); err != nil {
		return nil, err
	}

	survey.Status = int(model.SurveyPublished)
	if survey.PublishedAt == nil {
		now := time.Now().UTC()
		survey.PublishedAt = &now
	}
	if err := s.db.WithContext(ctx).Save(survey).Error; err != nil {
		return nil, err
	}
	wire := surveyWire(survey, questions)
	return &wire, nil
}

func (s *service) archive(ctx context.Context, surveyID uuid.UUID) (*model.Survey, error) {
	survey, err := s.loadSurvey(ctx, surveyID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, newStateError("Survey not found")
		}
		return nil, err
	}
	if survey.Status == int(model.SurveyArchived) {
		return nil, newStateError("Survey is already archived")
	}
	if survey.Status == int(model.SurveyDraft) {
		return nil, newStateError("Drafts cannot be archived directly; publish or delete instead")
	}
	survey.Status = int(model.SurveyArchived)
	if err := s.db.WithContext(ctx).Save(survey).Error; err != nil {
		return nil, err
	}
	questions, err := s.loadQuestions(ctx, surveyID)
	if err != nil {
		return nil, err
	}
	wire := surveyWire(survey, questions)
	return &wire, nil
}

func (s *service) clone(ctx context.Context, surveyID uuid.UUID) (*model.Survey, error) {
	source, err := s.loadSurvey(ctx, surveyID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, newStateError("Survey not found")
		}
		return nil, err
	}
	questions, err := s.loadQuestions(ctx, surveyID)
	if err != nil {
		return nil, err
	}

	clone := store.SurveyEntity{
		ID:                uuid.New(),
		Title:             source.Title,
		Description:       source.Description,
		EndedAt:           source.EndedAt,
		IsAnonymous:       source.IsAnonymous,
		Status:            int(model.SurveyDraft),
		NotifySubscribers: source.NotifySubscribers,
		Attachments:       source.Attachments,
		HideResults:       source.HideResults,
		PublisherID:       source.PublisherID,
	}

	clones := make([]store.SurveyQuestionEntity, 0, len(questions))
	for _, question := range questions {
		var options []model.SurveyOption
		store.JSONArray(question.Options, &options)
		for i := range options {
			options[i].ID = uuid.NewString()
		}
		clones = append(clones, store.SurveyQuestionEntity{
			ID:            uuid.New(),
			Type:          question.Type,
			Options:       store.ToJSON(options),
			Title:         question.Title,
			Description:   question.Description,
			Order:         question.Order,
			IsRequired:    question.IsRequired,
			Attachments:   question.Attachments,
			MaxSelections: question.MaxSelections,
			MaxLength:     question.MaxLength,
			MinValue:      question.MinValue,
			MaxValue:      question.MaxValue,
			SurveyID:      clone.ID,
		})
	}

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&clone).Error; err != nil {
			return err
		}
		if len(clones) > 0 {
			if err := tx.Create(&clones).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	wire := surveyWire(&clone, clones)
	return &wire, nil
}

func surveyStatusName(status int) string {
	switch model.SurveyStatus(status) {
	case model.SurveyDraft:
		return "Draft"
	case model.SurveyPublished:
		return "Published"
	case model.SurveyArchived:
		return "Archived"
	default:
		return fmt.Sprintf("%d", status)
	}
}

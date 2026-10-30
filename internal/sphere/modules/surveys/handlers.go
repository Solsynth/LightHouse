package surveys

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"src.solsynth.dev/sosys/go/pkg/errs"
	"src.solsynth.dev/sosys/lighthouse/internal/platform/accountjson"
	"src.solsynth.dev/sosys/lighthouse/internal/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/internal/platform/httpx"
	"src.solsynth.dev/sosys/lighthouse/internal/platform/module"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/core"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/ports"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

// answerResponse is a feedback row: SnSurveyAnswer plus the [NotMapped] Account
// the C# controller populates for non-anonymous surveys.
type answerResponse struct {
	model.SurveyAnswer
	Account any `json:"account"`
}

type surveyRequest struct {
	Title             *string                  `json:"title"`
	Description       *string                  `json:"description"`
	EndedAt           *model.Time              `json:"ended_at"`
	ClearEndedAt      *bool                    `json:"clear_ended_at"`
	IsAnonymous       *bool                    `json:"is_anonymous"`
	NotifySubscribers *bool                    `json:"notify_subscribers"`
	HideResults       *bool                    `json:"hide_results"`
	Attachments       []string                 `json:"attachments"`
	Questions         *[]surveyQuestionRequest `json:"questions"`
}

type surveyQuestionRequest struct {
	ID            string               `json:"id"`
	Type          int                  `json:"type"`
	Options       []model.SurveyOption `json:"options"`
	Title         string               `json:"title"`
	Description   *string              `json:"description"`
	Order         int                  `json:"order"`
	IsRequired    bool                 `json:"is_required"`
	MaxSelections *int                 `json:"max_selections"`
	MaxLength     *int                 `json:"max_length"`
	MinValue      *float64             `json:"min_value"`
	MaxValue      *float64             `json:"max_value"`
	Attachments   []string             `json:"attachments"`
}

type answerRequest struct {
	Answer map[string]json.RawMessage `json:"answer"`
}

func (r surveyQuestionRequest) toWire() model.SurveyQuestion {
	id := r.ID
	if id == "" {
		id = uuid.NewString()
	}
	options := make([]model.SurveyOption, 0, len(r.Options))
	for _, option := range r.Options {
		if option.ID == "" {
			option.ID = uuid.NewString()
		}
		options = append(options, option)
	}
	return model.SurveyQuestion{
		ID:            id,
		Type:          r.Type,
		Options:       options,
		Title:         r.Title,
		Description:   r.Description,
		Order:         r.Order,
		IsRequired:    r.IsRequired,
		Attachments:   []model.CloudFileReferenceObject{},
		MaxSelections: r.MaxSelections,
		MaxLength:     r.MaxLength,
		MinValue:      r.MinValue,
		MaxValue:      r.MaxValue,
	}
}

func questionEntity(wire model.SurveyQuestion, surveyID uuid.UUID) store.SurveyQuestionEntity {
	id, err := uuid.Parse(wire.ID)
	if err != nil {
		id = uuid.New()
	}
	return store.SurveyQuestionEntity{
		ID:            id,
		Type:          wire.Type,
		Options:       store.ToJSON(wire.Options),
		Title:         wire.Title,
		Description:   wire.Description,
		Order:         wire.Order,
		IsRequired:    wire.IsRequired,
		Attachments:   store.ToJSON(wire.Attachments),
		MaxSelections: wire.MaxSelections,
		MaxLength:     wire.MaxLength,
		MinValue:      wire.MinValue,
		MaxValue:      wire.MaxValue,
		SurveyID:      surveyID,
	}
}

func surveyIDFromPath(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errs.New("SURVEY_NOT_FOUND", "Survey not found", http.StatusNotFound))
		return uuid.Nil, false
	}
	return id, true
}

func failSurvey(c *gin.Context, err error) {
	var validation *surveyValidationError
	if errors.As(err, &validation) {
		httpx.Fail(c, errs.Validation(validation.fields))
		return
	}
	var state *stateError
	if errors.As(err, &state) {
		httpx.Fail(c, errs.New("INVALID_STATE", state.Error(), http.StatusConflict))
		return
	}
	httpx.Fail(c, errs.New("SERVER_ERROR", err.Error(), http.StatusBadRequest))
}

func (s *service) getSurvey(c *gin.Context) {
	id, ok := surveyIDFromPath(c)
	if !ok {
		return
	}
	survey, err := s.loadSurvey(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, errs.New("SURVEY_NOT_FOUND", "Survey not found", http.StatusNotFound))
			return
		}
		httpx.Internal(c)
		return
	}
	questions, err := s.loadQuestions(c.Request.Context(), id)
	if err != nil {
		httpx.Internal(c)
		return
	}

	payload := model.SurveyWithStats{
		Survey:       surveyWire(survey, questions),
		Stats:        map[string]any{},
		TotalAnswers: int(s.countAnswers(c.Request.Context(), id)),
	}

	user := authn.CurrentUser(c)
	accountID := uuid.Nil
	isViewer := false
	if user != nil {
		accountID, _ = uuid.Parse(user.GetId())
		answer, err := s.getAnswer(c.Request.Context(), id, accountID)
		if err == nil && answer != nil {
			payload.UserAnswer = answer
			payload.HasAnswered = true
		}
		isViewer = s.hasRole(c.Request.Context(), survey.PublisherID, accountID, int(model.PublisherRoleViewer))
	}

	if !survey.HideResults || isViewer {
		stats, err := s.surveyStats(c.Request.Context(), id)
		if err != nil {
			httpx.Internal(c)
			return
		}
		payload.Stats = stats
	}

	httpx.OK(c, payload)
}

func (s *service) answerSurvey(c *gin.Context) {
	id, ok := surveyIDFromPath(c)
	if !ok {
		return
	}
	accountID, err := uuid.Parse(authn.AccountID(c))
	if err != nil {
		httpx.Unauthorized(c)
		return
	}
	var request answerRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	if request.Answer == nil {
		request.Answer = map[string]json.RawMessage{}
	}
	answer, err := s.submitAnswer(c.Request.Context(), id, accountID, request.Answer)
	if err != nil {
		failSurvey(c, err)
		return
	}
	httpx.OK(c, answer)
}

func (s *service) deleteAnswer(c *gin.Context) {
	id, ok := surveyIDFromPath(c)
	if !ok {
		return
	}
	accountID, err := uuid.Parse(authn.AccountID(c))
	if err != nil {
		httpx.Unauthorized(c)
		return
	}
	if err := s.unAnswer(c.Request.Context(), id, accountID); err != nil {
		failSurvey(c, err)
		return
	}
	httpx.NoContent(c)
}

func (s *service) getFeedback(c *gin.Context) {
	id, ok := surveyIDFromPath(c)
	if !ok {
		return
	}
	accountID, err := uuid.Parse(authn.AccountID(c))
	if err != nil {
		httpx.Unauthorized(c)
		return
	}

	offset := parseQueryInt(c, "offset", 0)
	if offset < 0 {
		offset = 0
	}
	take := parseQueryInt(c, "take", 20)
	if take < 1 {
		take = 1
	}
	if take > 100 {
		take = 100
	}

	survey, err := s.loadSurvey(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, errs.New("SURVEY_NOT_FOUND", "Survey not found", http.StatusNotFound))
			return
		}
		httpx.Internal(c)
		return
	}
	if !s.hasRole(c.Request.Context(), survey.PublisherID, accountID, int(model.PublisherRoleViewer)) {
		httpx.Fail(c, errs.New("FORBIDDEN", "You need to be a viewer to view this survey's feedback.", http.StatusForbidden))
		return
	}

	var total int64
	s.db.WithContext(c.Request.Context()).Model(&store.SurveyAnswerEntity{}).
		Where("survey_id = ?", id).Count(&total)

	var entities []store.SurveyAnswerEntity
	if err := s.db.WithContext(c.Request.Context()).
		Where("survey_id = ?", id).
		Order("created_at DESC").
		Offset(offset).Limit(take).
		Find(&entities).Error; err != nil {
		httpx.Internal(c)
		return
	}

	responses := make([]answerResponse, 0, len(entities))
	accountIDs := make([]string, 0, len(entities))
	for i := range entities {
		responses = append(responses, answerResponse{SurveyAnswer: answerWire(&entities[i])})
		accountIDs = append(accountIDs, entities[i].AccountID.String())
	}

	if !survey.IsAnonymous {
		s.populateAccounts(c.Request.Context(), responses, accountIDs)
	}

	httpx.List(c, responses, total)
}

func (s *service) populateAccounts(ctx context.Context, responses []answerResponse, accountIDs []string) {
	accounts, ok := module.Get[ports.Accounts](s.ports, ports.PortAccounts)
	if !ok || accounts == nil {
		return
	}
	resolved, err := accounts.GetAccounts(ctx, accountIDs)
	if err != nil {
		return
	}
	byID := make(map[string]any, len(resolved))
	for _, account := range resolved {
		if account == nil {
			continue
		}
		byID[account.GetId()] = accountjson.FromProto(account)
	}
	for i := range responses {
		responses[i].Account = byID[responses[i].AccountID]
	}
}

func (s *service) listMine(c *gin.Context) {
	accountID, err := uuid.Parse(authn.AccountID(c))
	if err != nil {
		httpx.Unauthorized(c)
		return
	}
	page, ok := httpx.PageQuery(c, maxTake(s.core))
	if !ok {
		return
	}

	publisherIDs := make([]uuid.UUID, 0)
	if pubName, hasPub := c.GetQuery("pub"); hasPub {
		if err := s.db.WithContext(c.Request.Context()).
			Table("publisher_members").
			Joins("JOIN publishers ON publishers.id = publisher_members.publisher_id AND publishers.deleted_at IS NULL").
			Where("LOWER(publishers.name) = ?", lower(pubName)).
			Where("publisher_members.account_id = ?", accountID).
			Pluck("publisher_members.publisher_id", &publisherIDs).Error; err != nil {
			httpx.Internal(c)
			return
		}
	} else {
		if err := s.db.WithContext(c.Request.Context()).
			Model(&store.PublisherMemberEntity{}).
			Where("account_id = ?", accountID).
			Pluck("publisher_id", &publisherIDs).Error; err != nil {
			httpx.Internal(c)
			return
		}
	}

	if len(publisherIDs) == 0 {
		httpx.List(c, []model.Survey{}, 0)
		return
	}

	query := s.db.WithContext(c.Request.Context()).Model(&store.SurveyEntity{}).
		Where("publisher_id IN ?", publisherIDs)
	if active := c.Query("active"); active == "true" || active == "1" {
		query = query.Where("(ended_at IS NULL OR ended_at > ?)", time.Now().UTC())
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		httpx.Internal(c)
		return
	}

	var surveys []store.SurveyEntity
	if err := query.Order("created_at DESC").
		Offset(page.Offset).Limit(page.Take).
		Find(&surveys).Error; err != nil {
		httpx.Internal(c)
		return
	}

	questionsBySurvey, err := s.questionsFor(c.Request.Context(), surveyIDs(surveys))
	if err != nil {
		httpx.Internal(c)
		return
	}
	items := make([]model.Survey, 0, len(surveys))
	for i := range surveys {
		items = append(items, surveyWire(&surveys[i], questionsBySurvey[surveys[i].ID]))
	}
	httpx.List(c, items, total)
}

func (s *service) questionsFor(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID][]store.SurveyQuestionEntity, error) {
	result := map[uuid.UUID][]store.SurveyQuestionEntity{}
	if len(ids) == 0 {
		return result, nil
	}
	var questions []store.SurveyQuestionEntity
	if err := s.db.WithContext(ctx).
		Where("survey_id IN ?", ids).
		Order(`"order" ASC, created_at ASC`).
		Find(&questions).Error; err != nil {
		return nil, err
	}
	for _, question := range questions {
		result[question.SurveyID] = append(result[question.SurveyID], question)
	}
	return result, nil
}

func surveyIDs(surveys []store.SurveyEntity) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(surveys))
	for i := range surveys {
		ids = append(ids, surveys[i].ID)
	}
	return ids
}

func (s *service) createSurvey(c *gin.Context) {
	accountID, err := uuid.Parse(authn.AccountID(c))
	if err != nil {
		httpx.Unauthorized(c)
		return
	}
	var request surveyRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	if request.Questions == nil {
		httpx.BadRequest(c, "SURVEY_QUESTIONS_REQUIRED", "Questions are required.")
		return
	}

	pubName, _ := c.GetQuery("pub")
	publisher, err := s.publisherByName(c.Request.Context(), pubName)
	if err != nil || publisher == nil {
		httpx.BadRequest(c, "PUBLISHER_NOT_FOUND", "Publisher was not found.")
		return
	}
	if !s.hasRole(c.Request.Context(), publisher.ID, accountID, int(model.PublisherRoleEditor)) {
		httpx.Fail(c, errs.New("FORBIDDEN", "You need at least be an editor to create surveys as this publisher.", http.StatusForbidden))
		return
	}

	attachments, err := s.resolveAttachments(c.Request.Context(), request.Attachments)
	if err != nil {
		httpx.Fail(c, errs.New("SERVER_ERROR", err.Error(), http.StatusBadRequest))
		return
	}

	questions := make([]model.SurveyQuestion, 0, len(*request.Questions))
	for _, questionRequest := range *request.Questions {
		question := questionRequest.toWire()
		resolved, err := s.resolveAttachments(c.Request.Context(), questionRequest.Attachments)
		if err != nil {
			httpx.Fail(c, errs.New("SERVER_ERROR", err.Error(), http.StatusBadRequest))
			return
		}
		question.Attachments = resolved
		questions = append(questions, question)
	}

	if err := validateSurvey(questions); err != nil {
		failSurvey(c, err)
		return
	}

	survey := store.SurveyEntity{
		ID:                uuid.New(),
		Title:             request.Title,
		Description:       request.Description,
		IsAnonymous:       boolValue(request.IsAnonymous),
		Status:            int(model.SurveyDraft),
		NotifySubscribers: boolValue(request.NotifySubscribers),
		HideResults:       boolValue(request.HideResults),
		Attachments:       store.ToJSON(attachments),
		PublisherID:       publisher.ID,
	}
	if request.EndedAt != nil {
		endedAt := store.TimeValue(request.EndedAt)
		survey.EndedAt = &endedAt
	}

	entities := make([]store.SurveyQuestionEntity, 0, len(questions))
	for _, question := range questions {
		entities = append(entities, questionEntity(question, survey.ID))
	}

	err = s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&survey).Error; err != nil {
			return err
		}
		if len(entities) > 0 {
			if err := tx.Create(&entities).Error; err != nil {
				return err
			}
		}
		if survey.NotifySubscribers {
			return ensureSubscription(tx, survey.ID, accountID)
		}
		return nil
	})
	if err != nil {
		httpx.Fail(c, errs.New("SERVER_ERROR", err.Error(), http.StatusBadRequest))
		return
	}

	httpx.OK(c, surveyWire(&survey, entities))
}

func (s *service) updateSurvey(c *gin.Context) {
	id, ok := surveyIDFromPath(c)
	if !ok {
		return
	}
	accountID, err := uuid.Parse(authn.AccountID(c))
	if err != nil {
		httpx.Unauthorized(c)
		return
	}
	var request surveyRequest
	if !httpx.BindJSON(c, &request) {
		return
	}

	survey, err := s.loadSurvey(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, errs.New("SURVEY_NOT_FOUND", "Survey not found", http.StatusNotFound))
			return
		}
		httpx.Internal(c)
		return
	}
	if !s.hasRole(c.Request.Context(), survey.PublisherID, accountID, int(model.PublisherRoleEditor)) {
		httpx.Fail(c, errs.New("FORBIDDEN", "You need to be at least an editor to update this survey.", http.StatusForbidden))
		return
	}
	if survey.Status != int(model.SurveyDraft) {
		httpx.Fail(c, errs.New("SURVEY_IMMUTABLE",
			"Survey in "+surveyStatusName(survey.Status)+" status is immutable; clone a new draft to revise.",
			http.StatusConflict))
		return
	}

	existingEntities, err := s.loadQuestions(c.Request.Context(), id)
	if err != nil {
		httpx.Internal(c)
		return
	}
	existing := make(map[uuid.UUID]store.SurveyQuestionEntity, len(existingEntities))
	for _, entity := range existingEntities {
		existing[entity.ID] = entity
	}

	var attachments []model.CloudFileReferenceObject
	if request.Attachments != nil {
		attachments, err = s.resolveAttachments(c.Request.Context(), request.Attachments)
		if err != nil {
			httpx.Fail(c, errs.New("SERVER_ERROR", err.Error(), http.StatusBadRequest))
			return
		}
	}

	if request.Title != nil {
		survey.Title = request.Title
	}
	if request.Description != nil {
		survey.Description = request.Description
	}
	if request.ClearEndedAt != nil && *request.ClearEndedAt {
		survey.EndedAt = nil
	} else if request.EndedAt != nil {
		endedAt := store.TimeValue(request.EndedAt)
		survey.EndedAt = &endedAt
	}
	if request.IsAnonymous != nil {
		survey.IsAnonymous = *request.IsAnonymous
	}
	if request.NotifySubscribers != nil {
		survey.NotifySubscribers = *request.NotifySubscribers
	}
	if request.HideResults != nil {
		survey.HideResults = *request.HideResults
	}
	if request.Attachments != nil {
		survey.Attachments = store.ToJSON(attachments)
	}

	var incoming []model.SurveyQuestion
	if request.Questions != nil {
		incoming = make([]model.SurveyQuestion, 0, len(*request.Questions))
		for _, questionRequest := range *request.Questions {
			question := questionRequest.toWire()
			if questionRequest.Attachments != nil {
				resolved, err := s.resolveAttachments(c.Request.Context(), questionRequest.Attachments)
				if err != nil {
					httpx.Fail(c, errs.New("SERVER_ERROR", err.Error(), http.StatusBadRequest))
					return
				}
				question.Attachments = resolved
			} else if parsed, err := uuid.Parse(question.ID); err == nil {
				if current, ok := existing[parsed]; ok {
					var preserved []model.CloudFileReferenceObject
					store.JSONArray(current.Attachments, &preserved)
					if preserved == nil {
						preserved = []model.CloudFileReferenceObject{}
					}
					question.Attachments = preserved
				}
			}
			incoming = append(incoming, question)
		}
		if err := validateSurvey(incoming); err != nil {
			failSurvey(c, err)
			return
		}
	}

	err = s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if request.NotifySubscribers != nil && survey.NotifySubscribers {
			if err := ensureSubscription(tx, survey.ID, accountID); err != nil {
				return err
			}
		}
		if err := tx.Save(survey).Error; err != nil {
			return err
		}
		if request.Questions == nil {
			return nil
		}

		incomingIDs := make([]uuid.UUID, 0, len(incoming))
		for _, question := range incoming {
			parsed, err := uuid.Parse(question.ID)
			if err != nil {
				parsed = uuid.New()
			}
			incomingIDs = append(incomingIDs, parsed)
		}

		removed := make([]uuid.UUID, 0)
		for existingID := range existing {
			found := false
			for _, incomingID := range incomingIDs {
				if incomingID == existingID {
					found = true
					break
				}
			}
			if !found {
				removed = append(removed, existingID)
			}
		}
		if len(removed) > 0 {
			if err := tx.Where("id IN ?", removed).Delete(&store.SurveyQuestionEntity{}).Error; err != nil {
				return err
			}
		}

		for i, question := range incoming {
			questionID := incomingIDs[i]
			values := map[string]any{
				"type":           question.Type,
				"options":        store.ToJSON(question.Options),
				"title":          question.Title,
				"description":    question.Description,
				"order":          question.Order,
				"is_required":    question.IsRequired,
				"attachments":    store.ToJSON(question.Attachments),
				"max_selections": question.MaxSelections,
				"max_length":     question.MaxLength,
				"min_value":      question.MinValue,
				"max_value":      question.MaxValue,
				"updated_at":     time.Now().UTC(),
			}
			if _, ok := existing[questionID]; ok {
				if err := tx.Model(&store.SurveyQuestionEntity{}).
					Where("id = ?", questionID).Updates(values).Error; err != nil {
					return err
				}
				continue
			}
			entity := questionEntity(question, survey.ID)
			entity.ID = questionID
			if err := tx.Create(&entity).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		failSurvey(c, err)
		return
	}

	questions, err := s.loadQuestions(c.Request.Context(), id)
	if err != nil {
		httpx.Internal(c)
		return
	}
	httpx.OK(c, surveyWire(survey, questions))
}

func (s *service) publishSurvey(c *gin.Context) {
	s.lifecycle(c, "You need at least editor rights to publish this survey.", s.publish)
}

func (s *service) archiveSurvey(c *gin.Context) {
	s.lifecycle(c, "You need at least editor rights to archive this survey.", s.archive)
}

func (s *service) cloneSurvey(c *gin.Context) {
	s.lifecycle(c, "You need at least editor rights to clone this survey.", s.clone)
}

func (s *service) lifecycle(c *gin.Context, forbiddenMessage string, action func(context.Context, uuid.UUID) (*model.Survey, error)) {
	id, ok := surveyIDFromPath(c)
	if !ok {
		return
	}
	accountID, err := uuid.Parse(authn.AccountID(c))
	if err != nil {
		httpx.Unauthorized(c)
		return
	}
	existing, err := s.loadSurvey(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, errs.New("SURVEY_NOT_FOUND", "Survey not found", http.StatusNotFound))
			return
		}
		httpx.Internal(c)
		return
	}
	if !s.hasRole(c.Request.Context(), existing.PublisherID, accountID, int(model.PublisherRoleEditor)) {
		httpx.Fail(c, errs.New("FORBIDDEN", forbiddenMessage, http.StatusForbidden))
		return
	}
	result, err := action(c.Request.Context(), id)
	if err != nil {
		failSurvey(c, err)
		return
	}
	httpx.OK(c, result)
}

func (s *service) deleteSurvey(c *gin.Context) {
	id, ok := surveyIDFromPath(c)
	if !ok {
		return
	}
	accountID, err := uuid.Parse(authn.AccountID(c))
	if err != nil {
		httpx.Unauthorized(c)
		return
	}
	survey, err := s.loadSurvey(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, errs.New("SURVEY_NOT_FOUND", "Survey not found", http.StatusNotFound))
			return
		}
		httpx.Internal(c)
		return
	}
	if !s.hasRole(c.Request.Context(), survey.PublisherID, accountID, int(model.PublisherRoleEditor)) {
		httpx.Fail(c, errs.New("FORBIDDEN", "You need to be at least an editor to delete this survey.", http.StatusForbidden))
		return
	}

	err = s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("survey_id = ?", id).Delete(&store.SurveyAnswerEntity{}).Error; err != nil {
			return err
		}
		if err := tx.Where("survey_id = ?", id).Delete(&store.SurveyQuestionEntity{}).Error; err != nil {
			return err
		}
		if err := tx.Where("survey_id = ?", id).Delete(&store.SurveySubscriptionEntity{}).Error; err != nil {
			return err
		}
		return tx.Delete(&store.SurveyEntity{ID: id}).Error
	})
	if err != nil {
		httpx.Fail(c, errs.New("SURVEY_DELETE_ERROR", "An error occurred while deleting the survey... "+err.Error(), http.StatusInternalServerError))
		return
	}
	s.invalidateStats(c.Request.Context(), id)
	httpx.NoContent(c)
}

// ---- subscriptions --------------------------------------------------------

func ensureSubscription(tx *gorm.DB, surveyID, accountID uuid.UUID) error {
	var count int64
	if err := tx.Model(&store.SurveySubscriptionEntity{}).
		Where("survey_id = ? AND account_id = ?", surveyID, accountID).
		Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	return tx.Create(&store.SurveySubscriptionEntity{
		ID:        uuid.New(),
		SurveyID:  surveyID,
		AccountID: accountID,
	}).Error
}

func (s *service) subscribe(c *gin.Context) {
	id, ok := surveyIDFromPath(c)
	if !ok {
		return
	}
	accountID, err := uuid.Parse(authn.AccountID(c))
	if err != nil {
		httpx.Unauthorized(c)
		return
	}
	if _, err := s.loadSurvey(c.Request.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, errs.New("SURVEY_NOT_FOUND", "Survey not found", http.StatusNotFound))
			return
		}
		httpx.Internal(c)
		return
	}

	var existing store.SurveySubscriptionEntity
	err = s.db.WithContext(c.Request.Context()).
		Where("survey_id = ? AND account_id = ?", id, accountID).
		First(&existing).Error
	if err == nil {
		httpx.OK(c, subscriptionWire(&existing))
		return
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		httpx.Internal(c)
		return
	}

	subscription := store.SurveySubscriptionEntity{
		ID:        uuid.New(),
		SurveyID:  id,
		AccountID: accountID,
	}
	if err := s.db.WithContext(c.Request.Context()).Create(&subscription).Error; err != nil {
		httpx.Internal(c)
		return
	}
	httpx.OK(c, subscriptionWire(&subscription))
}

func (s *service) unsubscribe(c *gin.Context) {
	id, ok := surveyIDFromPath(c)
	if !ok {
		return
	}
	accountID, err := uuid.Parse(authn.AccountID(c))
	if err != nil {
		httpx.Unauthorized(c)
		return
	}
	result := s.db.WithContext(c.Request.Context()).
		Where("survey_id = ? AND account_id = ?", id, accountID).
		Delete(&store.SurveySubscriptionEntity{})
	if result.Error != nil {
		httpx.Internal(c)
		return
	}
	httpx.NoContent(c)
}

func (s *service) getSubscription(c *gin.Context) {
	id, ok := surveyIDFromPath(c)
	if !ok {
		return
	}
	accountID, err := uuid.Parse(authn.AccountID(c))
	if err != nil {
		httpx.Unauthorized(c)
		return
	}
	if _, err := s.loadSurvey(c.Request.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, errs.New("SURVEY_NOT_FOUND", "Survey not found", http.StatusNotFound))
			return
		}
		httpx.Internal(c)
		return
	}
	var subscription store.SurveySubscriptionEntity
	err = s.db.WithContext(c.Request.Context()).
		Where("survey_id = ? AND account_id = ?", id, accountID).
		First(&subscription).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httpx.Fail(c, errs.New("SURVEY_SUBSCRIPTION_NOT_FOUND", "Subscription not found", http.StatusNotFound))
			return
		}
		httpx.Internal(c)
		return
	}
	httpx.OK(c, subscriptionWire(&subscription))
}

// ---- helpers --------------------------------------------------------------

func (s *service) publisherByName(ctx context.Context, name string) (*store.PublisherEntity, error) {
	if name == "" {
		return nil, store.ErrNotFound
	}
	var publisher store.PublisherEntity
	err := s.db.WithContext(ctx).
		Where("LOWER(name) = ?", lower(name)).
		First(&publisher).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	return &publisher, nil
}

func maxTake(c *core.Services) int {
	if c != nil && c.Config != nil && c.Config.Security.MaxTake > 0 {
		return c.Config.Security.MaxTake
	}
	return 100
}

func parseQueryInt(c *gin.Context, key string, fallback int) int {
	value := c.Query(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func boolValue(value *bool) bool {
	return value != nil && *value
}

func lower(value string) string {
	return strings.ToLower(value)
}

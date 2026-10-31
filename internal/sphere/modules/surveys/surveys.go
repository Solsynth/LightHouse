// Package surveys ports the Sphere survey feature: survey authoring, answering,
// result tallies and per-survey subscriptions.
package surveys

import (
	"context"

	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"

	"src.solsynth.dev/sosys/lighthouse/shared/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/core"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

// ModuleID is the surveys module's registry ID.
const ModuleID = "app.solian.sphere.surveys"

// Factory returns the surveys module factory.
func Factory() module.Factory[core.Services] {
	return module.Factory[core.Services]{
		ID:             ModuleID,
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Requires:       []string{"app.solian.sphere.publishers"},
		Ports: []module.PortRequirement{
			{ID: "files", Optional: true},
			{ID: "accounts", Optional: true},
			{ID: "notify", Optional: true},
		},
		Models: []any{
			store.SurveyEntity{},
			store.SurveyQuestionEntity{},
			store.SurveyAnswerEntity{},
			store.SurveySubscriptionEntity{},
		},
		SQL: []module.SchemaHook{module.DDL(
			`CREATE INDEX IF NOT EXISTS idx_survey_answers_account_survey ON survey_answers (account_id, survey_id, deleted_at)`,
			`CREATE INDEX IF NOT EXISTS idx_survey_answers_survey ON survey_answers (survey_id, deleted_at)`,
			`CREATE INDEX IF NOT EXISTS idx_survey_questions_survey ON survey_questions (survey_id, deleted_at)`,
			`CREATE INDEX IF NOT EXISTS idx_survey_subscriptions_account_survey ON survey_subscriptions (account_id, survey_id, deleted_at)`,
		)},
		Capabilities: []string{
			"surveys.create", "surveys.answer", "surveys.publish", "surveys.archive",
			"surveys.clone", "surveys.subscribe",
		},
		New: func() module.Instance[core.Services] { return &instance{} },
	}
}

type instance struct {
	svc *service
}

func (i *instance) Configure(_ context.Context, host *module.Host[core.Services]) error {
	svc := newService(host.Core, host.Ports)
	i.svc = svc

	api := host.API.Group("/surveys")
	api.GET("/me", authn.RequireAuth(), svc.listMine)
	api.GET("/:id", svc.getSurvey)
	api.POST("", authn.RequireAuth(), svc.createSurvey)
	api.POST("/:id/answer", authn.RequireAuth(), svc.core.Auth.RequirePermission("surveys.answer"), svc.answerSurvey)
	api.DELETE("/:id/answer", authn.RequireAuth(), svc.core.Auth.RequirePermission("surveys.answer"), svc.deleteAnswer)
	api.GET("/:id/feedback", authn.RequireAuth(), svc.getFeedback)
	api.PATCH("/:id", authn.RequireAuth(), svc.updateSurvey)
	api.POST("/:id/publish", authn.RequireAuth(), svc.core.Auth.RequirePermission("surveys.publish"), svc.publishSurvey)
	api.POST("/:id/archive", authn.RequireAuth(), svc.core.Auth.RequirePermission("surveys.archive"), svc.archiveSurvey)
	api.POST("/:id/clone", authn.RequireAuth(), svc.core.Auth.RequirePermission("surveys.clone"), svc.cloneSurvey)
	api.DELETE("/:id", authn.RequireAuth(), svc.deleteSurvey)
	api.POST("/:id/subscribe", authn.RequireAuth(), svc.core.Auth.RequirePermission("surveys.subscribe"), svc.subscribe)
	api.POST("/:id/unsubscribe", authn.RequireAuth(), svc.core.Auth.RequirePermission("surveys.subscribe"), svc.unsubscribe)
	api.GET("/:id/subscription", authn.RequireAuth(), svc.getSubscription)

	if host.GRPC != nil {
		gen.RegisterDySurveyServiceServer(host.GRPC, &surveyServer{svc: svc})
	}
	return nil
}

func (i *instance) Stop(context.Context) error { return nil }

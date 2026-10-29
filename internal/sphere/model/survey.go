package model

// Survey is the wire shape of a Sphere survey.
type Survey struct {
	ID                string                     `json:"id"`
	Title             *string                    `json:"title"`
	Description       *string                    `json:"description"`
	EndedAt           *Time                      `json:"ended_at"`
	IsAnonymous       bool                       `json:"is_anonymous"`
	Status            int                        `json:"status"`
	PublishedAt       *Time                      `json:"published_at"`
	NotifySubscribers bool                       `json:"notify_subscribers"`
	Attachments       []CloudFileReferenceObject `json:"attachments"`
	HideResults       bool                       `json:"hide_results"`
	PublisherID       string                     `json:"publisher_id"`
	Publisher         *Publisher                 `json:"publisher"`
	Questions         []SurveyQuestion           `json:"questions"`
	CreatedAt         *Time                      `json:"created_at"`
	UpdatedAt         *Time                      `json:"updated_at"`
	DeletedAt         *Time                      `json:"deleted_at"`
}

// SurveyOption is one selectable option.
type SurveyOption struct {
	ID          string  `json:"id"`
	Label       string  `json:"label"`
	Description *string `json:"description"`
	Order       int     `json:"order"`
}

// SurveyQuestion is one survey question.
type SurveyQuestion struct {
	ID            string                     `json:"id"`
	Type          int                        `json:"type"`
	Options       []SurveyOption             `json:"options"`
	Title         string                     `json:"title"`
	Description   *string                    `json:"description"`
	Order         int                        `json:"order"`
	IsRequired    bool                       `json:"is_required"`
	Attachments   []CloudFileReferenceObject `json:"attachments"`
	MaxSelections *int                       `json:"max_selections"`
	MaxLength     *int                       `json:"max_length"`
	MinValue      *float64                   `json:"min_value"`
	MaxValue      *float64                   `json:"max_value"`
	SurveyID      string                     `json:"survey_id"`
	CreatedAt     *Time                      `json:"created_at"`
	UpdatedAt     *Time                      `json:"updated_at"`
	DeletedAt     *Time                      `json:"deleted_at"`
}

// SurveyAnswer is one account's answer set.
type SurveyAnswer struct {
	ID        string         `json:"id"`
	Answer    map[string]any `json:"answer"`
	AccountID string         `json:"account_id"`
	SurveyID  string         `json:"survey_id"`
	CreatedAt *Time          `json:"created_at"`
	UpdatedAt *Time          `json:"updated_at"`
	DeletedAt *Time          `json:"deleted_at"`
}

// SurveySubscription is one survey subscription.
type SurveySubscription struct {
	ID        string `json:"id"`
	AccountID string `json:"account_id"`
	SurveyID  string `json:"survey_id"`
	CreatedAt *Time  `json:"created_at"`
	UpdatedAt *Time  `json:"updated_at"`
	DeletedAt *Time  `json:"deleted_at"`
}

// SurveyQuestionStats is the per-question option tally.
type SurveyQuestionStats struct {
	QuestionID string         `json:"question_id"`
	Options    map[string]int `json:"options"`
}

// SurveyWithStats is the survey read payload with answer tallies.
type SurveyWithStats struct {
	Survey
	UserAnswer   *SurveyAnswer  `json:"user_answer"`
	Stats        map[string]any `json:"stats"`
	TotalAnswers int            `json:"total_answers"`
	HasAnswered  bool           `json:"has_answered"`
}

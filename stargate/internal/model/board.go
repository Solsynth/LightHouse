package model

// BoardItem mirrors SnAccountBoardItem (Passport AccountBoard.cs).
type BoardItem struct {
	Id                 string         `json:"id,omitempty"`
	Order              int            `json:"order"`
	Kind               BoardItemKind  `json:"kind"`
	WidgetKey          *string        `json:"widget_key,omitempty"`
	CustomAppId        *string        `json:"custom_app_id,omitempty"`
	CustomAppWidgetKey *string        `json:"custom_app_widget_key,omitempty"`
	IsEnabled          bool           `json:"is_enabled"`
	Payload            map[string]any `json:"payload,omitempty"`
	AccountId          string         `json:"account_id,omitempty"`
	CreatedAt          *Time          `json:"created_at,omitempty"`
	UpdatedAt          *Time          `json:"updated_at,omitempty"`
	DeletedAt          *Time          `json:"deleted_at,omitempty"`
}

// BoardItemKind mirrors the AccountBoardItemKind enum (Passport).
type BoardItemKind int

const (
	BoardItemKindWidget  BoardItemKind = 0
	BoardItemKindApp     BoardItemKind = 1
	BoardItemKindSection BoardItemKind = 2
)

// Relationship mirrors SnAccountRelationship (Shared Relationship.cs).
type Relationship struct {
	AccountId       string              `json:"account_id"`
	Account         *Account            `json:"account,omitempty"`
	RelatedId       string              `json:"related_id"`
	Related         *Account            `json:"related,omitempty"`
	ExpiredAt       *Time               `json:"expired_at,omitempty"`
	DegradeToStatus *RelationshipStatus `json:"degrade_to_status,omitempty"`
	Status          RelationshipStatus  `json:"status"`
	Alias           *string             `json:"alias,omitempty"`
	CreatedAt       *Time               `json:"created_at,omitempty"`
	UpdatedAt       *Time               `json:"updated_at,omitempty"`
	DeletedAt       *Time               `json:"deleted_at,omitempty"`
}

// RelationshipStatus mirrors RelationshipStatus.
type RelationshipStatus int

const (
	RelationshipPending     RelationshipStatus = 0
	RelationshipFriends     RelationshipStatus = 100
	RelationshipMuted       RelationshipStatus = -50
	RelationshipBlocked     RelationshipStatus = -100
	RelationshipCloseFriend RelationshipStatus = 200
)

// ActionLog mirrors SnActionLog (Shared ActionLog.cs). Action is a string
// constant from ActionLogType.
type ActionLog struct {
	Id        string         `json:"id"`
	Action    string         `json:"action"`
	Meta      map[string]any `json:"meta,omitempty"`
	UserAgent *string        `json:"user_agent,omitempty"`
	IpAddress *string        `json:"ip_address,omitempty"`
	Location  *GeoPoint      `json:"location,omitempty"`
	AccountId string         `json:"account_id"`
	SessionId *string        `json:"session_id,omitempty"`
	CreatedAt *Time          `json:"created_at,omitempty"`
	UpdatedAt *Time          `json:"updated_at,omitempty"`
	DeletedAt *Time          `json:"deleted_at,omitempty"`
}

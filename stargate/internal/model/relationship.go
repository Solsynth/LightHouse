package model

// Relationship mirrors SnAccountRelationship (Shared Account.cs).
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

// RelationshipStatus mirrors RelationshipStatus (Shared Account.cs).
type RelationshipStatus int

const (
	RelationshipPending     RelationshipStatus = 0
	RelationshipFriends     RelationshipStatus = 100
	RelationshipMuted       RelationshipStatus = -50
	RelationshipBlocked     RelationshipStatus = -100
	RelationshipCloseFriend RelationshipStatus = 200
)

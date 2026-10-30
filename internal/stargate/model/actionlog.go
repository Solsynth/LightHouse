package model

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

package model

// Auth models mirror DysonNetwork.Shared.Models.AuthSession.cs and related
// files. JSON keys match the Island SDK parsers (auth_session.g.dart).

// SessionType mirrors SessionType.
type SessionType int

const (
	SessionTypeLogin  SessionType = 0
	SessionTypeOAuth  SessionType = 1
	SessionTypeOidc   SessionType = 2
	SessionTypeApiKey SessionType = 3
)

// String returns the C# enum name.
func (t SessionType) String() string {
	switch t {
	case SessionTypeOAuth:
		return "OAuth"
	case SessionTypeOidc:
		return "Oidc"
	case SessionTypeApiKey:
		return "ApiKey"
	default:
		return "Login"
	}
}

// ClientPlatform mirrors ClientPlatform.
type ClientPlatform int

const (
	ClientPlatformUnidentified ClientPlatform = 0
	ClientPlatformWeb          ClientPlatform = 1
	ClientPlatformIos          ClientPlatform = 2
	ClientPlatformAndroid      ClientPlatform = 3
	ClientPlatformMacOs        ClientPlatform = 4
	ClientPlatformWindows      ClientPlatform = 5
	ClientPlatformLinux        ClientPlatform = 6
)

// AuthSession mirrors SnAuthSession.
type AuthSession struct {
	Id            string      `json:"id"`
	Label         *string     `json:"label,omitempty"`
	LastGrantedAt *Time       `json:"last_granted_at,omitempty"`
	ExpiredAt     *Time       `json:"expired_at,omitempty"`
	Audiences     []string    `json:"audiences,omitempty"`
	Scopes        []string    `json:"scopes,omitempty"`
	IpAddress     *string     `json:"ip_address,omitempty"`
	UserAgent     *string     `json:"user_agent,omitempty"`
	Location      *GeoPoint   `json:"location,omitempty"`
	Type          SessionType `json:"type"`
	AccountId     string      `json:"account_id"`
	CreatedAt     *Time       `json:"created_at,omitempty"`
	UpdatedAt     *Time       `json:"updated_at,omitempty"`
	DeletedAt     *Time       `json:"deleted_at,omitempty"`
	IsCurrent     bool        `json:"is_current,omitempty"`
	ChildrenCount *int        `json:"children_count,omitempty"`
	// ClientId/ParentSessionId/AppId/ChallengeId/Epoch are persisted columns
	// that are not part of the public wire shape.
	ClientId        *string `json:"-"`
	ParentSessionId *string `json:"-"`
	AppId           *string `json:"-"`
	ChallengeId     *string `json:"-"`
	Epoch           int     `json:"-"`
	// Account is populated server-side for enrichment.
	Account *Account `json:"-"`
}

// AuthChallenge mirrors SnAuthChallenge.
type AuthChallenge struct {
	Id                  string         `json:"id"`
	ExpiredAt           *Time          `json:"expired_at,omitempty"`
	StepRemain          int            `json:"step_remain"`
	StepTotal           int            `json:"step_total"`
	FailedAttempts      int            `json:"failed_attempts"`
	BlacklistFactors    []string       `json:"blacklist_factors,omitempty"`
	Audiences           []string       `json:"audiences,omitempty"`
	Scopes              []string       `json:"scopes,omitempty"`
	IpAddress           *string        `json:"ip_address,omitempty"`
	UserAgent           *string        `json:"user_agent,omitempty"`
	DeviceId            string         `json:"device_id"`
	DeviceName          *string        `json:"device_name,omitempty"`
	Platform            ClientPlatform `json:"platform"`
	Nonce               *string        `json:"nonce,omitempty"`
	Location            *GeoPoint      `json:"location,omitempty"`
	AccountId           string         `json:"account_id"`
	ApprovedAt          *Time          `json:"approved_at,omitempty"`
	DeclinedAt          *Time          `json:"declined_at,omitempty"`
	ApprovedBySessionId *string        `json:"approved_by_session_id,omitempty"`
	CreatedAt           *Time          `json:"created_at,omitempty"`
	UpdatedAt           *Time          `json:"updated_at,omitempty"`
	DeletedAt           *Time          `json:"deleted_at,omitempty"`
}

// AuthClient mirrors SnAuthClient (device).
type AuthClient struct {
	Id          string         `json:"id"`
	DeviceId    string         `json:"device_id"`
	DeviceName  string         `json:"device_name"`
	DeviceLabel *string        `json:"device_label,omitempty"`
	AccountId   string         `json:"account_id"`
	Platform    ClientPlatform `json:"platform"`
	IsCurrent   bool           `json:"is_current,omitempty"`
	CreatedAt   *Time          `json:"created_at,omitempty"`
	UpdatedAt   *Time          `json:"updated_at,omitempty"`
	DeletedAt   *Time          `json:"deleted_at,omitempty"`
}

// AuthClientWithSessions mirrors SnAuthClientWithSessions.
type AuthClientWithSessions struct {
	AuthClient
	Sessions []AuthSession `json:"sessions"`
}

// ApiKey mirrors SnApiKey (Shared ApiKey.cs).
type ApiKey struct {
	Id        string  `json:"id"`
	Label     string  `json:"label"`
	AccountId string  `json:"account_id"`
	AppId     *string `json:"app_id,omitempty"`
	SessionId string  `json:"-"`
	// Key is only populated on creation/rotation responses.
	Key       *string `json:"token,omitempty"`
	CreatedAt *Time   `json:"created_at,omitempty"`
	UpdatedAt *Time   `json:"updated_at,omitempty"`
	ExpiredAt *Time   `json:"expired_at,omitempty"`
	DeletedAt *Time   `json:"deleted_at,omitempty"`
}

// AuthorizedApp mirrors SnAuthorizedApp (Padlock Models AuthorizedApp.cs).
type AuthorizedApp struct {
	Id               string            `json:"id"`
	Type             AuthorizedAppType `json:"type"`
	AccountId        string            `json:"account_id"`
	AppId            string            `json:"app_id"`
	AppSlug          *string           `json:"app_slug,omitempty"`
	AppName          *string           `json:"app_name,omitempty"`
	Scopes           []string          `json:"scopes,omitempty"`
	LastAuthorizedAt *Time             `json:"last_authorized_at,omitempty"`
	LastUsedAt       *Time             `json:"last_used_at,omitempty"`
	CreatedAt        *Time             `json:"created_at,omitempty"`
	UpdatedAt        *Time             `json:"updated_at,omitempty"`
	DeletedAt        *Time             `json:"deleted_at,omitempty"`
}

// AuthorizedAppType mirrors AuthorizedAppType.
type AuthorizedAppType int

const (
	AuthorizedAppTypeOidc   AuthorizedAppType = 0
	AuthorizedAppTypeApiKey AuthorizedAppType = 1
)

// String returns the C# enum name.
func (t AuthorizedAppType) String() string {
	if t == AuthorizedAppTypeApiKey {
		return "ApiKey"
	}
	return "Oidc"
}

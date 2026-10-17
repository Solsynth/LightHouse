package model

// OAuthApp is the OAuth/OIDC client registration owned by Stargate's
// app.solian.id.oauthapps module. It mirrors the OAuth-relevant subset of
// Develop's SnCustomApp: everything the provider needs to authorize a
// request, plus the metadata it shows on the consent screen.
//
// Ownership: exactly one of ProjectID (a Develop dev project) and AccountID
// (an individual owner) is set. The latter lets a deployment run the OAuth
// provider without the Develop service.
type OAuthApp struct {
	Id          string `json:"id"`
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Status mirrors CustomAppStatus: 0 developing, 1 staging,
	// 2 production, 3 suspended. Only production enforces redirect URIs.
	Status      int      `json:"status"`
	ProjectId   *string  `json:"project_id,omitempty"`
	AccountId   *string  `json:"account_id,omitempty"`
	OauthConfig OAuthAppOauthConfig `json:"oauth_config"`
	Links       OAuthAppLinks       `json:"links"`
	CreatedAt   *Time               `json:"created_at,omitempty"`
	UpdatedAt   *Time               `json:"updated_at,omitempty"`
	DeletedAt   *Time               `json:"deleted_at,omitempty"`
}

// OAuthAppOauthConfig mirrors SnCustomAppOauthConfig.
type OAuthAppOauthConfig struct {
	ClientUri              string   `json:"client_uri,omitempty"`
	RedirectUris           []string `json:"redirect_uris"`
	PostLogoutRedirectUris []string `json:"post_logout_redirect_uris,omitempty"`
	AllowedScopes          []string `json:"allowed_scopes"`
	AllowedGrantTypes      []string `json:"allowed_grant_types"`
	RequirePkce            bool     `json:"require_pkce"`
	AllowOfflineAccess     bool     `json:"allow_offline_access"`
	IsPublicClient         bool     `json:"is_public_client"`
}

// OAuthAppLinks mirrors SnCustomAppLinks.
type OAuthAppLinks struct {
	HomePage       string `json:"home_page,omitempty"`
	PrivacyPolicy  string `json:"privacy_policy,omitempty"`
	TermsOfService string `json:"terms_of_service,omitempty"`
}

// OAuthAppSecret is a client secret or API key. Secret is the plaintext
// value and is populated only on create and rotate responses.
type OAuthAppSecret struct {
	Id          string `json:"id"`
	AppId       string `json:"app_id"`
	Secret      string `json:"secret,omitempty"`
	Description string `json:"description"`
	ExpiredAt   *Time  `json:"expired_at,omitempty"`
	IsOidc      bool   `json:"is_oidc"`
	CreatedAt   *Time  `json:"created_at,omitempty"`
	UpdatedAt   *Time  `json:"updated_at,omitempty"`
}

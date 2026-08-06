package wellknownctl

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Static OpenAPI 3.0.1 manifests served at /swagger/padlock/v1/swagger.json
// and /swagger/passport/v1/swagger.json for the web playground. The C#
// services serve Swashbuckle output at the same URLs; byte-identity with
// Swashbuckle is a documented plan deviation (dev tool only), so these are
// hand-built docs covering the ported endpoint surfaces. Paths are keyed
// /api/... exactly like the gateway-exposed routes.

type openAPIDoc struct {
	OpenAPI    string                 `json:"openapi"`
	Info       openAPIInfo            `json:"info"`
	Paths      map[string]openAPIPath `json:"paths"`
	Components openAPIComponents      `json:"components"`
}

type openAPIInfo struct {
	Title       string `json:"title"`
	Version     string `json:"version"`
	Description string `json:"description,omitempty"`
}

type openAPIPath map[string]openAPIOperation

type openAPIOperation struct {
	Summary    string                     `json:"summary,omitempty"`
	Tags       []string                   `json:"tags,omitempty"`
	Parameters []openAPIParameter         `json:"parameters,omitempty"`
	Responses  map[string]openAPIResponse `json:"responses"`
}

type openAPIParameter struct {
	Name     string        `json:"name"`
	In       string        `json:"in"`
	Required bool          `json:"required,omitempty"`
	Schema   openAPISchema `json:"schema"`
}

type openAPISchema struct {
	Type   string `json:"type,omitempty"`
	Format string `json:"format,omitempty"`
}

type openAPIResponse struct {
	Description string                      `json:"description"`
	Content     map[string]openAPIMediaType `json:"content,omitempty"`
}

type openAPIMediaType struct {
	Schema openAPISchemaRef `json:"schema"`
}

type openAPISchemaRef struct {
	Ref string `json:"$ref,omitempty"`
}

type openAPIComponents struct {
	Schemas map[string]openAPISchemaObject `json:"schemas,omitempty"`
}

type openAPISchemaObject struct {
	Type       string                   `json:"type,omitempty"`
	Properties map[string]openAPISchema `json:"properties,omitempty"`
}

// routeSpec is one entry of the static route tables below.
type routeSpec struct {
	path      string   // /api/... path template
	method    string   // GET / POST / PATCH / PUT / DELETE
	tag       string   // OpenAPI tag grouping
	summary   string   // human-readable operation summary
	params    []string // path parameter names ({id} → "id")
	notFound  bool     // document a 404 response
	forbidden bool     // document a 403 response (permission-gated route)
}

var standardErrorResponses = map[string]openAPIResponse{
	"400": apiErrorResponse("Bad request — validation or semantic error"),
	"401": apiErrorResponse("Unauthorized — missing or invalid token"),
}

func apiErrorResponse(desc string) openAPIResponse {
	return openAPIResponse{
		Description: desc,
		Content: map[string]openAPIMediaType{
			"application/json": {Schema: openAPISchemaRef{Ref: "#/components/schemas/ApiError"}},
		},
	}
}

func buildDoc(title, description string, routes []routeSpec) openAPIDoc {
	paths := make(map[string]openAPIPath)
	for _, r := range routes {
		responses := make(map[string]openAPIResponse, 5)
		for code, resp := range standardErrorResponses {
			responses[code] = resp
		}
		if r.forbidden {
			responses["403"] = apiErrorResponse("Forbidden — missing permission")
		}
		if r.notFound {
			responses["404"] = apiErrorResponse("Not found")
		}
		responses["200"] = openAPIResponse{Description: "Success"}

		op := openAPIOperation{
			Summary:   r.summary,
			Tags:      []string{r.tag},
			Responses: responses,
		}
		for _, p := range r.params {
			op.Parameters = append(op.Parameters, openAPIParameter{
				Name: p, In: "path", Required: true,
				Schema: openAPISchema{Type: "string"},
			})
		}
		pathOps, ok := paths[r.path]
		if !ok {
			pathOps = openAPIPath{}
			paths[r.path] = pathOps
		}
		pathOps[r.method] = op
	}

	return openAPIDoc{
		OpenAPI: "3.0.1",
		Info: openAPIInfo{
			Title:       title,
			Version:     "v1",
			Description: description,
		},
		Paths: paths,
		Components: openAPIComponents{
			Schemas: map[string]openAPISchemaObject{
				"ApiError": {
					Type: "object",
					Properties: map[string]openAPISchema{
						"code":    {Type: "string"},
						"message": {Type: "string"},
						"status":  {Type: "integer", Format: "int32"},
						"traceId": {Type: "string"},
					},
				},
			},
		},
	}
}

// serveSwagger returns a handler that serves one of the static manifests.
func serveSwagger(doc openAPIDoc) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, doc)
	}
}

// padlockSwagger covers the Padlock surface ported into Stargate: auth,
// registration, account security, QR login, OIDC provider, social login,
// API keys, connections and admin.
var padlockSwagger = buildDoc(
	"DysonNetwork.Padlock",
	"Padlock API — authentication, accounts, account security, OIDC provider, social login and admin. Ported from DysonNetwork.Padlock into Stargate.",
	[]routeSpec{
		{path: "/api/accounts", method: "POST", tag: "Accounts", summary: "Register a new account"},
		{path: "/api/accounts/validate", method: "POST", tag: "Accounts", summary: "Validate account name / email availability"},
		{path: "/api/accounts/me", method: "GET", tag: "Accounts", summary: "Get the current account identity"},
		{path: "/api/accounts/me", method: "PATCH", tag: "Accounts", summary: "Update basic account info (nick, language, region)", forbidden: true},
		{path: "/api/accounts/me/pin-status", method: "GET", tag: "Accounts", summary: "Get the current account PIN status"},

		{path: "/api/auth/challenge", method: "POST", tag: "Auth", summary: "Start an authentication challenge"},
		{path: "/api/auth/challenge/{id}", method: "GET", tag: "Auth", summary: "Get an authentication challenge", params: []string{"id"}, notFound: true},
		{path: "/api/auth/challenge/{id}", method: "PATCH", tag: "Auth", summary: "Verify an authentication factor step", params: []string{"id"}, notFound: true},
		{path: "/api/auth/challenge/{id}/factors", method: "GET", tag: "Auth", summary: "List the factors available for a challenge", params: []string{"id"}, notFound: true},
		{path: "/api/auth/challenge/{id}/factors/{factorId}", method: "POST", tag: "Auth", summary: "Request a factor code (SMS / email side-effect)", params: []string{"id", "factorId"}, notFound: true},
		{path: "/api/auth/challenge/{id}/passkey/start", method: "POST", tag: "Auth", summary: "Start a WebAuthn assertion for a challenge", params: []string{"id"}, notFound: true},
		{path: "/api/auth/challenge/{id}/passkey/complete", method: "POST", tag: "Auth", summary: "Complete a WebAuthn assertion for a challenge", params: []string{"id"}, notFound: true},
		{path: "/api/auth/challenge/pending", method: "GET", tag: "Auth", summary: "List pending cross-device challenges"},
		{path: "/api/auth/challenge/{id}/approve", method: "POST", tag: "Auth", summary: "Approve a cross-device challenge", params: []string{"id"}, notFound: true},
		{path: "/api/auth/challenge/{id}/decline", method: "POST", tag: "Auth", summary: "Decline a cross-device challenge", params: []string{"id"}, notFound: true},
		{path: "/api/auth/passkey/start", method: "POST", tag: "Auth", summary: "Start a discoverable passkey login"},
		{path: "/api/auth/passkey/{id}/complete", method: "POST", tag: "Auth", summary: "Complete a passkey login", params: []string{"id"}},
		{path: "/api/auth/token", method: "POST", tag: "Auth", summary: "Exchange an authorization code or refresh token for a token pair"},
		{path: "/api/auth/refresh", method: "POST", tag: "Auth", summary: "Refresh tokens via the RefreshToken cookie"},
		{path: "/api/auth/captcha", method: "POST", tag: "Auth", summary: "Validate a captcha token"},
		{path: "/api/auth/recover", method: "POST", tag: "Auth", summary: "Recover an account with a recovery code"},
		{path: "/api/auth/logout", method: "POST", tag: "Auth", summary: "Log out and revoke the current session"},
		{path: "/api/auth/login/session", method: "POST", tag: "Auth", summary: "Create a child session token pair"},
		{path: "/api/auth/sudo", method: "POST", tag: "Auth", summary: "Enable sudo mode for the current session"},

		{path: "/api/auth/qr/generate", method: "POST", tag: "QR Login", summary: "Generate a QR login challenge"},
		{path: "/api/auth/qr/{id}", method: "GET", tag: "QR Login", summary: "Get a QR challenge status", params: []string{"id"}, notFound: true},
		{path: "/api/auth/qr/{id}/scan", method: "POST", tag: "QR Login", summary: "Mark a QR challenge as scanned", params: []string{"id"}, notFound: true},
		{path: "/api/auth/qr/{id}/approve", method: "POST", tag: "QR Login", summary: "Approve a QR challenge", params: []string{"id"}, notFound: true},
		{path: "/api/auth/qr/{id}/decline", method: "POST", tag: "QR Login", summary: "Decline a QR challenge", params: []string{"id"}, notFound: true},

		{path: "/api/auth/captcha/verify", method: "POST", tag: "Captcha", summary: "Verify a captcha token"},
		{path: "/api/auth/captcha/config", method: "GET", tag: "Captcha", summary: "Get the captcha provider configuration"},
		{path: "/api/auth/webauthn/config", method: "GET", tag: "Captcha", summary: "Get the WebAuthn relying-party configuration"},

		{path: "/api/auth/open/authorize", method: "GET", tag: "OIDC Provider", summary: "OIDC authorize (browser consent page)"},
		{path: "/api/auth/open/authorize", method: "POST", tag: "OIDC Provider", summary: "OIDC authorize — approve the consent"},
		{path: "/api/auth/open/token", method: "POST", tag: "OIDC Provider", summary: "OIDC token endpoint (authorization_code / refresh_token / device_code)"},
		{path: "/api/auth/open/userinfo", method: "GET", tag: "OIDC Provider", summary: "OIDC userinfo for the bearer token"},
		{path: "/api/auth/open/device/code", method: "POST", tag: "OIDC Provider", summary: "Start the OIDC device authorization flow"},
		{path: "/api/auth/open/device/code/{userCode}", method: "GET", tag: "OIDC Provider", summary: "Poll the device code status", params: []string{"userCode"}, notFound: true},
		{path: "/api/auth/open/device/code/{userCode}/approve", method: "POST", tag: "OIDC Provider", summary: "Approve a device code", params: []string{"userCode"}, notFound: true},
		{path: "/api/auth/open/device/code/{userCode}/decline", method: "POST", tag: "OIDC Provider", summary: "Decline a device code", params: []string{"userCode"}, notFound: true},

		{path: "/api/auth/login/{provider}", method: "GET", tag: "Social Login", summary: "Redirect to a social login provider (google, apple, microsoft, steam, discord)", params: []string{"provider"}},
		{path: "/api/auth/login/apple/mobile", method: "POST", tag: "Social Login", summary: "Sign in with Apple (mobile identity token)"},
		{path: "/api/auth/callback/{provider}", method: "GET", tag: "Social Login", summary: "Social login callback", params: []string{"provider"}},
		{path: "/api/auth/connect/apple/mobile", method: "POST", tag: "Social Login", summary: "Connect an Apple account to the current user"},

		{path: "/api/factors", method: "GET", tag: "Security", summary: "List the current account's auth factors"},
		{path: "/api/factors", method: "POST", tag: "Security", summary: "Create an auth factor"},
		{path: "/api/factors/{id}/enable", method: "POST", tag: "Security", summary: "Enable an auth factor", params: []string{"id"}, notFound: true},
		{path: "/api/factors/{id}/disable", method: "POST", tag: "Security", summary: "Disable an auth factor", params: []string{"id"}, notFound: true},
		{path: "/api/factors/{id}", method: "DELETE", tag: "Security", summary: "Delete an auth factor", params: []string{"id"}, notFound: true},
		{path: "/api/factors/passkey/start", method: "POST", tag: "Security", summary: "Start passkey registration"},
		{path: "/api/factors/passkey/complete", method: "POST", tag: "Security", summary: "Complete passkey registration"},
		{path: "/api/factors/passkey", method: "GET", tag: "Security", summary: "List the current account's passkeys"},
		{path: "/api/factors/passkey/{id}", method: "PATCH", tag: "Security", summary: "Rename a passkey", params: []string{"id"}, notFound: true},
		{path: "/api/factors/passkey/{id}", method: "DELETE", tag: "Security", summary: "Delete a passkey", params: []string{"id"}, notFound: true},

		{path: "/api/sessions", method: "GET", tag: "Sessions", summary: "List the current account's sessions"},
		{path: "/api/sessions/{id}/children", method: "GET", tag: "Sessions", summary: "List a session's child sessions", params: []string{"id"}, notFound: true},
		{path: "/api/sessions/{id}", method: "DELETE", tag: "Sessions", summary: "Revoke a session", params: []string{"id"}, notFound: true},
		{path: "/api/sessions/current", method: "DELETE", tag: "Sessions", summary: "Revoke the current session"},
		{path: "/api/sessions/other", method: "DELETE", tag: "Sessions", summary: "Revoke all other sessions"},
		{path: "/api/devices", method: "GET", tag: "Devices", summary: "List the current account's devices"},
		{path: "/api/devices/{deviceId}", method: "DELETE", tag: "Devices", summary: "Delete a device", params: []string{"deviceId"}, notFound: true},
		{path: "/api/devices/other", method: "DELETE", tag: "Devices", summary: "Delete all other devices"},
		{path: "/api/devices/{deviceId}/label", method: "PATCH", tag: "Devices", summary: "Rename a device", params: []string{"deviceId"}, notFound: true},
		{path: "/api/devices/current/label", method: "PATCH", tag: "Devices", summary: "Rename the current device"},

		{path: "/api/contacts", method: "GET", tag: "Contacts", summary: "List the current account's contacts"},
		{path: "/api/contacts", method: "POST", tag: "Contacts", summary: "Add a contact"},
		{path: "/api/contacts/{id}/verify", method: "POST", tag: "Contacts", summary: "Verify a contact", params: []string{"id"}, notFound: true},
		{path: "/api/contacts/{id}/primary", method: "POST", tag: "Contacts", summary: "Set a contact as primary", params: []string{"id"}, notFound: true},
		{path: "/api/contacts/{id}/public", method: "POST", tag: "Contacts", summary: "Make a contact public", params: []string{"id"}, notFound: true},
		{path: "/api/contacts/{id}/public", method: "DELETE", tag: "Contacts", summary: "Make a contact private", params: []string{"id"}, notFound: true},
		{path: "/api/contacts/{id}", method: "DELETE", tag: "Contacts", summary: "Delete a contact", params: []string{"id"}, notFound: true},

		{path: "/api/authorized-apps", method: "GET", tag: "Authorized Apps", summary: "List the current account's authorized apps"},
		{path: "/api/authorized-apps/{id}/scopes", method: "POST", tag: "Authorized Apps", summary: "Update an authorized app's scopes", params: []string{"id"}, notFound: true, forbidden: true},
		{path: "/api/authorized-apps/{id}", method: "DELETE", tag: "Authorized Apps", summary: "Revoke an authorized app", params: []string{"id"}, notFound: true},

		{path: "/api/api-keys", method: "GET", tag: "API Keys", summary: "List the current account's API keys"},
		{path: "/api/api-keys", method: "POST", tag: "API Keys", summary: "Create an API key", forbidden: true},
		{path: "/api/api-keys/{id}", method: "DELETE", tag: "API Keys", summary: "Delete an API key", params: []string{"id"}, notFound: true},
		{path: "/api/api-keys/{id}/rotate", method: "POST", tag: "API Keys", summary: "Rotate an API key token", params: []string{"id"}, notFound: true},

		{path: "/api/connections", method: "GET", tag: "Connections", summary: "List the current account's social connections"},
		{path: "/api/connections/{id}", method: "DELETE", tag: "Connections", summary: "Delete a social connection", params: []string{"id"}, notFound: true},
		{path: "/api/connections/{id}/visibility", method: "POST", tag: "Connections", summary: "Update a connection's visibility", params: []string{"id"}, notFound: true},

		{path: "/api/identity", method: "GET", tag: "Security", summary: "Get the current account's security identity"},

		{path: "/api/admin/accounts", method: "GET", tag: "Admin", summary: "List accounts (admin)", forbidden: true},
		{path: "/api/admin/accounts/{name}/spells", method: "GET", tag: "Admin", summary: "List an account's magic spells (admin)", params: []string{"name"}, forbidden: true},
		{path: "/api/admin/accounts/{name}/spells", method: "POST", tag: "Admin", summary: "Create a magic spell for an account (admin)", params: []string{"name"}, forbidden: true},
		{path: "/api/admin/accounts/{name}/spells/{spellId}/resend", method: "POST", tag: "Admin", summary: "Resend an account's magic spell (admin)", params: []string{"name", "spellId"}, forbidden: true},
		{path: "/api/admin/accounts/{name}/spells/{spellId}", method: "DELETE", tag: "Admin", summary: "Delete an account's magic spell (admin)", params: []string{"name", "spellId"}, forbidden: true},
		{path: "/api/admin/accounts/notifications", method: "POST", tag: "Admin", summary: "Send a notification (admin)", forbidden: true},
		{path: "/api/admin/accounts/emails", method: "POST", tag: "Admin", summary: "Send an email (admin)", forbidden: true},
		{path: "/api/admin/permissions", method: "GET", tag: "Admin", summary: "List permission groups (admin)", forbidden: true},
		{path: "/api/admin/cache", method: "DELETE", tag: "Admin", summary: "Flush cache groups (admin)", forbidden: true},
		{path: "/api/admin/stats/users/geography", method: "GET", tag: "Admin", summary: "Account geography statistics (admin)", forbidden: true},
	},
)

// passportSwagger covers the Passport surface moved into Stargate: profile,
// public accounts, relationships and magic spells.
var passportSwagger = buildDoc(
	"DysonNetwork.Passport",
	"Passport API — profile, public accounts, relationships and magic spells. Ported from DysonNetwork.Passport into Stargate.",
	[]routeSpec{
		{path: "/api/accounts/me", method: "GET", tag: "Profile", summary: "Get the current account with hydrated profile, badges, perk subscription and contacts"},
		{path: "/api/accounts/me", method: "PATCH", tag: "Profile", summary: "Update basic account info (nick, language, region)"},
		{path: "/api/accounts/me", method: "DELETE", tag: "Profile", summary: "Request deletion of the current account"},
		{path: "/api/accounts/me/profile", method: "PATCH", tag: "Profile", summary: "Update the account profile (bio, links, picture, background, ...)"},
		{path: "/api/accounts/me/followers", method: "GET", tag: "Followers", summary: "List the current account's followers"},
		{path: "/api/accounts/me/following", method: "GET", tag: "Followers", summary: "List the accounts the current account follows"},

		{path: "/api/accounts/{name}", method: "GET", tag: "Public Accounts", summary: "Get a public account by name", params: []string{"name"}, notFound: true},
		{path: "/api/accounts/id/{id}", method: "GET", tag: "Public Accounts", summary: "Get a public account by id", params: []string{"id"}, notFound: true},
		{path: "/api/accounts/search", method: "GET", tag: "Public Accounts", summary: "Search public accounts by name"},
		{path: "/api/accounts/{name}/picture", method: "GET", tag: "Public Accounts", summary: "Redirect to the account's picture file", params: []string{"name"}, notFound: true},
		{path: "/api/accounts/{name}/background", method: "GET", tag: "Public Accounts", summary: "Redirect to the account's background file", params: []string{"name"}, notFound: true},
		{path: "/api/accounts/{name}/connections", method: "GET", tag: "Public Accounts", summary: "Get a public account's public connections", params: []string{"name"}, notFound: true},
		{path: "/api/accounts/{name}/followers", method: "GET", tag: "Public Accounts", summary: "List an account's followers", params: []string{"name"}, notFound: true},
		{path: "/api/accounts/{name}/following", method: "GET", tag: "Public Accounts", summary: "List the accounts an account follows", params: []string{"name"}, notFound: true},

		{path: "/api/relationships/{accountId}", method: "GET", tag: "Relationships", summary: "Get the relationship with an account", params: []string{"accountId"}, notFound: true},
		{path: "/api/relationships/{accountId}", method: "POST", tag: "Relationships", summary: "Create a relationship (friend request)", params: []string{"accountId"}, forbidden: true},
		{path: "/api/relationships/{accountId}", method: "PATCH", tag: "Relationships", summary: "Update a relationship", params: []string{"accountId"}, forbidden: true},
		{path: "/api/relationships/{accountId}", method: "DELETE", tag: "Relationships", summary: "Delete a relationship", params: []string{"accountId"}, forbidden: true},
		{path: "/api/relationships/{accountId}/friends", method: "POST", tag: "Relationships", summary: "Send a friend request", params: []string{"accountId"}, forbidden: true},
		{path: "/api/relationships/{accountId}/friends", method: "DELETE", tag: "Relationships", summary: "Remove a friend", params: []string{"accountId"}, forbidden: true},
		{path: "/api/relationships/{accountId}/friends/accept", method: "POST", tag: "Relationships", summary: "Accept a friend request", params: []string{"accountId"}, forbidden: true},
		{path: "/api/relationships/{accountId}/friends/decline", method: "POST", tag: "Relationships", summary: "Decline a friend request", params: []string{"accountId"}, forbidden: true},
		{path: "/api/relationships/requests", method: "GET", tag: "Relationships", summary: "List pending friend requests"},
		{path: "/api/relationships/{accountId}/block", method: "POST", tag: "Relationships", summary: "Block an account", params: []string{"accountId"}, forbidden: true},
		{path: "/api/relationships/{accountId}/block", method: "DELETE", tag: "Relationships", summary: "Unblock an account", params: []string{"accountId"}, forbidden: true},
		{path: "/api/relationships/{accountId}/mute", method: "POST", tag: "Relationships", summary: "Mute an account", params: []string{"accountId"}, forbidden: true},
		{path: "/api/relationships/{accountId}/mute", method: "DELETE", tag: "Relationships", summary: "Unmute an account", params: []string{"accountId"}, forbidden: true},
		{path: "/api/relationships/{accountId}/close-friend", method: "POST", tag: "Relationships", summary: "Mark an account as close friend", params: []string{"accountId"}, forbidden: true},
		{path: "/api/relationships/{accountId}/close-friend", method: "DELETE", tag: "Relationships", summary: "Unmark an account as close friend", params: []string{"accountId"}, forbidden: true},
		{path: "/api/relationships/close-friends", method: "GET", tag: "Relationships", summary: "List close friends"},
		{path: "/api/relationships/{accountId}/alias", method: "PATCH", tag: "Relationships", summary: "Set a relationship alias", params: []string{"accountId"}, forbidden: true},
		{path: "/api/relationships/{accountId}/mutual-friends", method: "GET", tag: "Relationships", summary: "List mutual friends", params: []string{"accountId"}},
		{path: "/api/relationships/sync", method: "POST", tag: "Relationships", summary: "Incremental relationship sync"},
		{path: "/api/relationships/inspect/{accountId}", method: "GET", tag: "Relationships", summary: "Inspect the relationship status with an account", params: []string{"accountId"}},

		{path: "/api/spells/contact-verification/resend", method: "POST", tag: "Spells", summary: "Resend the current account's contact-verification spell", forbidden: true},
		{path: "/api/spells/{word}", method: "GET", tag: "Spells", summary: "Get a magic spell by its secret word", params: []string{"word"}, notFound: true},
		{path: "/api/spells/{word}/apply", method: "POST", tag: "Spells", summary: "Apply a magic spell (verify contact / reset password)", params: []string{"word"}, notFound: true},
		{path: "/api/spells/{id}/resend", method: "POST", tag: "Spells", summary: "Resend a magic spell by id", params: []string{"id"}, notFound: true},
	},
)

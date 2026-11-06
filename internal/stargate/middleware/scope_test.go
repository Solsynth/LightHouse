package middleware

import (
	"context"
	"testing"

	"src.solsynth.dev/sosys/stargate/model"
)

// TestScopeAllowsContextOnlyFiltersOAuthSessions pins which sessions the scope
// gate applies to: OAuth app sessions are narrowed by the scopes the app was
// granted, every other session type carries no app grant and passes.
func TestScopeAllowsContextOnlyFiltersOAuthSessions(t *testing.T) {
	tests := []struct {
		name   string
		typ    model.SessionType
		scopes []string
		key    string
		want   bool
		noSess bool
	}{
		{name: "login wildcard is not a filter", typ: model.SessionTypeLogin, scopes: []string{"*"}, key: "accounts.manage", want: true},
		{name: "login narrow scope is not a filter", typ: model.SessionTypeLogin, scopes: []string{"chat.read"}, key: "accounts.manage", want: true},
		{name: "oidc is not filtered", typ: model.SessionTypeOidc, scopes: nil, key: "accounts.manage", want: true},
		{name: "api key is not filtered", typ: model.SessionTypeApiKey, scopes: []string{"chat.read"}, key: "accounts.manage", want: true},
		{name: "oauth wildcard covers everything", typ: model.SessionTypeOAuth, scopes: []string{"*"}, key: "accounts.manage", want: true},
		{name: "oauth exact scope", typ: model.SessionTypeOAuth, scopes: []string{"accounts.manage"}, key: "accounts.manage", want: true},
		{name: "oauth prefix scope", typ: model.SessionTypeOAuth, scopes: []string{"accounts.*"}, key: "accounts.manage", want: true},
		{name: "oauth omission denies", typ: model.SessionTypeOAuth, scopes: []string{"chat.read"}, key: "accounts.manage", want: false},
		{name: "oauth empty scope list denies", typ: model.SessionTypeOAuth, key: "accounts.manage", want: false},
		{name: "no session is not filtered", noSess: true, key: "accounts.manage", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			if !tt.noSess {
				ctx = WithCurrentSession(ctx, &model.AuthSession{Type: tt.typ, Scopes: tt.scopes})
			}
			if got := ScopeAllowsContext(ctx, tt.key); got != tt.want {
				t.Fatalf("ScopeAllowsContext = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestTokenScopeAllows pins the filter semantics: a scope narrows what a
// token may do and never grants. "*" matches every key, presets match nothing,
// and an empty list matches nothing.
func TestTokenScopeAllows(t *testing.T) {
	tests := []struct {
		name   string
		scopes []string
		key    string
		want   bool
	}{
		{name: "wildcard matches any key", scopes: []string{"*"}, key: "accounts.manage", want: true},
		{name: "exact key", scopes: []string{"accounts.manage"}, key: "accounts.manage", want: true},
		{name: "case insensitive key", scopes: []string{"Accounts.Manage"}, key: "accounts.manage", want: true},
		{name: "prefix wildcard", scopes: []string{"accounts.*"}, key: "accounts.manage", want: true},
		{name: "prefix wildcard mismatch", scopes: []string{"posts.*"}, key: "accounts.manage", want: false},
		{name: "unrelated scope", scopes: []string{"chat.read"}, key: "accounts.manage", want: false},
		{name: "empty list", scopes: nil, key: "accounts.manage", want: false},
		{name: "preset only", scopes: []string{"openid", "profile", "email"}, key: "openid", want: false},
		{name: "preset does not hide exact key", scopes: []string{"openid", "accounts.manage"}, key: "accounts.manage", want: true},
		{name: "blank key", scopes: []string{"*"}, key: "  ", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TokenScopeAllows(tt.scopes, tt.key); got != tt.want {
				t.Fatalf("TokenScopeAllows(%v, %q) = %v, want %v", tt.scopes, tt.key, got, tt.want)
			}
		})
	}
}

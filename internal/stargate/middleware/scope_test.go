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

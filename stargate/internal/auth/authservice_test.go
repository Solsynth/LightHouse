package auth

import (
	"slices"
	"testing"
)

// TestScopesOrEmpty pins the login scope contract: a login session carries only
// what the client asked for — never a full-grant wildcard, which
// PermissionScopeGate.HasFullScope honours for OAuth sessions only — and a nil
// list is materialized as an empty array because auth_sessions.scopes is jsonb
// NOT NULL.
func TestScopesOrEmpty(t *testing.T) {
	tests := []struct {
		name   string
		scopes []string
		want   []string
	}{
		{name: "nil becomes empty", scopes: nil, want: []string{}},
		{name: "empty stays empty", scopes: []string{}, want: []string{}},
		{name: "requested scopes pass through", scopes: []string{"openid", "profile"}, want: []string{"openid", "profile"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := scopesOrEmpty(tt.scopes)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("scopesOrEmpty(%v) = %v, want %v", tt.scopes, got, tt.want)
			}
			if got == nil {
				t.Fatal("scopesOrEmpty returned a nil slice: jsonb would store null")
			}
		})
	}
}

func TestMergeAuthorizedAppScopesOnlyExpands(t *testing.T) {
	existing := []string{"openid", "profile"}
	requested := []string{" profile ", "email", ""}

	got := mergeAuthorizedAppScopes(existing, requested)
	want := []string{"openid", "profile", "email"}
	if !slices.Equal(got, want) {
		t.Fatalf("mergeAuthorizedAppScopes(%v, %v) = %v, want %v", existing, requested, got, want)
	}
	if !slices.Equal(existing, []string{"openid", "profile"}) {
		t.Fatalf("existing scopes mutated: %v", existing)
	}
	if !slices.Equal(requested, []string{" profile ", "email", ""}) {
		t.Fatalf("requested scopes mutated: %v", requested)
	}

	if got := mergeAuthorizedAppScopes([]string{"openid"}, nil); !slices.Equal(got, []string{"openid"}) {
		t.Fatalf("fewer requested scopes removed stored scope: %v", got)
	}
}

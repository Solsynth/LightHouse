package auth

import (
	"slices"
	"testing"
)

// TestScopesWithFullScope pins the normal-login scope contract: the session
// scopes always contain the full-grant wildcard "*" (PermissionScopeGate
// HasFullScope), never duplicated, and the input slice is not mutated.
func TestScopesWithFullScope(t *testing.T) {
	tests := []struct {
		name   string
		scopes []string
		want   []string
	}{
		{name: "nil", scopes: nil, want: []string{"*"}},
		{name: "empty", scopes: []string{}, want: []string{"*"}},
		{name: "appends to requested scopes", scopes: []string{"openid", "profile"}, want: []string{"openid", "profile", "*"}},
		{name: "keeps existing wildcard", scopes: []string{"*"}, want: []string{"*"}},
		{name: "keeps existing wildcard in place", scopes: []string{"openid", "*", "email"}, want: []string{"openid", "*", "email"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := append([]string{}, tt.scopes...)
			got := scopesWithFullScope(tt.scopes)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("scopesWithFullScope(%v) = %v, want %v", tt.scopes, got, tt.want)
			}
			if !slices.Equal(tt.scopes, original) {
				t.Fatalf("input slice mutated: got %v, want %v", tt.scopes, original)
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

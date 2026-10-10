package auth

import (
	"context"
	"slices"
	"testing"

	"src.solsynth.dev/sosys/stargate/internal/config"
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

// TestValidateCaptchaFailsClosed pins the captcha policy: an unusable verifier
// is a misconfiguration, never a pass. Only the explicit [captcha]
// allow_disabled opt-out short-circuits to valid.
func TestValidateCaptchaFailsClosed(t *testing.T) {
	cases := []struct {
		name    string
		cfg     *config.Config
		want    bool
		wantErr bool
	}{
		{
			name:    "nil config",
			cfg:     nil,
			want:    false,
			wantErr: true,
		},
		{
			name:    "unconfigured without opt-out",
			cfg:     config.Default(), // skip = true, no secret, no opt-out
			want:    false,
			wantErr: true,
		},
		{
			name: "explicit opt-out",
			cfg: func() *config.Config {
				c := config.Default()
				c.Captcha.AllowDisabled = true
				return c
			}(),
			want:    true,
			wantErr: false,
		},
		{
			name: "configured verifier rejects an empty token",
			cfg: func() *config.Config {
				c := config.Default()
				c.Captcha.Skip = false
				c.Captcha.APISecret = "secret"
				return c
			}(),
			want:    false,
			wantErr: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &AuthService{cfg: tc.cfg}
			got, err := svc.ValidateCaptcha(context.Background(), "")
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, want error: %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("valid = %v, want %v", got, tc.want)
			}
		})
	}
}

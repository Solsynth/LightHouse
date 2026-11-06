package auth

import "testing"

// TestScopeAllows pins scope filter semantics: a scope narrows what a token
// may do and never grants. "*" matches every key, presets match nothing, and
// an empty list matches nothing.
func TestScopeAllows(t *testing.T) {
	tests := []struct {
		name  string
		scope []string
		key   string
		want  bool
	}{
		{name: "wildcard matches any key", scope: []string{"*"}, key: "accounts.manage", want: true},
		{name: "exact key", scope: []string{"accounts.manage"}, key: "accounts.manage", want: true},
		{name: "case insensitive key", scope: []string{"Accounts.Manage"}, key: "accounts.manage", want: true},
		{name: "prefix wildcard", scope: []string{"accounts.*"}, key: "accounts.manage", want: true},
		{name: "prefix wildcard mismatch", scope: []string{"posts.*"}, key: "accounts.manage", want: false},
		{name: "unrelated scope", scope: []string{"chat.read"}, key: "accounts.manage", want: false},
		{name: "empty list", scope: nil, key: "accounts.manage", want: false},
		{name: "preset only", scope: []string{"openid", "profile", "email"}, key: "openid", want: false},
		{name: "preset does not hide exact key", scope: []string{"openid", "accounts.manage"}, key: "accounts.manage", want: true},
		{name: "blank key", scope: []string{"*"}, key: "  ", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ScopeAllows(tt.scope, tt.key); got != tt.want {
				t.Fatalf("ScopeAllows(%v, %q) = %v, want %v", tt.scope, tt.key, got, tt.want)
			}
		})
	}
}

// TestMatchedScopeReportsTheMatch keeps the logged/returned scope honest.
func TestMatchedScopeReportsTheMatch(t *testing.T) {
	if matched, ok := MatchedScope([]string{"openid", "*"}, "accounts.manage"); !ok || matched != "*" {
		t.Fatalf("MatchedScope wildcard = %q, %v", matched, ok)
	}
	if matched, ok := MatchedScope([]string{"accounts.*"}, "accounts.manage"); !ok || matched != "accounts.*" {
		t.Fatalf("MatchedScope prefix = %q, %v", matched, ok)
	}
	if matched, ok := MatchedScope([]string{"chat.read"}, "accounts.manage"); ok || matched != "" {
		t.Fatalf("MatchedScope unrelated = %q, %v", matched, ok)
	}
}

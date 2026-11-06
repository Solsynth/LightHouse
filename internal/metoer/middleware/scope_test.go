package middleware

import "testing"

// TestScopeAllows pins the filter semantics: a scope narrows what a token may
// do and never grants. "*" matches every key, presets match nothing, and an
// empty list matches nothing.
func TestScopeAllows(t *testing.T) {
	tests := []struct {
		name   string
		scopes []string
		key    string
		want   bool
	}{
		{name: "wildcard matches any key", scopes: []string{"*"}, key: "notifications.send", want: true},
		{name: "exact key", scopes: []string{"notifications.send"}, key: "notifications.send", want: true},
		{name: "case insensitive key", scopes: []string{"Notifications.Send"}, key: "notifications.send", want: true},
		{name: "prefix wildcard", scopes: []string{"notifications.*"}, key: "notifications.send", want: true},
		{name: "prefix wildcard mismatch", scopes: []string{"chat.*"}, key: "notifications.send", want: false},
		{name: "unrelated scope", scopes: []string{"chat.read"}, key: "notifications.send", want: false},
		{name: "empty list", scopes: nil, key: "notifications.send", want: false},
		{name: "preset only", scopes: []string{"openid", "profile", "email"}, key: "openid", want: false},
		{name: "preset does not hide exact key", scopes: []string{"openid", "notifications.send"}, key: "notifications.send", want: true},
		{name: "blank key", scopes: []string{"*"}, key: "  ", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ScopeAllows(tt.scopes, tt.key); got != tt.want {
				t.Fatalf("ScopeAllows(%v, %q) = %v, want %v", tt.scopes, tt.key, got, tt.want)
			}
		})
	}
}

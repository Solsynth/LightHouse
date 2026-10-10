package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestClientIPTrustedProxyHops pins the X-Forwarded-For trust model: the
// client IP is counted from the RIGHT by the configured hop count, so a
// client-supplied leftmost entry can never choose the address fail2ban and
// the per-IP quotas see.
func TestClientIPTrustedProxyHops(t *testing.T) {
	original := int(trustedProxyHops.Load())
	t.Cleanup(func() { SetTrustedProxyHops(original) })

	newRequest := func(xff string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = "10.0.0.9:4321"
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		return r
	}

	cases := []struct {
		name string
		hops int
		xff  string
		want string
	}{
		{name: "no header falls back to the peer", hops: 1, xff: "", want: "10.0.0.9"},
		{name: "one trusted hop uses the rightmost entry", hops: 1, xff: "203.0.113.6", want: "203.0.113.6"},
		{
			name: "one trusted hop ignores the client-supplied leftmost entry",
			hops: 1, xff: "9.9.9.9, 198.51.100.7", want: "198.51.100.7",
		},
		{name: "two trusted hops", hops: 2, xff: "9.9.9.9, 198.51.100.7", want: "9.9.9.9"},
		{
			name: "blank entries are dropped",
			hops: 1, xff: "9.9.9.9, , 198.51.100.7 ", want: "198.51.100.7",
		},
		{
			name: "a chain shorter than the trusted depth still uses the leftmost",
			hops: 2, xff: "198.51.100.7", want: "198.51.100.7",
		},
		{name: "zero hops clamps to one", hops: 0, xff: "9.9.9.9, 198.51.100.7", want: "198.51.100.7"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			SetTrustedProxyHops(tc.hops)
			if got := ClientIP(newRequest(tc.xff)); got != tc.want {
				t.Fatalf("ClientIP(hops=%d, xff=%q) = %q, want %q", tc.hops, tc.xff, got, tc.want)
			}
		})
	}
}

package auth

import (
	"strconv"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// TestTokenExpired pins the exp claim check: numeric and string encodings
// (the C# minting serializes claims as strings) must both be honored, and a
// missing claim must not be treated as expired.
func TestTokenExpired(t *testing.T) {
	now := time.Now().Unix()
	cases := []struct {
		name   string
		claims jwt.MapClaims
		want   bool
	}{
		{"number expired", jwt.MapClaims{"exp": float64(now - 10)}, true},
		{"string expired", jwt.MapClaims{"exp": strconv.FormatInt(now-10, 10)}, true},
		{"number future", jwt.MapClaims{"exp": float64(now + 3600)}, false},
		{"string future", jwt.MapClaims{"exp": strconv.FormatInt(now+3600, 10)}, false},
		{"missing", jwt.MapClaims{}, false},
		{"garbage", jwt.MapClaims{"exp": "soon"}, false},
	}
	for _, tc := range cases {
		if got := tokenExpired(tc.claims); got != tc.want {
			t.Errorf("tokenExpired(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

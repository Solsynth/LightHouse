package model

import (
	"strings"
	"time"
)

// SecurityMode is a per-account security preference that overrides the
// default risk calculation.
type SecurityMode int

const (
	SecurityModeDefault  SecurityMode = 0
	SecurityModeLockdown SecurityMode = 1
	SecurityModeLockoff  SecurityMode = 2
)

// String returns the lowercase name of the mode.
func (m SecurityMode) String() string {
	switch m {
	case SecurityModeLockdown:
		return "lockdown"
	case SecurityModeLockoff:
		return "lockoff"
	default:
		return "default"
	}
}

// Parse converts a lowercase string to a SecurityMode.
func (m SecurityMode) Parse(s string) (SecurityMode, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "lockdown":
		return SecurityModeLockdown, true
	case "lockoff":
		return SecurityModeLockoff, true
	case "default":
		return SecurityModeDefault, true
	default:
		return SecurityModeDefault, false
	}
}

// --- Trust helpers (pure; no cycle risk since model has no deps) ---

// IsNative reports whether the platform represents a non-web native app.
func (p ClientPlatform) IsNative() bool {
	return p != ClientPlatformWeb && p != ClientPlatformUnidentified
}

// SessionCategory returns "browser" for Web sessions and "device" for all
// others (native apps + unidentified).
func SessionCategory(platform ClientPlatform) string {
	if platform == ClientPlatformWeb {
		return "browser"
	}
	return "device"
}

// SessionTrusted is the single trust predicate. A session is trusted iff:
//  - the platform is native (Web and Unidentified are never trusted), and
//  - lastGrantedAt is non-nil, and
//  - now - lastGrantedAt <= maxGap.
func SessionTrusted(platform ClientPlatform, lastGrantedAt *Time, now time.Time, maxGap time.Duration) bool {
	if !platform.IsNative() {
		return false
	}
	if lastGrantedAt == nil {
		return false
	}
	return now.Sub(lastGrantedAt.Time()) <= maxGap
}

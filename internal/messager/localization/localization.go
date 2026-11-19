// Package localization contains the notification copy Messager sends through
// Ring. The keys and locale fallback match the DysonNetwork fleet's JSON
// localization service.
package localization

import "strings"

var messages = map[string]map[string]string{
	"en": {
		"chatEncryptedMessageBody": "You've received a message",
	},
	"zh-hans": {
		"chatEncryptedMessageBody": "您收到了一条消息",
	},
}

// Localize returns the requested message with named placeholders replaced.
// Languages use the same normalization and English fallback as the fleet:
// zh-* maps to zh-hans, en-* maps to en, and unsupported languages use en.
func Localize(language, key string, args map[string]string) string {
	locale := NormalizeLocale(language)
	text, ok := messages[locale][key]
	if !ok {
		text, ok = messages["en"][key]
	}
	if !ok {
		text = key
	}
	for name, value := range args {
		text = strings.ReplaceAll(text, "{"+name+"}", value)
	}
	return text
}

// NormalizeLocale maps an account language onto a supported locale.
func NormalizeLocale(language string) string {
	language = strings.ToLower(strings.TrimSpace(language))
	switch {
	case strings.HasPrefix(language, "zh"):
		return "zh-hans"
	case strings.HasPrefix(language, "en"):
		return "en"
	default:
		return "en"
	}
}

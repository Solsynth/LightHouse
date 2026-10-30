// Package localization contains the notification copy shared by Stargate's
// outbound Ring messages. The keys and locale fallback match the DysonNetwork
// fleet's JSON localization service.
package localization

import "strings"

var messages = map[string]map[string]string{
	"en": {
		"friendRequestTitle":                    "{sender} requested to be your friend",
		"friendRequestBody":                     "You can go to relationships page and decide accept their request or not.",
		"newLoginTitle":                         "New login detected",
		"newLoginBody":                          "Your account was signed in on a device named {deviceName} from {ipAddress}",
		"loginAttemptTitle":                     "Login attempt detected",
		"loginAttemptBody":                      "Someone is trying to sign in to your account from a device named {deviceName} at {ipAddress}",
		"loginApprovedTitle":                    "Login approved",
		"loginApprovedBody":                     "Your sign-in from {deviceName} was approved on another device",
		"loginDeclinedTitle":                    "Login declined",
		"loginDeclinedBody":                     "Your sign-in from {deviceName} was declined on another device",
		"authCodeTitle":                         "Disposable Verification Code",
		"authCodeBody":                          "{code} is your verification code. It expires in 5 minutes.",
		"punishmentTitle":                       "Your account has a new action",
		"punishmentTitlePermissionModification": "Permissions Restricted",
		"punishmentTitleBlockLogin":             "Login Blocked",
		"punishmentTitleDisableAccount":         "Account Disabled",
		"punishmentTitleStrike":                 "Warning Issued",
		"punishmentBody":                        "Reason: {reason}",
		"punishmentBodyWithExpiry":              "Reason: {reason}\nExpires: {expiredAt}",
		"punishmentLiftedTitle":                 "Restriction Lifted",
		"punishmentLiftedBody":                  "Your {type} restriction has been lifted",
	},
	"zh-hans": {
		"friendRequestTitle":                    "{sender} 请求成为您的朋友",
		"friendRequestBody":                     "您可以前往关系页面决定接受或拒绝其请求。",
		"newLoginTitle":                         "检测到新登录",
		"newLoginBody":                          "您的账户已在名为 {deviceName} 的设备上登录，IP 地址为 {ipAddress}",
		"loginAttemptTitle":                     "检测到登录尝试",
		"loginAttemptBody":                      "有人正尝试从名为 {deviceName} 的设备（IP 地址 {ipAddress}）登录您的账户",
		"loginApprovedTitle":                    "登录已批准",
		"loginApprovedBody":                     "您在另一台设备上的登录请求已由 {deviceName} 批准",
		"loginDeclinedTitle":                    "登录已拒绝",
		"loginDeclinedBody":                     "您在另一台设备上的登录请求已被 {deviceName} 拒绝",
		"authCodeTitle":                         "一次性验证码",
		"authCodeBody":                          "{code} 是您的一次性验证码，将在 5 分钟后过期。",
		"punishmentTitle":                       "您的账户有一条新的处罚",
		"punishmentTitlePermissionModification": "权限受限",
		"punishmentTitleBlockLogin":             "登陆已禁用",
		"punishmentTitleDisableAccount":         "账户已停用",
		"punishmentTitleStrike":                 "警告已发出",
		"punishmentBody":                        "原因：{reason}",
		"punishmentBodyWithExpiry":              "原因：{reason}\n过期时间：{expiredAt}",
		"punishmentLiftedTitle":                 "限制已解除",
		"punishmentLiftedBody":                  "您的 {type} 限制已被解除",
	},
}

// Localize returns the requested message with named placeholders replaced.
// Languages use the same normalization and English fallback as the fleet:
// zh-* maps to zh-hans, en-* maps to en, and unsupported languages use en.
func Localize(language, key string, args map[string]string) string {
	locale := normalizeLocale(language)
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

func normalizeLocale(language string) string {
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

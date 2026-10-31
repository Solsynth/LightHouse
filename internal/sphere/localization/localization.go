// Package localization contains the notification copy shared by Sphere's
// outbound Ring messages, matching DysonNetwork.Sphere's JSON localization resources.
package localization

import "strings"

var messages = map[string]map[string]string{
	"en": {
		"postSharedMedia":            "Shared {count} media",
		"postMentionTitle":           "{user} mentioned you in a post",
		"postMentionBody":            "{user} mentioned you in a post: {content}",
		"postReplyTitle":             "{user} replied to your post",
		"postReplyBody":              "{user} replied: {content}",
		"postReplyContentBody":       "{user} replied to post \"{title}\": {content}",
		"postReactTitle":             "{user} reacted to your post",
		"postReactBody":              "{user} reacted with {reaction}",
		"postReactContentBody":       "{user} reacted to \"{title}\" with {reaction}",
		"postSubscriptionTitle":      "{publisher} posted",
		"postCloseFriendTitle":       "{publisher} posted for close friends",
		"postAwardedTitle":           "{user} awarded your post",
		"postAwardedBody":            "{user} awarded {amount} credits",
		"postAwardedContentBody":     "{user} awarded {amount} credits to \"{title}\"",
		"publishingRewardTitle":      "Achievement for publisher {publisher} on {date}",
		"liveStreamStartedTitle":     "{publisher} is now live",
		"followRequestReceivedTitle": "{user} wants to follow your publisher {publisher}",
		"followRequestReceivedBody":  "{user} wants to follow your publisher {publisher}",
		"followRequestApprovedTitle": "Follow request approved",
		"followRequestApprovedBody":  "{publisher} approved your follow request",
		"followRequestRejectedTitle": "Follow request rejected",
		"followRequestRejectedBody":  "{publisher} rejected your follow request",
		"subscriberAddedTitle":       "You've been added to {publisher}",
		"subscriberAddedBody":        "{publisher} has added you as a subscriber",
		"postRemovedFromRealmTitle":  "Your post was removed from a realm",
		"postRemovedFromRealmBody":   "Your post was removed from the realm. Reason: {reason}",
		"posts.award.distribution":   "Post award settlement for {publisher}",
		"posts.sponsor.remarks":      "Sponsor post {title}",
	},
	"zh-hans": {
		"postSharedMedia":            "分享了 {count} 个媒体文件",
		"postMentionTitle":           "{user} 在帖子中提到了您",
		"postMentionBody":            "{user} 在帖子中提到了您 {content}",
		"postReplyTitle":             "{user} 回复了您的帖子",
		"postReplyBody":              "{user} 回复：{content}",
		"postReplyContentBody":       "{user} 回复了帖子 \"{title}\"：{content}",
		"postReactTitle":             "{user} 对您的帖子做出了反应",
		"postReactBody":              "{user} 用 {reaction} 做出了反应",
		"postReactContentBody":       "{user} 用 {reaction} 对 \"{title}\" 做出了反应",
		"postSubscriptionTitle":      "{publisher} 发布了帖子",
		"postCloseFriendTitle":       "{publisher} 发布了密友帖子",
		"postAwardedTitle":           "{user} 打赏了您的帖子",
		"postAwardedBody":            "{user} 打赏了 {amount} 积分",
		"postAwardedContentBody":     "{user} 打赏了 {amount} 积分给 \"{title}\"",
		"publishingRewardTitle":      "发布者 {publisher} 于 {date} 的成就",
		"liveStreamStartedTitle":     "{publisher} 正在直播",
		"followRequestReceivedTitle": "{user} 想要关注您的发布者 {publisher}",
		"followRequestReceivedBody":  "{user} 想要关注您的发布者 {publisher}",
		"followRequestApprovedTitle": "关注请求已通过",
		"followRequestApprovedBody":  "{publisher} 已通过您的关注请求",
		"followRequestRejectedTitle": "关注请求被拒绝",
		"followRequestRejectedBody":  "{publisher} 已拒绝您的关注请求",
		"subscriberAddedTitle":       "您已被添加到 {publisher}",
		"subscriberAddedBody":        "{publisher} 已将您添加为订阅者",
		"postRemovedFromRealmTitle":  "您的帖子已从领域中移除",
		"postRemovedFromRealmBody":   "您的帖子已从领域中移除。原因：{reason}",
		"posts.award.distribution":   "发布者 {publisher} 的帖子打赏结算",
		"posts.sponsor.remarks":      "赞助帖子 {title}",
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

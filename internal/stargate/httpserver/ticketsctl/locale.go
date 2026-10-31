package ticketsctl

import "strings"

// Ticket notification strings copied from Passport's en.json / zh-hans.json
// under the same keys, so the wire notifications keep their wording.
var ticketTranslations = map[string]map[string]string{
	"en": {
		"ticketStatusUpdatedTitle": "Ticket Status Updated",
		"ticketStatusUpdatedBody":  `Ticket "{ticketTitle}" status changed from {oldStatus} to {newStatus} by {updaterName}`,
		"ticketAssignedTitle":      "Ticket Assigned",
		"ticketAssignedBody":       `Ticket "{ticketTitle}" has been assigned to {assigneeName} by {assignerName}`,
		"ticketNewMessageTitle":    "New Ticket Reply",
		"ticketNewMessageBody":     `{senderName} replied to ticket "{ticketTitle}"`,
		"ticketCreatedTitle":       "New Ticket Created",
		"ticketCreatedBody":        `{creatorName} created a new ticket "{ticketTitle}"`,
	},
	"zh-hans": {
		"ticketStatusUpdatedTitle": "工单状态已更新",
		"ticketStatusUpdatedBody":  `工单 "{ticketTitle}" 状态已从 {oldStatus} 变更为 {newStatus}，由 {updaterName} 操作`,
		"ticketAssignedTitle":      "工单已分配",
		"ticketAssignedBody":       `工单 "{ticketTitle}" 已分配给 {assigneeName}，由 {assignerName} 操作`,
		"ticketNewMessageTitle":    "新工单回复",
		"ticketNewMessageBody":     `{senderName} 回复了工单 "{ticketTitle}"`,
		"ticketCreatedTitle":       "新工单已创建",
		"ticketCreatedBody":        `{creatorName} 创建了新工单 "{ticketTitle}"`,
	},
}

func localeKey(language string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(language)), "zh") {
		return "zh-hans"
	}
	return "en"
}

// localize resolves a notification key for the account language and applies
// the {placeholder} arguments.
func localize(language, key string, args map[string]string) string {
	table, ok := ticketTranslations[localeKey(language)]
	if !ok {
		table = ticketTranslations["en"]
	}
	template, ok := table[key]
	if !ok {
		return key
	}
	for name, value := range args {
		template = strings.ReplaceAll(template, "{"+name+"}", value)
	}
	return template
}

package push

import (
	"src.solsynth.dev/sosys/metoer/internal/model"
)

// fcmPayload mirrors the CorePush FirebaseSender.SendAsync payload dict
// (the FCM v1 messages:send body): {"message":{"token","notification"}}.
func fcmPayload(deviceToken, title, body string) map[string]any {
	return map[string]any{
		"message": map[string]any{
			"token": deviceToken,
			"notification": map[string]any{
				"title": title,
				"body":  body,
			},
		},
	}
}

// applePayload mirrors the C# Apple push payload: topic/type/aps/meta where
// aps = {"alert", "sound" (Priority>=5 ? "default" : null),
// "mutable-content": 1}.
func applePayload(apnsPushTopic, notificationTopic string, alertDict map[string]any, meta map[string]any, priority int) map[string]any {
	apsDict := map[string]any{
		"alert":           alertDict,
		"sound":           nil,
		"mutable-content": 1,
	}
	if priority >= 5 {
		apsDict["sound"] = "default"
	}
	return map[string]any{
		"topic": apnsPushTopic,
		"type":  notificationTopic,
		"aps":   apsDict,
		"meta":  meta,
	}
}

// appkPayload mirrors the C# Appk VoIP payload: the notification meta
// spread, aps = {"content-available": 1} and uuid (TryAdd — only when
// absent).
func appkPayload(notification *model.Notification) map[string]any {
	payload := map[string]any{}
	for k, v := range notification.Meta {
		payload[k] = v
	}
	payload["aps"] = map[string]any{"content-available": 1}
	if _, exists := payload["uuid"]; !exists {
		payload["uuid"] = notification.Id.String()
	}
	return payload
}

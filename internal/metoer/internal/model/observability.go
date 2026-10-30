package model

import (
	"github.com/google/uuid"
)

// DeliveryOutcome mirrors DysonNetwork.Ring.DeliveryOutcome.
type DeliveryOutcome int

const (
	DeliveryOutcomeSuccess DeliveryOutcome = iota // 0
	DeliveryOutcomeFailure                        // 1
	DeliveryOutcomeInvalidToken                   // 2
	DeliveryOutcomeSkipped                        // 3
	DeliveryOutcomeHeld                           // 4
)

// EmailDeliveryRecord mirrors SnEmailDeliveryRecord.
type EmailDeliveryRecord struct {
	ModelBase
	Source             string         `json:"source"`
	Provider           string         `json:"provider"`
	Outcome            DeliveryOutcome `json:"outcome"`
	DurationMilliseconds int64        `json:"duration_milliseconds"`
	Error              *string        `json:"error"`
}

// NotificationDeliveryRecord mirrors SnNotificationDeliveryRecord.
type NotificationDeliveryRecord struct {
	ModelBase
	NotificationId      *uuid.UUID     `json:"notification_id"`
	SubscriptionId      *uuid.UUID     `json:"subscription_id"`
	Topic               string         `json:"topic"`
	AppId               *string        `json:"app_id"`
	PushType            *string        `json:"push_type"`
	Provider            string         `json:"provider"`
	Outcome             DeliveryOutcome `json:"outcome"`
	DurationMilliseconds int64         `json:"duration_milliseconds"`
	Error               *string        `json:"error"`
}

// NotificationSendRecord mirrors SnNotificationSendRecord.
type NotificationSendRecord struct {
	ModelBase
	Topic    string  `json:"topic"`
	AppId    *string `json:"app_id"`
	PushType *string `json:"push_type"`
	Source   string  `json:"source"`
}

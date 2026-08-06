package model

// Queue wire types mirroring DysonNetwork.Ring.Services.QueueService plus
// the empirically verified C# serialization behavior:
//
//   - The envelope (QueueMessage) is serialized with
//     InfraObjectCoder.SerializerOptionsWithoutIgnore: snake_case keys,
//     nulls INCLUDED (no omitempty).
//   - The `data` payload for push notifications is serialized with the STJ
//     DEFAULT options (PascalCase keys, nulls included) and NodaTime
//     Instant values emit `{}` (verified: "CreatedAt":{}). The C# consumer
//     round-trips that to epoch, so Go must emit and parse the same shape.
//   - EmailMessage data is PascalCase with nulls included.

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// QueueMessageType mirrors QueueMessageType (declaration order = values).
type QueueMessageType int

const (
	QueueMessageTypeEmail QueueMessageType = iota // 0
	QueueMessageTypePushNotification              // 1
)

// QueueMessage is the pusher_queue envelope. No omitempty: nulls included.
type QueueMessage struct {
	Type                      QueueMessageType `json:"type"`
	TargetId                  *string          `json:"target_id"`
	Data                      string           `json:"data"`
	ExcludedWebSocketDeviceIds []string        `json:"excluded_web_socket_device_ids"`
	IsSavable                 bool             `json:"is_savable"`
}

// EmailMessage is the PascalCase data payload for type=0 messages.
type EmailMessage struct {
	ToName    string `json:"ToName"`
	ToAddress string `json:"ToAddress"`
	Subject   string `json:"Subject"`
	Body      string `json:"Body"`
}

// qInstant replicates C# default-STJ serialization of a NodaTime Instant:
// it emits `{}` and parses `{}` back to the epoch (the C# round-trip
// behavior). RFC3339 strings are also accepted on read so Go-published
// messages written by other Go services remain parseable.
type qInstant struct{}

func (qInstant) MarshalJSON() ([]byte, error) { return []byte("{}"), nil }

func (*qInstant) UnmarshalJSON(b []byte) error {
	s := string(b)
	if s == "{}" || s == "null" || s == `""` {
		return nil
	}
	var str string
	if err := json.Unmarshal(b, &str); err == nil {
		if _, err := time.Parse(time.RFC3339, str); err == nil {
			return nil
		}
	}
	return nil
}

// qNullableInstant mirrors a Nullable<Instant> under default STJ options:
// null emits null, and {} (or null) reads back as nil.
type qNullableInstant struct{}

func (qNullableInstant) MarshalJSON() ([]byte, error) { return []byte("null"), nil }

func (*qNullableInstant) UnmarshalJSON(b []byte) error { return nil }

// QueueNotification is the PascalCase `data` payload for PushNotification
// queue messages (STJ default options: PascalCase keys, nulls included,
// Instant as {}).
type QueueNotification struct {
	Id        uuid.UUID      `json:"Id"`
	Topic     string         `json:"Topic"`
	Title     *string        `json:"Title"`
	Subtitle  *string        `json:"Subtitle"`
	Content   *string        `json:"Content"`
	Meta      map[string]any `json:"Meta"`
	Priority  int            `json:"Priority"`
	ViewedAt  *qNullableInstant `json:"ViewedAt"`
	AppId     *string        `json:"AppId"`
	PushType  *string        `json:"PushType"`
	AccountId string         `json:"AccountId"`
	CreatedAt qInstant       `json:"CreatedAt"`
	UpdatedAt qInstant       `json:"UpdatedAt"`
	DeletedAt *qNullableInstant `json:"DeletedAt"`
}

// NewQueueNotification builds the queue DTO from a model notification,
// normalizing Meta to a non-nil map (C# Dictionary default).
func NewQueueNotification(n *Notification) *QueueNotification {
	meta := n.Meta
	if meta == nil {
		meta = map[string]any{}
	}
	return &QueueNotification{
		Id:        n.Id,
		Topic:     n.Topic,
		Title:     n.Title,
		Subtitle:  n.Subtitle,
		Content:   n.Content,
		Meta:      meta,
		Priority:  n.Priority,
		AppId:     n.AppId,
		PushType:  n.PushType,
		AccountId: n.AccountId.String(),
	}
}

// ToNotification converts the queue DTO back into a model notification.
// Timestamps are the epoch (the C# zeroing behavior); the Id is preserved.
func (q *QueueNotification) ToNotification() (*Notification, error) {
	accountId, err := uuid.Parse(q.AccountId)
	if err != nil {
		return nil, fmt.Errorf("parse queue notification account id: %w", err)
	}
	meta := q.Meta
	if meta == nil {
		meta = map[string]any{}
	}
	return &Notification{
		Topic:    q.Topic,
		Title:    q.Title,
		Subtitle: q.Subtitle,
		Content:  q.Content,
		Meta:     meta,
		Priority: q.Priority,
		AppId:    q.AppId,
		PushType: q.PushType,
		AccountId: accountId,
		ModelBase: ModelBase{Id: q.Id},
	}, nil
}

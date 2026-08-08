package store

import (
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"
	"src.solsynth.dev/sosys/go/pkg/models"
	"src.solsynth.dev/sosys/metoer/internal/model"
)

func encodeJSON(value any) (datatypes.JSON, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return datatypes.JSON(data), nil
}

func decodeJSON(data datatypes.JSON) (map[string]any, error) {
	if len(data) == 0 || string(data) == "null" {
		return nil, nil
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, fmt.Errorf("decode json: %w", err)
	}
	return value, nil
}

func modelTime(value time.Time) models.Time { return models.Time(value) }
func modelTimePtr(value *time.Time) *models.Time {
	if value == nil {
		return nil
	}
	converted := models.Time(*value)
	return &converted
}
func timePtr(value *models.Time) *time.Time {
	if value == nil {
		return nil
	}
	converted := time.Time(*value)
	return &converted
}

func notificationFromEntity(entity *NotificationEntity) (*model.Notification, error) {
	meta, err := decodeJSON(entity.Meta)
	if err != nil {
		return nil, fmt.Errorf("parse notification meta: %w", err)
	}
	return &model.Notification{
		ModelBase: model.ModelBase{Id: entity.ID, CreatedAt: modelTime(entity.CreatedAt), UpdatedAt: modelTime(entity.UpdatedAt), DeletedAt: modelTimePtr(deletedTime(entity.DeletedAt))},
		Topic:     entity.Topic, Title: entity.Title, Subtitle: entity.Subtitle, Content: entity.Content,
		Meta: meta, Priority: entity.Priority, ViewedAt: modelTimePtr(entity.ViewedAt), AppId: entity.AppID,
		PushType: entity.PushType, AccountId: entity.AccountID,
	}, nil
}

func notificationEntityFromModel(value *model.Notification) (NotificationEntity, error) {
	meta, err := encodeJSON(value.Meta)
	if err != nil {
		return NotificationEntity{}, err
	}
	return NotificationEntity{ID: value.Id, EntityBase: EntityBase{CreatedAt: time.Time(value.CreatedAt), UpdatedAt: time.Time(value.UpdatedAt), DeletedAt: deletedAt(value.DeletedAt)}, AccountID: value.AccountId, AppID: value.AppId, Content: value.Content, Meta: meta, Priority: value.Priority, PushType: value.PushType, Subtitle: value.Subtitle, Title: value.Title, Topic: value.Topic, ViewedAt: timePtr(value.ViewedAt)}, nil
}

func subscriptionFromEntity(entity *PushSubscriptionEntity) *model.PushSubscription {
	return &model.PushSubscription{ModelBase: model.ModelBase{Id: entity.ID, CreatedAt: modelTime(entity.CreatedAt), UpdatedAt: modelTime(entity.UpdatedAt), DeletedAt: modelTimePtr(deletedTime(entity.DeletedAt))}, AccountId: entity.AccountID, DeviceId: entity.DeviceID, DeviceToken: entity.DeviceToken, DeviceName: entity.DeviceName, Provider: model.PushProvider(entity.Provider), IsActivated: entity.IsActivated, AppId: entity.AppID, CountDelivered: entity.CountDelivered, LastUsedAt: modelTimePtr(entity.LastUsedAt)}
}
func subscriptionEntityFromModel(value *model.PushSubscription) PushSubscriptionEntity {
	return PushSubscriptionEntity{ID: value.Id, EntityBase: EntityBase{CreatedAt: time.Time(value.CreatedAt), UpdatedAt: time.Time(value.UpdatedAt), DeletedAt: deletedAt(value.DeletedAt)}, AccountID: value.AccountId, AppID: value.AppId, CountDelivered: value.CountDelivered, DeviceID: value.DeviceId, DeviceName: value.DeviceName, DeviceToken: value.DeviceToken, IsActivated: value.IsActivated, LastUsedAt: timePtr(value.LastUsedAt), Provider: int(value.Provider)}
}

func preferenceFromEntity(entity *NotificationPreferenceEntity) *model.NotificationPreference {
	return &model.NotificationPreference{ModelBase: model.ModelBase{Id: entity.ID, CreatedAt: modelTime(entity.CreatedAt), UpdatedAt: modelTime(entity.UpdatedAt), DeletedAt: modelTimePtr(deletedTime(entity.DeletedAt))}, AccountId: entity.AccountID, Topic: entity.Topic, Preference: model.NotificationPreferenceLevel(entity.Preference)}
}
func preferenceEntityFromModel(value *model.NotificationPreference) NotificationPreferenceEntity {
	return NotificationPreferenceEntity{ID: value.Id, EntityBase: EntityBase{CreatedAt: time.Time(value.CreatedAt), UpdatedAt: time.Time(value.UpdatedAt), DeletedAt: deletedAt(value.DeletedAt)}, AccountID: value.AccountId, Topic: value.Topic, Preference: int(value.Preference)}
}

func planFromEntity(entity *EmailSendingPlanEntity) *model.EmailSendingPlan {
	return &model.EmailSendingPlan{ModelBase: model.ModelBase{Id: entity.ID, CreatedAt: modelTime(entity.CreatedAt), UpdatedAt: modelTime(entity.UpdatedAt), DeletedAt: modelTimePtr(deletedTime(entity.DeletedAt))}, SendingPlanKey: entity.SendingPlanKey, CreatedByAccountId: entity.CreatedByAccountID, Subject: entity.Subject, HtmlBody: entity.HTMLBody, BroadcastToAll: entity.BroadcastToAll, RecipientCount: entity.RecipientCount, MaxEmailsPerInterval: entity.MaxEmailsPerInterval, IntervalMinutes: entity.IntervalMinutes, MaxEmailsPerDay: entity.MaxEmailsPerDay, Status: model.EmailSendingPlanStatus(entity.Status), AdvancedIntervalsCount: entity.AdvancedIntervalsCount, PlannedStartAt: modelTime(entity.PlannedStartAt), NextIntervalAt: modelTimePtr(entity.NextIntervalAt), LastAdvancedAt: modelTimePtr(entity.LastAdvancedAt), PausedAt: modelTimePtr(entity.PausedAt), CompletedAt: modelTimePtr(entity.CompletedAt)}
}
func planEntityFromModel(value *model.EmailSendingPlan) EmailSendingPlanEntity {
	return EmailSendingPlanEntity{ID: value.Id, EntityBase: EntityBase{CreatedAt: time.Time(value.CreatedAt), UpdatedAt: time.Time(value.UpdatedAt), DeletedAt: deletedAt(value.DeletedAt)}, SendingPlanKey: value.SendingPlanKey, CreatedByAccountID: value.CreatedByAccountId, Subject: value.Subject, HTMLBody: value.HtmlBody, BroadcastToAll: value.BroadcastToAll, RecipientCount: value.RecipientCount, MaxEmailsPerInterval: value.MaxEmailsPerInterval, IntervalMinutes: value.IntervalMinutes, MaxEmailsPerDay: value.MaxEmailsPerDay, Status: int(value.Status), AdvancedIntervalsCount: value.AdvancedIntervalsCount, PlannedStartAt: time.Time(value.PlannedStartAt), NextIntervalAt: timePtr(value.NextIntervalAt), LastAdvancedAt: timePtr(value.LastAdvancedAt), PausedAt: timePtr(value.PausedAt), CompletedAt: timePtr(value.CompletedAt)}
}

func recipientFromEntity(entity *EmailSendingPlanRecipientEntity) *model.EmailSendingPlanRecipient {
	return &model.EmailSendingPlanRecipient{ModelBase: model.ModelBase{Id: entity.ID, CreatedAt: modelTime(entity.CreatedAt), UpdatedAt: modelTime(entity.UpdatedAt), DeletedAt: modelTimePtr(deletedTime(entity.DeletedAt))}, PlanId: entity.PlanID, AccountId: entity.AccountID, RecipientNameSnapshot: entity.RecipientNameSnapshot, Status: model.EmailSendingPlanRecipientStatus(entity.Status), AttemptCount: entity.AttemptCount, LastIntervalNumber: entity.LastIntervalNumber, LastResolvedEmail: entity.LastResolvedEmail, LastError: entity.LastError, ProcessedAt: modelTimePtr(entity.ProcessedAt)}
}
func recipientEntityFromModel(value *model.EmailSendingPlanRecipient) EmailSendingPlanRecipientEntity {
	return EmailSendingPlanRecipientEntity{ID: value.Id, EntityBase: EntityBase{CreatedAt: time.Time(value.CreatedAt), UpdatedAt: time.Time(value.UpdatedAt), DeletedAt: deletedAt(value.DeletedAt)}, PlanID: value.PlanId, AccountID: value.AccountId, RecipientNameSnapshot: value.RecipientNameSnapshot, Status: int(value.Status), AttemptCount: value.AttemptCount, LastIntervalNumber: value.LastIntervalNumber, LastResolvedEmail: value.LastResolvedEmail, LastError: value.LastError, ProcessedAt: timePtr(value.ProcessedAt)}
}

func advanceFromEntity(entity *EmailSendingPlanAdvanceEntity) *model.EmailSendingPlanAdvance {
	return &model.EmailSendingPlanAdvance{ModelBase: model.ModelBase{Id: entity.ID, CreatedAt: modelTime(entity.CreatedAt), UpdatedAt: modelTime(entity.UpdatedAt), DeletedAt: modelTimePtr(deletedTime(entity.DeletedAt))}, PlanId: entity.PlanID, IntervalNumber: entity.IntervalNumber, IsManual: entity.IsManual, AttemptedCount: entity.AttemptedCount, SentCount: entity.SentCount, SkippedCount: entity.SkippedCount, FailedCount: entity.FailedCount, PendingCountAfter: entity.PendingCountAfter, StartedAt: modelTime(entity.StartedAt), CompletedAt: modelTime(entity.CompletedAt)}
}
func advanceEntityFromModel(value *model.EmailSendingPlanAdvance) EmailSendingPlanAdvanceEntity {
	return EmailSendingPlanAdvanceEntity{ID: value.Id, EntityBase: EntityBase{CreatedAt: time.Time(value.CreatedAt), UpdatedAt: time.Time(value.UpdatedAt), DeletedAt: deletedAt(value.DeletedAt)}, PlanID: value.PlanId, IntervalNumber: value.IntervalNumber, IsManual: value.IsManual, AttemptedCount: value.AttemptedCount, SentCount: value.SentCount, SkippedCount: value.SkippedCount, FailedCount: value.FailedCount, PendingCountAfter: value.PendingCountAfter, StartedAt: time.Time(value.StartedAt), CompletedAt: time.Time(value.CompletedAt)}
}

func deletedTime(value gorm.DeletedAt) *time.Time {
	if !value.Valid {
		return nil
	}
	converted := value.Time
	return &converted
}

func deletedAt(value *models.Time) gorm.DeletedAt {
	if value == nil {
		return gorm.DeletedAt{}
	}
	return gorm.DeletedAt{Time: time.Time(*value), Valid: true}
}

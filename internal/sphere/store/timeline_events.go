package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm/clause"
)

// TimelineServiceEventEntity is one account timeline event folded in from the
// fleet `timeline_events` stream (the same protocol Stargate's account
// timeline uses). The explorable feed interleaves these rows with posts.
type TimelineServiceEventEntity struct {
	ID uuid.UUID `gorm:"column:id;primaryKey;type:uuid"`
	EntityBase
	AccountID uuid.UUID      `gorm:"column:account_id;type:uuid"`
	EventType string         `gorm:"column:event_type;size:128;not null"`
	Source    string         `gorm:"column:source;size:64;not null"`
	SourceID  string         `gorm:"column:source_id;size:128;not null"`
	Payload   datatypes.JSON `gorm:"column:payload;type:jsonb"`
}

func (TimelineServiceEventEntity) TableName() string { return "timeline_service_events" }

// TimelineServiceEvent is one stored event.
type TimelineServiceEvent struct {
	ID        uuid.UUID
	AccountID uuid.UUID
	EventType string
	Source    string
	// SourceID is the upstream envelope event id: (source, source_id) is the
	// idempotency key.
	SourceID   string
	OccurredAt time.Time
	Payload    json.RawMessage
}

func (s *Store) InsertTimelineServiceEvent(ctx context.Context, event TimelineServiceEvent) (bool, error) {
	entity := TimelineServiceEventEntity{
		ID:        uuid.New(),
		AccountID: event.AccountID,
		EventType: event.EventType,
		Source:    event.Source,
		SourceID:  event.SourceID,
		Payload:   datatypes.JSON(event.Payload),
	}
	occurred := event.OccurredAt
	if occurred.IsZero() {
		occurred = time.Now().UTC()
	}
	entity.CreatedAt = occurred
	entity.UpdatedAt = occurred
	result := s.DB.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&entity)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// ListTimelineServiceEventsBefore returns folded events older than cursor
// (newest first), restricted to the given event types. A nil cursor means
// "from now".
func (s *Store) ListTimelineServiceEventsBefore(ctx context.Context, eventTypes []string, cursor *time.Time, limit int) ([]TimelineServiceEvent, error) {
	if len(eventTypes) == 0 || limit <= 0 {
		return nil, nil
	}
	query := s.DB.WithContext(ctx).
		Where("event_type IN ? AND deleted_at IS NULL", eventTypes)
	if cursor != nil {
		query = query.Where("created_at < ?", *cursor)
	}
	var entities []TimelineServiceEventEntity
	if err := query.Order("created_at DESC").Limit(limit).Find(&entities).Error; err != nil {
		return nil, err
	}
	events := make([]TimelineServiceEvent, 0, len(entities))
	for i := range entities {
		events = append(events, TimelineServiceEvent{
			ID:         entities[i].ID,
			AccountID:  entities[i].AccountID,
			EventType:  entities[i].EventType,
			Source:     entities[i].Source,
			OccurredAt: entities[i].CreatedAt,
			Payload:    json.RawMessage(entities[i].Payload),
		})
	}
	return events, nil
}

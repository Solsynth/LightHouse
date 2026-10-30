package events

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	

	eb "src.solsynth.dev/sosys/go/pkg/eventbus"

	"src.solsynth.dev/sosys/metoer/internal/store"
)

// filesystemStream and subject mirror FileMetadataUpdatedEvent
// (StreamName "filesystem_events", Type "filesystem.file.updated.v1").
const (
	filesystemStream   = "filesystem_events"
	filesystemSubject  = "filesystem.file.updated.v1"
	filesystemConsumer = "metoer_filemetadatareferencelistener"
)

// FileMetadataUpdatedEvent mirrors the C# FileMetadataUpdatedEvent wire
// shape (PascalCase keys; the event bus envelope carries event_id/
// timestamp/event_type/stream_name). Only the fields Ring's updater reads
// are modeled.
type FileMetadataUpdatedEvent struct {
	FileId   string `json:"FileId"`
	TaskId   string `json:"TaskId"`
	AccountId string `json:"AccountId"`
	Status   int    `json:"Status"`
}

// ConsumeFileMetadataUpdated runs the filesystem.file.updated.v1 consumer,
// mirroring AddFileMetadataReferenceListener. Ring's schema has no
// SnCloudFileReferenceObject-typed columns, so the updater is a no-op that
// acks valid events (malformed events are also acked — they can never
// succeed on redelivery); DB errors would leave the message unacked. Blocks
// until ctx is cancelled.
func ConsumeFileMetadataUpdated(ctx context.Context, bus *eb.Bus, st *store.Store, log *slog.Logger) error {
	if bus == nil || bus.Conn == nil {
		return nil
	}
	return bus.Consume(ctx, filesystemStream, filesystemSubject, filesystemConsumer, func(payload []byte) error {
		var ev FileMetadataUpdatedEvent
		if err := json.Unmarshal(payload, &ev); err != nil {
			log.Warn("malformed filesystem.file.updated.v1 event", "error", err)
			return nil
		}
		// FileMetadataReferenceUpdater.ApplyAsync enumerates the jsonb columns
		// typed as SnCloudFileReferenceObject; Ring's schema has none, so the
		// update is a no-op (0 rows).
		started := time.Now()
		count, err := st.ApplyFileReferenceUpdates(ctx, ev.FileId)
		if err != nil {
			return err
		}
		log.Debug("applied filesystem.file.updated.v1",
			"file_id", ev.FileId, "rows", count, "took", time.Since(started))
		return nil
	})
}

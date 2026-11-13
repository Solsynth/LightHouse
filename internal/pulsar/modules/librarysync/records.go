package librarysync

import (
	"encoding/json"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"

	"gorm.io/datatypes"
)

func trackRecordOf(row model.LibrarySyncTrack) TrackRecord {
	return TrackRecord{
		Fingerprint: row.Fingerprint,
		Title:       row.Title,
		Artist:      row.Artist,
		AlbumArtist: row.AlbumArtist,
		Album:       row.Album,
		Genre:       row.Genre,
		Year:        row.Year,
		TrackNumber: row.TrackNumber,
		DiscNumber:  row.DiscNumber,
		DurationMs:  row.DurationMs,
		Mbid:        row.Mbid,
		CreatedAt:   formatTime(row.CreatedAt),
		UpdatedAt:   formatTime(row.UpdatedAt),
	}
}

func linkRecordOf(row model.LibrarySyncLink) LinkRecord {
	return LinkRecord{
		SourceID:         row.SourceID,
		ExternalID:       row.ExternalID,
		TrackFingerprint: row.TrackFingerprint,
		AlbumExternalID:  row.AlbumExternalID,
		ArtistExternalID: row.ArtistExternalID,
		ArtID:            row.ArtID,
		LinkedAt:         formatTime(row.LinkedAt),
		LastSeenAt:       formatTime(row.Clock),
	}
}

func lyricsRecordOf(row model.LibrarySyncLyrics) LyricsRecord {
	return LyricsRecord{
		TrackFingerprint:    row.TrackFingerprint,
		Lines:               row.Lines,
		Synced:              row.Synced,
		Language:            row.Language,
		Origin:              row.Origin,
		RemoteID:            row.RemoteID,
		FetchedAt:           formatTime(row.FetchedAt),
		TranslationLanguage: row.TranslationLanguage,
	}
}

func playRecordOf(row model.LibrarySyncPlay) PlayRecord {
	return PlayRecord{
		TrackFingerprint: row.TrackFingerprint,
		SourceID:         row.SourceID,
		StartedAt:        formatTime(row.StartedAt),
		Clock:            formatTime(row.UpdatedAt),
		ListenedMs:       row.ListenedMs,
		PositionMs:       row.PositionMs,
		Completed:        row.Completed,
	}
}

// sessionRecordOf decodes the stored document. A cleared document decodes to
// the zero record, which the client reads as "nothing to adopt" while its clock
// still advances.
func sessionRecordOf(row model.LibrarySyncSession) *SessionRecord {
	record := SessionRecord{}
	if len(row.Data) > 0 {
		_ = json.Unmarshal(row.Data, &record)
	}
	if record.QueueJSON == "" && record.UpdatedAt == "" {
		// A cleared document: the row survives so its clock stays visible, but
		// there is nothing for a client to adopt.
		return nil
	}
	if record.UpdatedAt == "" {
		record.UpdatedAt = formatTime(row.Clock)
	}
	return &record
}

func decodeSettings(data datatypes.JSON) map[string]any {
	values := map[string]any{}
	if len(data) > 0 {
		_ = json.Unmarshal(data, &values)
	}
	return values
}

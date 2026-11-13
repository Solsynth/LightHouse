package librarysync

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/store"
	"src.solsynth.dev/sosys/lighthouse/shared/pkg/errs"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/httpx"
)

// pull returns one page of the account's mirror after the `since` revision.
// Live rows and tombstones both travel; tombstones are reported through
// Removed so a device keeps a locally newer row.
func (h *handler) pull(c *gin.Context) {
	accountID, ok := accountIDOf(c)
	if !ok {
		httpx.Fail(c, errs.Unauthorized(""))
		return
	}

	since, err := strconv.ParseInt(c.DefaultQuery("since", "0"), 10, 64)
	if err != nil || since < 0 {
		httpx.Fail(c, errs.Validation(map[string][]string{"since": {"since must be a non-negative integer."}}))
		return
	}
	maxBatch := h.core.Config.LibrarySync.MaxBatch
	if maxBatch <= 0 {
		maxBatch = 500
	}
	limit := maxBatch
	if raw := c.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			httpx.Fail(c, errs.Validation(map[string][]string{"limit": {"limit must be a positive integer."}}))
			return
		}
		if parsed > maxBatch {
			httpx.Fail(c, errs.Validation(map[string][]string{
				"limit": {"The 'limit' parameter (" + raw + ") exceeds the maximum allowed value (" + strconv.Itoa(maxBatch) + ")."},
			}))
			return
		}
		limit = parsed
	}

	rows := func(entity any) *gorm.DB {
		return h.store.DB.
			Where("account_id = ? AND rev > ?", accountID, since).
			Order("rev ASC").
			Limit(limit + 1)
	}

	var tracks []model.LibrarySyncTrack
	var links []model.LibrarySyncLink
	var lyrics []model.LibrarySyncLyrics
	var plays []model.LibrarySyncPlay
	var sessions []model.LibrarySyncSession
	var settings []model.LibrarySyncSettings
	for _, load := range []func() error{
		func() error { return rows(&model.LibrarySyncTrack{}).Find(&tracks).Error },
		func() error { return rows(&model.LibrarySyncLink{}).Find(&links).Error },
		func() error { return rows(&model.LibrarySyncLyrics{}).Find(&lyrics).Error },
		func() error { return rows(&model.LibrarySyncPlay{}).Find(&plays).Error },
		func() error { return rows(&model.LibrarySyncSession{}).Find(&sessions).Error },
		func() error { return rows(&model.LibrarySyncSettings{}).Find(&settings).Error },
	} {
		if err := load(); err != nil {
			h.fail(c, err)
			return
		}
	}

	// One monotonic cursor spans six tables, so a page may never straddle a
	// revision: the boundary is the last revision that fits the limit in every
	// table, and each table is cut to it. Otherwise a pull with since=pageRev
	// could skip an entity that another table queued at that same revision.
	pageRev, hasMore := pageBoundary(since, limit,
		snapshotOf(tracks, func(row model.LibrarySyncTrack) int64 { return row.Rev }),
		snapshotOf(links, func(row model.LibrarySyncLink) int64 { return row.Rev }),
		snapshotOf(lyrics, func(row model.LibrarySyncLyrics) int64 { return row.Rev }),
		snapshotOf(plays, func(row model.LibrarySyncPlay) int64 { return row.Rev }),
		snapshotOf(sessions, func(row model.LibrarySyncSession) int64 { return row.Rev }),
		snapshotOf(settings, func(row model.LibrarySyncSettings) int64 { return row.Rev }))

	response := PullResponse{
		Rev:      pageRev,
		HasMore:  hasMore,
		Tracks:   make([]TrackRecord, 0, len(tracks)),
		Links:    make([]LinkRecord, 0, len(links)),
		Lyrics:   make([]LyricsRecord, 0, len(lyrics)),
		Plays:    make([]PlayRecord, 0, len(plays)),
		Removed:  map[string][]string{},
		Settings: map[string]any{},
	}

	for _, row := range tracks {
		if row.Rev > pageRev {
			continue
		}
		if row.DeletedAt != nil {
			response.Removed[EntityTrack] = append(response.Removed[EntityTrack],
				encodeRemoval(map[string]any{
					"fingerprint": row.Fingerprint,
					"deleted_at":  formatTime(*row.DeletedAt),
				}))
			continue
		}
		response.Tracks = append(response.Tracks, trackRecordOf(row))
	}
	for _, row := range links {
		if row.Rev > pageRev {
			continue
		}
		if row.DeletedAt != nil {
			response.Removed[EntityLink] = append(response.Removed[EntityLink],
				encodeRemoval(map[string]any{
					"source_id":   row.SourceID,
					"external_id": row.ExternalID,
					"deleted_at":  formatTime(*row.DeletedAt),
				}))
			continue
		}
		response.Links = append(response.Links, linkRecordOf(row))
	}
	for _, row := range lyrics {
		if row.Rev > pageRev {
			continue
		}
		if row.DeletedAt != nil {
			response.Removed[EntityLyrics] = append(response.Removed[EntityLyrics],
				encodeRemoval(map[string]any{
					"track_fingerprint": row.TrackFingerprint,
					"deleted_at":        formatTime(*row.DeletedAt),
				}))
			continue
		}
		response.Lyrics = append(response.Lyrics, lyricsRecordOf(row))
	}
	for _, row := range plays {
		if row.Rev > pageRev {
			continue
		}
		response.Plays = append(response.Plays, playRecordOf(row))
	}
	if len(sessions) > 0 && sessions[0].Rev <= pageRev {
		clock := formatTime(sessions[0].Clock)
		response.SessionClock = &clock
		response.Session = sessionRecordOf(sessions[0])
	}
	if len(settings) > 0 && settings[0].Rev <= pageRev {
		clock := formatTime(settings[0].Clock)
		response.SettingsClock = &clock
		response.Settings = decodeSettings(settings[0].Data)
	}

	httpx.OK(c, response)
}

// status summarises the account's mirror for the Settings row.
func (h *handler) status(c *gin.Context) {
	accountID, ok := accountIDOf(c)
	if !ok {
		httpx.Fail(c, errs.Unauthorized(""))
		return
	}

	response := StatusResponse{}
	rev, err := store.CurrentLibrarySyncRev(h.store.DB, accountID)
	if err != nil {
		h.fail(c, err)
		return
	}
	response.Rev = rev

	count := func(entity any, where string) (int64, error) {
		var total int64
		err := h.store.DB.Model(entity).Where(where, accountID).Count(&total).Error
		return total, err
	}
	response.Tracks, err = count(&model.LibrarySyncTrack{}, "account_id = ? AND deleted_at IS NULL")
	if err != nil {
		h.fail(c, err)
		return
	}
	response.Plays, err = count(&model.LibrarySyncPlay{}, "account_id = ?")
	if err != nil {
		h.fail(c, err)
		return
	}

	var devices []string
	if err := h.store.DB.Raw(`
		SELECT DISTINCT device_id FROM (
			SELECT device_id, account_id FROM pulsar_library_tracks
			UNION ALL SELECT device_id, account_id FROM pulsar_library_links
			UNION ALL SELECT device_id, account_id FROM pulsar_library_lyrics
			UNION ALL SELECT device_id, account_id FROM pulsar_library_plays
			UNION ALL SELECT device_id, account_id FROM pulsar_library_session
			UNION ALL SELECT device_id, account_id FROM pulsar_library_settings
		) AS account_devices WHERE account_id = ?`, accountID).Scan(&devices).Error; err != nil {
		h.fail(c, err)
		return
	}
	response.DeviceCount = int64(len(devices))

	if err := h.store.DB.Raw(`
		SELECT device_id FROM (
			SELECT device_id, rev, account_id FROM pulsar_library_tracks
			UNION ALL SELECT device_id, rev, account_id FROM pulsar_library_links
			UNION ALL SELECT device_id, rev, account_id FROM pulsar_library_lyrics
			UNION ALL SELECT device_id, rev, account_id FROM pulsar_library_plays
			UNION ALL SELECT device_id, rev, account_id FROM pulsar_library_session
			UNION ALL SELECT device_id, rev, account_id FROM pulsar_library_settings
		) AS rows_by_device
		WHERE account_id = ?
		ORDER BY rev DESC
		LIMIT 1`, accountID).Scan(&response.LastDevice).Error; err != nil {
		h.fail(c, err)
		return
	}

	httpx.OK(c, response)
}

func (h *handler) fail(c *gin.Context, err error) {
	h.log.Error("rhythm sync request failed", "path", c.FullPath(), "error", err)
	httpx.Fail(c, errs.New("INTERNAL_ERROR", "An unexpected error occurred.", http.StatusInternalServerError))
}

// snapshot is one table's queried rows reduced to what the page boundary needs.
type snapshot []int64

// snapshotOf flattens a table's rows into their revisions, in order.
func snapshotOf[T any](rows []T, rev func(T) int64) snapshot {
	revs := make(snapshot, 0, len(rows))
	for _, row := range rows {
		revs = append(revs, rev(row))
	}
	return revs
}

// pageBoundary picks the revision a page ends at. A table that returned more
// than limit rows caps the page at its last fitting revision and reports more
// to come; otherwise the page ends at the highest revision any table returned,
// or at since when nothing matched.
func pageBoundary(since int64, limit int, tables ...snapshot) (int64, bool) {
	boundary := since
	hasMore := false
	for _, table := range tables {
		if len(table) > limit {
			candidate := table[limit-1]
			if !hasMore || candidate < boundary {
				boundary = candidate
			}
			hasMore = true
			continue
		}
		for _, rev := range table {
			if rev > boundary {
				boundary = rev
			}
		}
	}
	return boundary, hasMore
}

// encodeRemoval renders one tombstone's natural key plus its clock.
func encodeRemoval(key map[string]any) string {
	encoded, err := json.Marshal(key)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

// formatTime renders a clock at the wire's second precision.
func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339)
}

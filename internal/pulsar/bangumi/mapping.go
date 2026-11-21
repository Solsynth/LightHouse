package bangumi

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/model"
)

// DomainForSubjectType maps a Bangumi subject type to Pulsar's media domain.
// Music has no Pulsar mirror here: the music domain is track-shaped and is
// served by MusicBrainz, while Bangumi's music subjects are releases.
func DomainForSubjectType(subjectType int) string {
	switch subjectType {
	case SubjectTypeBook:
		return model.DomainBook
	case SubjectTypeAnime:
		return model.DomainAnime
	case SubjectTypeGame:
		return model.DomainGaming
	case SubjectTypeReal:
		return model.DomainVideo
	default:
		return ""
	}
}

// SubjectTypeForDomain is the inverse mapping used when only a Pulsar domain
// is known. Music has no Bangumi subject type in this integration.
func SubjectTypeForDomain(domain string) (int, bool) {
	switch domain {
	case model.DomainBook:
		return SubjectTypeBook, true
	case model.DomainAnime:
		return SubjectTypeAnime, true
	case model.DomainGaming:
		return SubjectTypeGame, true
	case model.DomainVideo:
		return SubjectTypeReal, true
	default:
		return 0, false
	}
}

// DomainLabel is the human name in a mirror collection's title.
func DomainLabel(domain string) string {
	switch domain {
	case model.DomainBook:
		return "Books"
	case model.DomainAnime:
		return "Anime"
	case model.DomainGaming:
		return "Games"
	case model.DomainVideo:
		return "Video"
	default:
		return strings.Title(domain)
	}
}

// StatusFromCollectionType maps Bangumi's five collection types onto Pulsar's
// watchlist statuses.
func StatusFromCollectionType(collectionType int) (string, bool) {
	switch collectionType {
	case CollectionWish:
		return model.StatusPlanToWatch, true
	case CollectionDone:
		return model.StatusCompleted, true
	case CollectionDoing:
		return model.StatusWatching, true
	case CollectionOnHold:
		return model.StatusOnHold, true
	case CollectionDropped:
		return model.StatusDropped, true
	default:
		return "", false
	}
}

// CollectionTypeFromStatus maps a Pulsar status back to Bangumi. An unknown or
// absent status becomes "wish" so a newly pushed item lands in a real list.
func CollectionTypeFromStatus(status string) int {
	switch status {
	case model.StatusCompleted:
		return CollectionDone
	case model.StatusWatching:
		return CollectionDoing
	case model.StatusOnHold:
		return CollectionOnHold
	case model.StatusDropped:
		return CollectionDropped
	default:
		return CollectionWish
	}
}

// LocalState is the subset of a collection item the mirror owns.
type LocalState struct {
	Status         string
	Rating         *int
	Note           *string
	Progress       *int
	VolumeProgress *int
}

// FromItem reads the mirrored fields off a stored collection item.
func FromItem(item model.CollectionItem) LocalState {
	state := LocalState{Rating: item.Rating, Note: item.Note, Progress: item.Progress, VolumeProgress: item.VolumeProgress}
	if item.Status != nil {
		state.Status = *item.Status
	}
	return state
}

// RemoteState is the subset of a Bangumi collection row the mirror owns.
type RemoteState struct {
	Type      int
	Rate      int
	Comment   string
	EpStatus  int
	VolStatus int
}

// FromRemote reads the mirrored fields off a Bangumi collection row.
func FromRemote(row SubjectCollection) RemoteState {
	return RemoteState{
		Type:      row.Type,
		Rate:      row.Rate,
		Comment:   row.Comment,
		EpStatus:  row.EpStatus,
		VolStatus: row.VolStatus,
	}
}

// LocalFingerprint hashes the values a local edit can move. Two equal hashes
// mean the local side has not changed since the last agreement.
func LocalFingerprint(state LocalState) string {
	h := sha256.New()
	fmt.Fprintf(h, "status=%s\n", state.Status)
	fmt.Fprintf(h, "rating=%s\n", optionalInt(state.Rating))
	fmt.Fprintf(h, "note=%s\n", optionalString(state.Note))
	fmt.Fprintf(h, "progress=%s\n", optionalInt(state.Progress))
	fmt.Fprintf(h, "volume_progress=%s\n", optionalInt(state.VolumeProgress))
	return hex.EncodeToString(h.Sum(nil))
}

// RemoteFingerprint hashes the same logical fields on Bangumi's side.
func RemoteFingerprint(state RemoteState) string {
	h := sha256.New()
	status, _ := StatusFromCollectionType(state.Type)
	fmt.Fprintf(h, "status=%s\n", status)
	fmt.Fprintf(h, "rating=%s\n", optionalInt(ratingPointer(state.Rate)))
	fmt.Fprintf(h, "note=%s\n", optionalString(commentPointer(state.Comment)))
	fmt.Fprintf(h, "progress=%s\n", optionalInt(progressPointer(state.EpStatus)))
	fmt.Fprintf(h, "volume_progress=%s\n", optionalInt(progressPointer(state.VolStatus)))
	return hex.EncodeToString(h.Sum(nil))
}

// PayloadFromLocal builds the write body for one item. For books Bangumi
// accepts chapter and volume progress; for other subject types progress is
// derived from episode collections and must not be sent directly.
func PayloadFromLocal(state LocalState, domain string, create bool) CollectionPayload {
	payload := CollectionPayload{}
	if create || strings.TrimSpace(state.Status) != "" {
		value := CollectionTypeFromStatus(state.Status)
		payload.Type = &value
	}
	if state.Rating != nil {
		rate := *state.Rating
		if rate < 0 {
			rate = 0
		}
		if rate > 10 {
			rate = 10
		}
		payload.Rate = &rate
	}
	if state.Note != nil {
		comment := *state.Note
		payload.Comment = &comment
	}
	if domain == model.DomainBook {
		if state.Progress != nil {
			value := *state.Progress
			if value < 0 {
				value = 0
			}
			payload.EpStatus = &value
		}
		if state.VolumeProgress != nil {
			value := *state.VolumeProgress
			if value < 0 {
				value = 0
			}
			payload.VolStatus = &value
		}
	}
	return payload
}

// MergeRemote returns the remote state that results from writing the local
// fields onto the current remote row. Fields the payload omits stay as they
// were; this lets the caller store an accurate remote fingerprint without a
// follow-up read.
func MergeRemote(current RemoteState, local LocalState, domain string, create bool) RemoteState {
	merged := current
	if create || strings.TrimSpace(local.Status) != "" {
		merged.Type = CollectionTypeFromStatus(local.Status)
	}
	if local.Rating != nil {
		rate := *local.Rating
		if rate < 0 {
			rate = 0
		}
		if rate > 10 {
			rate = 10
		}
		merged.Rate = rate
	}
	if local.Note != nil {
		merged.Comment = *local.Note
	}
	if domain == model.DomainBook {
		if local.Progress != nil && *local.Progress > 0 {
			merged.EpStatus = *local.Progress
		}
		if local.VolumeProgress != nil && *local.VolumeProgress > 0 {
			merged.VolStatus = *local.VolumeProgress
		}
	}
	return merged
}

// ApplyRemote copies the remote fields onto a local item. A zero rate clears
// the local rating; progress is copied for every domain so reading the list
// shows how far the user is, even where writes are not supported.
func ApplyRemote(item *model.CollectionItem, remote RemoteState) {
	if status, ok := StatusFromCollectionType(remote.Type); ok {
		value := status
		item.Status = &value
	}
	item.Rating = ratingPointer(remote.Rate)
	item.Note = commentPointer(remote.Comment)
	item.Progress = progressPointer(remote.EpStatus)
	item.VolumeProgress = progressPointer(remote.VolStatus)
}

func optionalInt(value *int) string {
	if value == nil {
		return "nil"
	}
	return strconv.Itoa(*value)
}

func optionalString(value *string) string {
	if value == nil {
		return "nil"
	}
	return *value
}

func ratingPointer(rate int) *int {
	if rate <= 0 {
		return nil
	}
	value := rate
	return &value
}

func commentPointer(comment string) *string {
	if strings.TrimSpace(comment) == "" {
		return nil
	}
	value := comment
	return &value
}

func progressPointer(progress int) *int {
	if progress <= 0 {
		return nil
	}
	value := progress
	return &value
}

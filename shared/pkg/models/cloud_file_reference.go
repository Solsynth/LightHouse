package models

import (
	"encoding/json"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"
)

// ContentSensitiveMark mirrors DysonNetwork.Shared.Models.ContentSensitiveMark.
// Enums serialize as integers (the C# jsonb serializer has no string enum
// converter), so values match the declaration order exactly.
type ContentSensitiveMark int

const (
	ContentSensitiveMarkLanguage ContentSensitiveMark = iota
	ContentSensitiveMarkSexualContent
	ContentSensitiveMarkViolence
	ContentSensitiveMarkProfanity
	ContentSensitiveMarkHateSpeech
	ContentSensitiveMarkRacism
	ContentSensitiveMarkAdultContent
	ContentSensitiveMarkDrugAbuse
	ContentSensitiveMarkAlcoholAbuse
	ContentSensitiveMarkGambling
	ContentSensitiveMarkSelfHarm
	ContentSensitiveMarkChildAbuse
	ContentSensitiveMarkOther
)

// MetaMap is a string-keyed object map that always marshals as a JSON
// object: a nil map emits {} instead of null, so the C# side never reads
// null into its non-nullable Dictionary properties.
type MetaMap map[string]any

func (m MetaMap) MarshalJSON() ([]byte, error) {
	if m == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(map[string]any(m))
}

// SnCloudFileReferenceObject mirrors
// DysonNetwork.Shared.Models.CloudFileReferenceObject: the value stored in
// jsonb columns that reference a cloud file. It caches the properties that
// don't change on the file to reduce database load, and supports both local
// cloud files (with Id) and external/fediverse files (with Url).
//
// FileMeta and UserMeta are intentionally not omitempty and marshal as {}
// when empty: the C# class initializes them to empty dictionaries and
// reading a jsonb value without the keys leaves them null, which can NRE on
// the C# side. Always emitting them keeps the wire shape C#-safe in both
// directions.
type SnCloudFileReferenceObject struct {
	Id              string                 `json:"id"`
	Name            string                 `json:"name,omitempty"`
	FileMeta        MetaMap                `json:"file_meta"`
	UserMeta        MetaMap                `json:"user_meta"`
	SensitiveMarks  []ContentSensitiveMark `json:"sensitive_marks,omitempty"`
	MimeType        string                 `json:"mime_type,omitempty"`
	Hash            string                 `json:"hash,omitempty"`
	Size            int64                  `json:"size,omitempty"`
	HasCompression  bool                   `json:"has_compression,omitempty"`
	Url             string                 `json:"url,omitempty"`
	Width           *int32                 `json:"width,omitempty"`
	Height          *int32                 `json:"height,omitempty"`
	Blurhash        string                 `json:"blurhash,omitempty"`
	Usage           string                 `json:"usage,omitempty"`
	ApplicationType string                 `json:"application_type,omitempty"`
	CreatedAt       *Time                  `json:"created_at,omitempty"`
	UpdatedAt       *Time                  `json:"updated_at,omitempty"`
	DeletedAt       *Time                  `json:"deleted_at,omitempty"`
}

// FromProtoValue mirrors SnCloudFileReferenceObject.FromProtoValue: it builds
// a jsonb reference from a DyCloudFile proto as the C# fleet does, falling
// back to the file-level meta when the object meta is absent and stamping
// timestamps from the proto (or now when missing).
func FromProtoValue(f *gen.DyCloudFile) SnCloudFileReferenceObject {
	now := time.Now().UTC()
	ref := SnCloudFileReferenceObject{
		Id:             f.Id,
		Name:           f.Name,
		FileMeta:       decodeMeta(f.Object, f.FileMeta),
		UserMeta:       decodeObjectBytes(f.UserMeta),
		MimeType:       f.MimeType,
		Hash:           f.Hash,
		Size:           f.Size,
		HasCompression: f.HasCompression,
	}
	if len(f.SensitiveMarks) > 0 {
		var marks []ContentSensitiveMark
		if err := json.Unmarshal(f.SensitiveMarks, &marks); err == nil && len(marks) > 0 {
			ref.SensitiveMarks = marks
		}
	}
	if f.Url != "" {
		ref.Url = f.Url
	}
	if f.Width != nil {
		ref.Width = f.Width
	}
	if f.Height != nil {
		ref.Height = f.Height
	}
	if f.Blurhash != nil {
		ref.Blurhash = *f.Blurhash
	}
	if f.Usage != nil {
		ref.Usage = *f.Usage
	}
	if f.ApplicationType != nil {
		ref.ApplicationType = *f.ApplicationType
	}
	ref.CreatedAt = timestampToTime(f.CreatedAt, now)
	if f.UpdatedAt != nil {
		ref.UpdatedAt = timestampToTime(f.UpdatedAt, now)
	} else {
		ref.UpdatedAt = ref.CreatedAt
	}
	return ref
}

// decodeMeta picks the object's meta bytes when present, mirroring the C#
// Object-first FileMeta precedence.
func decodeMeta(obj *gen.DyFileObject, fallback []byte) MetaMap {
	if obj != nil {
		return decodeObjectBytes(obj.Meta)
	}
	return decodeObjectBytes(fallback)
}

func decodeObjectBytes(b []byte) MetaMap {
	if len(b) == 0 {
		return MetaMap{}
	}
	var m MetaMap
	if err := json.Unmarshal(b, &m); err != nil {
		return MetaMap{}
	}
	return m
}

func timestampToTime(ts *timestamppb.Timestamp, fallback time.Time) *Time {
	if ts == nil {
		return NewTime(fallback)
	}
	return NewTime(ts.AsTime())
}

package models

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"
)

// TestCloudFileReferenceJSON pins the jsonb wire shape: snake_case keys,
// integer enum values, NodaTime-style ISO-8601 timestamps, and file_meta /
// user_meta always present (the C# class initializes them non-null).
func TestCloudFileReferenceJSON(t *testing.T) {
	created := time.Date(2026, 8, 6, 6, 29, 50, 0, time.UTC)
	ref := SnCloudFileReferenceObject{
		Id:              "file-1",
		Name:            "avatar.webp",
		FileMeta:        map[string]any{"width": 512, "animated": false},
		UserMeta:        map[string]any{"taken_at": "2026-08-01T00:00:00Z"},
		SensitiveMarks:  []ContentSensitiveMark{ContentSensitiveMarkLanguage, ContentSensitiveMarkAdultContent},
		MimeType:        "image/webp",
		Hash:            "abc123",
		Size:            2048,
		HasCompression:  true,
		Url:             "https://cdn.example/files/file-1",
		Width:           new(int32(512)),
		Height:          new(int32(512)),
		Blurhash:        "LEHV6nWB2yk8pyo0adR*.7kCMdnj",
		Usage:           "avatar",
		ApplicationType: "image",
		CreatedAt:       NewTime(created),
		UpdatedAt:       NewTime(created),
	}
	data, err := json.Marshal(ref)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"id", "name", "file_meta", "user_meta", "sensitive_marks", "mime_type", "hash", "size", "has_compression", "url", "width", "height", "blurhash", "usage", "application_type", "created_at", "updated_at"} {
		if _, ok := m[key]; !ok {
			t.Errorf("missing key %q in %s", key, data)
		}
	}
	if got := m["sensitive_marks"].([]any); len(got) != 2 || got[0] != float64(0) || got[1] != float64(6) {
		t.Errorf("sensitive_marks = %v, want [0 6]", got)
	}
	if got := m["created_at"]; got != "2026-08-06T06:29:50Z" {
		t.Errorf("created_at = %v, want RFC3339 UTC", got)
	}
}

// TestCloudFileReferenceEmptyMeta pins that empty file_meta/user_meta are
// still emitted as {} (C#-safety for non-nullable dictionary properties).
func TestCloudFileReferenceEmptyMeta(t *testing.T) {
	ref := SnCloudFileReferenceObject{Id: "file-1"}
	data, err := json.Marshal(ref)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, `"file_meta":{}`) || !strings.Contains(s, `"user_meta":{}`) {
		t.Fatalf("empty meta must be emitted as {}: %s", s)
	}
}

// TestFromProtoValue mirrors SnCloudFileReferenceObject.FromProtoValue: a
// DyCloudFile proto (as Drive returns it) maps to the full jsonb shape, with
// the object meta taking precedence over the file meta.
func TestFromProtoValue(t *testing.T) {
	created := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	f := &gen.DyCloudFile{
		Id:              "file-2",
		Name:            "banner.png",
		FileMeta:        []byte(`{"fallback":true}`),
		UserMeta:        []byte(`{"author":"u1"}`),
		SensitiveMarks:  []byte(`[1,2]`),
		MimeType:        "image/png",
		Hash:            "deadbeef",
		Size:            4096,
		HasCompression:  false,
		Url:             "https://ext.example/banner.png",
		Width:           new(int32(1024)),
		Height:          new(int32(768)),
		Blurhash:        new("LEHV6nWB"),
		Usage:           new("background"),
		ApplicationType: new("image"),
		Object:          &gen.DyFileObject{Meta: []byte(`{"width":1024,"height":768,"animated":true}`)},
		CreatedAt:       timestamppb.New(created),
		UpdatedAt:       timestamppb.New(created),
	}
	ref := FromProtoValue(f)
	if ref.Id != "file-2" || ref.Name != "banner.png" {
		t.Errorf("id/name = %q/%q", ref.Id, ref.Name)
	}
	// Object meta wins over file meta.
	if ref.FileMeta["fallback"] != nil || ref.FileMeta["animated"] != true {
		t.Errorf("file_meta = %v, want object meta {animated:true}", ref.FileMeta)
	}
	if ref.UserMeta["author"] != "u1" {
		t.Errorf("user_meta = %v", ref.UserMeta)
	}
	if len(ref.SensitiveMarks) != 2 || ref.SensitiveMarks[0] != ContentSensitiveMarkSexualContent || ref.SensitiveMarks[1] != ContentSensitiveMarkViolence {
		t.Errorf("sensitive_marks = %v", ref.SensitiveMarks)
	}
	if ref.Size != 4096 || ref.Width == nil || *ref.Width != 1024 || ref.Height == nil || *ref.Height != 768 {
		t.Errorf("size/width/height = %d/%v/%v", ref.Size, ref.Width, ref.Height)
	}
	if ref.Usage != "background" || ref.ApplicationType != "image" {
		t.Errorf("usage/application_type = %q/%q", ref.Usage, ref.ApplicationType)
	}
	if ref.CreatedAt == nil || !ref.CreatedAt.FromTime().Equal(created) {
		t.Errorf("created_at = %v, want %v", ref.CreatedAt, created)
	}
	if ref.UpdatedAt == nil || !ref.UpdatedAt.FromTime().Equal(created) {
		t.Errorf("updated_at = %v, want %v", ref.UpdatedAt, created)
	}
}

// TestFromProtoValueNilTimestamps pins the C# fallback: missing proto
// timestamps stamp now for both created_at and updated_at.
func TestFromProtoValueNilTimestamps(t *testing.T) {
	f := &gen.DyCloudFile{Id: "file-3"}
	ref := FromProtoValue(f)
	if ref.CreatedAt == nil || ref.UpdatedAt == nil {
		t.Fatal("created_at/updated_at must fall back to now")
	}
	if !ref.CreatedAt.FromTime().Equal(ref.UpdatedAt.FromTime()) {
		t.Errorf("updated_at %v != created_at %v", ref.UpdatedAt, ref.CreatedAt)
	}
}

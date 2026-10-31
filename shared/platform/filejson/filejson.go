// Package filejson decodes the legacy C# jsonb columns whose typed objects are
// emitted on the wire: cloud-file references plus embedded value objects such
// as PostEmbedView.
//
// Rows written by the legacy C# fleet serialize CloudFileReferenceObject with
// System.Text.Json's default PascalCase property names ("FileMeta",
// "MimeType", "Url"), while rows written here use the snake_case tags of
// models.SnCloudFileReferenceObject. Go's encoding/json matches a key against
// its field case-insensitively only when the key equals the tag, so a legacy
// "Id" still lands on `id` but "FileMeta" never reaches `file_meta`. A plain
// Unmarshal of a legacy row therefore silently drops the metadata tables and
// the wire emits empty file_meta/user_meta objects.
//
// The decoders here rewrite the top-level keys to snake_case before the typed
// decode. Values are carried as raw JSON, so nested objects (which are other
// systems' data, such as the EXIF dictionary in FileMeta) and 64-bit numbers
// survive unchanged.
package filejson

import (
	"encoding/json"
	"strings"
	"unicode"

	"src.solsynth.dev/sosys/lighthouse/shared/pkg/models"
)

// References decodes a jsonb array of references. It never returns nil: an
// empty column yields an empty slice so the wire shape stays [] rather than
// null.
func References(raw []byte) []models.SnCloudFileReferenceObject {
	out := []models.SnCloudFileReferenceObject{}
	if len(raw) == 0 {
		return out
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return out
	}
	for _, entry := range entries {
		var reference models.SnCloudFileReferenceObject
		_ = json.Unmarshal(normalize(entry), &reference)
		out = append(out, reference)
	}
	return out
}

// Reference decodes one jsonb reference. It returns nil when the column is
// empty, the JSON literal null or a blob that is not an object.
func Reference(raw []byte) *models.SnCloudFileReferenceObject {
	if len(raw) == 0 {
		return nil
	}
	var reference *models.SnCloudFileReferenceObject
	if err := json.Unmarshal(normalize(raw), &reference); err != nil {
		return nil
	}
	return reference
}

// Unmarshal decodes a jsonb column into a typed destination after normalizing
// its top-level keys. It is the typed counterpart of Value: use it for legacy
// value objects whose wire shape is a Go struct rather than an any.
func Unmarshal(raw []byte, destination any) error {
	return json.Unmarshal(normalize(raw), destination)
}

// Value decodes a typed jsonb object for a wire field typed as any, keeping the
// column an object with snake_case keys. It serves file references and other
// legacy value objects (for example posts.embed_view), which the C# fleet
// serialized with the same PascalCase keys. An empty column yields nil (wire
// null); a non-object blob is decoded as-is.
func Value(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil
		}
		return value
	}
	normalized := make(map[string]any, len(object))
	for key, value := range object {
		normalized[toSnakeCase(key)] = value
	}
	return normalized
}

// normalize rewrites the top-level keys of a JSON object to snake_case. Only
// the keys of the reference itself are rewritten: file_meta and user_meta hold
// data produced by other systems, not fields of this fleet's struct, so their
// contents are passed through verbatim.
func normalize(raw []byte) []byte {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return raw
	}
	normalized := make(map[string]json.RawMessage, len(object))
	for key, value := range object {
		snake := toSnakeCase(key)
		if key != snake {
			if _, present := object[snake]; present {
				// An exact snake_case key wins over a legacy alias; without
				// this the winner would depend on map iteration order.
				continue
			}
		}
		normalized[snake] = value
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return raw
	}
	return encoded
}

// toSnakeCase rewrites an identifier like "MimeType", "fileMeta" or "ID" to
// its snake_case form ("mime_type", "file_meta", "id").
func toSnakeCase(value string) string {
	runes := []rune(value)
	var out strings.Builder
	out.Grow(len(value) + 4)
	for index, r := range runes {
		if unicode.IsUpper(r) {
			if index > 0 {
				prev := runes[index-1]
				nextLower := index+1 < len(runes) && unicode.IsLower(runes[index+1])
				if unicode.IsLower(prev) || unicode.IsDigit(prev) || (unicode.IsUpper(prev) && nextLower) {
					out.WriteRune('_')
				}
			}
			out.WriteRune(unicode.ToLower(r))
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}

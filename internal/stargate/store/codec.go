package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"gorm.io/datatypes"

	"src.solsynth.dev/sosys/stargate/model"
)

// encodeJSON preserves JSON null as the literal bytes "null". Callers use a
// nil *datatypes.JSON for SQL NULL, keeping SQL NULL and JSON null distinct.
func encodeJSON(value any) (datatypes.JSON, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal json: %w", err)
	}
	return datatypes.JSON(encoded), nil
}

func encodeJSONPtr(value any) (*datatypes.JSON, error) {
	if value == nil {
		return nil, nil
	}
	encoded, err := encodeJSON(value)
	if err != nil {
		return nil, err
	}
	return &encoded, nil
}

// decodeJSONArray decodes a raw jsonb column value ([]byte from the driver)
// into a string slice. Returns nil for nil or empty input.
func decodeJSONArray(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

func decodeJSON(raw *datatypes.JSON, destination any) error {
	if raw == nil || len(*raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(*raw, destination); err != nil {
		return fmt.Errorf("unmarshal json: %w", err)
	}
	return nil
}

func decodeJSONValue(raw datatypes.JSON, destination any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, destination); err != nil {
		return fmt.Errorf("unmarshal json: %w", err)
	}
	return nil
}

// DecodeJSONObject decodes a legacy C# jsonb value object into a typed
// destination, normalizing the top-level keys to snake_case first. A nil or
// JSON-null column leaves destination untouched.
func DecodeJSONObject(raw *datatypes.JSON, destination any) error {
	if raw == nil || len(*raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(normalizeReferenceKeys(*raw), destination); err != nil {
		return fmt.Errorf("unmarshal json: %w", err)
	}
	return nil
}

// DecodeFileReference decodes a jsonb file-reference column into the wire
// model. Rows written by the legacy C# fleet carry System.Text.Json PascalCase
// keys ("Id", "FileMeta", "MimeType"), and a typed decode would silently drop
// every key whose snake_case tag does not match case-insensitively — clients
// strict-cast the object, so the blob is normalized before decoding. A nil or
// JSON-null column yields nil.
func DecodeFileReference(raw *datatypes.JSON) *model.SnCloudFileReferenceObject {
	if raw == nil || len(*raw) == 0 {
		return nil
	}
	var reference *model.SnCloudFileReferenceObject
	if err := DecodeJSONObject(raw, &reference); err != nil {
		return nil
	}
	return reference
}

// normalizeReferenceKeys rewrites the top-level keys of a jsonb reference to
// snake_case, keeping nested objects (file_meta/user_meta carry other systems'
// data) untouched. This mirrors platform/filejson, which Stargate cannot import
// because it is a separate module.
func normalizeReferenceKeys(raw []byte) []byte {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return raw
	}
	normalized := make(map[string]json.RawMessage, len(object))
	for key, value := range object {
		snake := toSnakeCase(key)
		if key != snake {
			if _, present := object[snake]; present {
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
				if unicode.IsLower(prev) || unicode.IsDigit(prev) || nextLower {
					out.WriteByte('_')
				}
			}
			out.WriteRune(unicode.ToLower(r))
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}

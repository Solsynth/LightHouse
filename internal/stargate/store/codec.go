package store

import (
	"encoding/json"
	"fmt"

	"gorm.io/datatypes"

	"src.solsynth.dev/sosys/lighthouse/shared/platform/filejson"
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

// decodeJSON decodes a nullable jsonb column through the shared codec without
// rewriting its keys: these columns carry data the producer owns (metadata,
// coordinates, config), so their key spelling must survive verbatim.
func decodeJSON(raw *datatypes.JSON, destination any) error {
	if raw == nil {
		return nil
	}
	return filejson.Decode(*raw, destination)
}

// DecodeJSONObject decodes a legacy C# jsonb value object into a typed
// destination, normalizing the top-level keys to snake_case first. A nil or
// JSON-null column leaves destination untouched.
func DecodeJSONObject(raw *datatypes.JSON, destination any) error {
	if raw == nil || len(*raw) == 0 {
		return nil
	}
	return filejson.Unmarshal(*raw, destination)
}

// DecodeFileReference decodes a nullable jsonb file-reference column through
// the shared codec, which understands the legacy C# PascalCase keys and drops
// a malformed property instead of erasing the reference. A nil column yields
// nil.
func DecodeFileReference(raw *datatypes.JSON) *model.SnCloudFileReferenceObject {
	if raw == nil {
		return nil
	}
	return filejson.Reference(*raw)
}

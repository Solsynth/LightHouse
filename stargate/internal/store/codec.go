package store

import (
	"encoding/json"
	"fmt"

	"gorm.io/datatypes"
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
